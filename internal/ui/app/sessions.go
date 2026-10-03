package app

import (
	"math/rand/v2"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// switcherMode is what the session switcher's keys do.
type switcherMode int

const (
	modePick   switcherMode = iota // move, switch, filter
	modeNew                        // type a new session's name
	modeRename                     // type the highlighted session's new name
	modeKill                       // confirm killing the highlighted session
)

// sessionSwitcher is the session switcher's state, kept free of Gio windows
// so its keys can be tested on their own. The drawing is in sessiondraw.go.
type sessionSwitcher struct {
	open      bool
	sel       string // the highlighted session
	filter    string
	filtering bool // typed keys go to the filter, even j, k, n, r and x
	mode      switcherMode
	field     string // the name typed in modeNew and modeRename
	fresh     bool   // field is the suggested name: the first key replaces it
	err       string // why Enter did not take the name

	openedAt time.Time
	selY     float32   // the highlight's drawn top, easing toward the row
	lastAt   time.Time // the frame selY was last eased in
	selAt    time.Time // when sel last changed, for the preview's fade
	draw     switcherDraw
}

// switcherResult is what a key asks the window to do: send a message,
// show another session, or remember the name of a session being made.
type switcherResult struct {
	send       any
	show       string
	newSession string
}

// openAt opens the switcher on current in mode ("pick", "new" or
// "rename").
func (s *sessionSwitcher) openAt(st *model.State, current, mode string, now time.Time) {
	if !s.open {
		s.openedAt, s.selY = now, -1
	}
	s.open, s.filter, s.filtering, s.err = true, "", false, ""
	s.setSel(current, now)
	s.mode = modePick
	switch mode {
	case "new":
		s.startNew(st)
	case "rename":
		s.startRename(st)
	}
}

func (s *sessionSwitcher) close() { s.open, s.mode = false, modePick }

func (s *sessionSwitcher) setSel(id string, now time.Time) {
	if id != s.sel {
		s.sel, s.selAt = id, now
	}
}

// rows are the sessions the list shows: the ones whose name holds the
// filter, case-insensitively, in the switcher's order.
func (s *sessionSwitcher) rows(st *model.State) []model.Session {
	f := strings.ToLower(s.filter)
	return slices.DeleteFunc(slices.Clone(st.Sessions), func(x model.Session) bool {
		return !strings.Contains(strings.ToLower(x.Name), f)
	})
}

// fix keeps the highlight on a row the list shows.
func (s *sessionSwitcher) fix(st *model.State, now time.Time) {
	rows := s.rows(st)
	if !slices.ContainsFunc(rows, func(x model.Session) bool { return x.ID == s.sel }) {
		id := ""
		if len(rows) > 0 {
			id = rows[0].ID
		}
		s.setSel(id, now)
	}
	if s.mode == modeRename || s.mode == modeKill {
		if st.Session(s.sel) == nil {
			s.mode = modePick
		}
	}
}

func (s *sessionSwitcher) move(st *model.State, d int, now time.Time) {
	rows := s.rows(st)
	i := slices.IndexFunc(rows, func(x model.Session) bool { return x.ID == s.sel })
	if len(rows) == 0 || i < 0 {
		return
	}
	s.setSel(rows[min(max(i+d, 0), len(rows)-1)].ID, now)
}

func (s *sessionSwitcher) startNew(st *model.State) {
	s.mode, s.field, s.fresh, s.err = modeNew, suggestName(st), true, ""
}

func (s *sessionSwitcher) startRename(st *model.State) {
	if x := st.Session(s.sel); x != nil {
		s.mode, s.field, s.fresh, s.err = modeRename, x.Name, true, ""
	}
}

// suggestName is a generated session name no session has.
func suggestName(st *model.State) string {
	for n := 0; ; n++ {
		if name := model.SessionName(rand.IntN, n); st.SessionNamed(name) == nil {
			return name
		}
	}
}

// nameChar is the character a key types into a session name or the
// filter, if any: letters (upper case with Shift), digits, '-', '_', '.';
// Space types '-'.
func nameChar(e key.Event) (rune, bool) {
	if e.Modifiers&^key.ModShift != 0 {
		return 0, false
	}
	if e.Name == key.NameSpace {
		return '-', true
	}
	r, size := utf8.DecodeRuneInString(string(e.Name))
	if size != len(e.Name) {
		return 0, false
	}
	switch {
	case r >= 'A' && r <= 'Z':
		if e.Modifiers&key.ModShift == 0 {
			r += 'a' - 'A'
		}
		return r, true
	case r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		return r, true
	}
	return 0, false
}

// key applies one key press; pane is the window's focused pane, where a new
// session starts.
func (s *sessionSwitcher) key(st *model.State, pane string, e key.Event, now time.Time) switcherResult {
	var res switcherResult
	if e.State != key.Press || modifierKey(e.Name) {
		return res
	}
	s.fix(st, now)
	switch s.mode {
	case modeNew, modeRename:
		switch e.Name {
		case key.NameEscape:
			s.mode, s.err = modePick, ""
		case key.NameReturn, key.NameEnter:
			name := strings.TrimSpace(s.field)
			editing := ""
			if s.mode == modeRename {
				editing = s.sel
			}
			if other := st.SessionNamed(name); name == "" {
				s.err = "Type a name."
			} else if other != nil && other.ID != editing {
				s.err = "A session is already named " + name + "."
			} else if s.mode == modeNew {
				res.send = proto.SessionNew{Name: name, FromPane: pane}
				res.newSession = name
				s.close()
			} else {
				if name != st.Session(s.sel).Name {
					res.send = proto.SessionRename{SessionID: s.sel, Name: name}
				}
				s.mode = modePick
			}
		case key.NameDeleteBackward:
			if s.fresh {
				s.field = ""
			} else if _, size := utf8.DecodeLastRuneInString(s.field); size > 0 {
				s.field = s.field[:len(s.field)-size]
			}
			s.fresh, s.err = false, ""
		default:
			if r, ok := nameChar(e); ok {
				if s.fresh {
					s.field = ""
				}
				if utf8.RuneCountInString(s.field) < 48 {
					s.field += string(r)
				}
				s.fresh, s.err = false, ""
			}
		}
		return res
	case modeKill:
		switch e.Name {
		case "Y", key.NameReturn, key.NameEnter:
			res.send = proto.SessionKill{SessionID: s.sel}
			s.mode = modePick
		case "N", key.NameEscape:
			s.mode = modePick
		}
		return res
	}
	switch e.Name {
	case key.NameEscape:
		if s.filtering {
			s.filter, s.filtering = "", false
			s.fix(st, now)
		} else {
			s.close()
		}
		return res
	case key.NameUpArrow:
		s.move(st, -1, now)
		return res
	case key.NameDownArrow:
		s.move(st, 1, now)
		return res
	case key.NameTab:
		if e.Modifiers == key.ModShift {
			s.move(st, -1, now)
		} else {
			s.move(st, 1, now)
		}
		return res
	case key.NameReturn, key.NameEnter:
		if s.sel != "" {
			res.show = s.sel
			s.close()
		}
		return res
	case key.NameDeleteBackward:
		if _, size := utf8.DecodeLastRuneInString(s.filter); size > 0 {
			s.filter = s.filter[:len(s.filter)-size]
			s.fix(st, now)
		}
		return res
	}
	if e.Modifiers&^key.ModShift != 0 {
		return res
	}
	if !s.filtering {
		switch e.Name {
		case "J":
			s.move(st, 1, now)
			return res
		case "K":
			s.move(st, -1, now)
			return res
		case "N":
			s.startNew(st)
			return res
		case "R":
			s.startRename(st)
			return res
		case "X":
			if s.sel != "" {
				s.mode = modeKill
			}
			return res
		case "/":
			s.filtering = true
			return res
		}
		if e.Name >= "1" && e.Name <= "9" && len(e.Name) == 1 {
			if rows := s.rows(st); int(e.Name[0]-'1') < len(rows) {
				res.show = rows[e.Name[0]-'1'].ID
				s.close()
			}
			return res
		}
	}
	if r, ok := nameChar(e); ok {
		s.filtering = true
		s.filter += string(r)
		s.fix(st, now)
	}
	return res
}
