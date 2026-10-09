package app

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
)

// palette is the command palette's state, kept free of Gio windows so its
// keys can be tested on their own. It lists every action with the keys
// bound to it now, so it is the cheatsheet too. The drawing is in
// palettedraw.go.
type palette struct {
	open     bool
	query    string
	sel      int // the highlighted row of rank's result
	openedAt time.Time
	height   anim.Value // the card's height, easing to fit the rows
	list     listScroll
	draw     paletteDraw
}

// paletteEntry is one row: an action and the keys that run it.
type paletteEntry struct {
	action config.Action
	keys   []string       // "Ctrl+Shift+T", or "Ctrl+T N" for a tab-mode key
	chords []config.Chord // the chords of keys, a mode's without its prefix
}

func (p *palette) openAt(now time.Time) {
	if !p.open {
		p.openedAt, p.list.selY, p.height = now, -1, anim.Value{}
	}
	p.open, p.query, p.sel = true, "", 0
}

func (p *palette) close() { p.open = false }

// paletteEntries is every action, grouped in the order the groups first
// appear in the config, with the chords b binds to it. A tab- or pane-mode
// key follows its prefix, and has no keys while the prefix is unbound.
func paletteEntries(b *config.Bindings) []paletteEntry {
	var out []paletteEntry
	var groups []string
	for _, a := range config.Actions() {
		cs, prefix := b.Global[a.Name], ""
		switch {
		case a.Tab:
			cs, prefix = b.Tab[a.Name], firstChord(b.Global["tab_prefix"])
		case a.Pane:
			cs, prefix = b.Pane[a.Name], firstChord(b.Global["pane_prefix"])
		}
		var keys []string
		var chords []config.Chord
		for _, c := range cs {
			switch {
			case !a.Tab && !a.Pane:
				keys = append(keys, c.String())
			case prefix != "":
				keys = append(keys, prefix+" "+c.String())
			default:
				continue
			}
			chords = append(chords, c)
		}
		if !slices.Contains(groups, a.Group) {
			groups = append(groups, a.Group)
		}
		out = append(out, paletteEntry{a, keys, chords})
	}
	slices.SortStableFunc(out, func(x, y paletteEntry) int {
		return slices.Index(groups, x.action.Group) - slices.Index(groups, y.action.Group)
	})
	return out
}

// rank is the entries query matches, best first. Each word of the query
// must match, as fuzzy does, the entry's title or its group, config name
// and keys, and scores both matches, so "sidebar" puts toggle_sidebar
// above "Next tab in sidebar order". A word that is a whole word of the
// title scores 10 more, so "pane" puts "Next pane" above "agent panel".
// Ties keep their order. Keys such as "ctrl+shift+r" (see
// config.KeyQuery) find the actions bound to that chord instead.
func rank(entries []paletteEntry, query string) []paletteEntry {
	if c, ok := config.KeyQuery(query); ok {
		return slices.DeleteFunc(slices.Clone(entries), func(e paletteEntry) bool { return !slices.Contains(e.chords, c) })
	}
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return entries
	}
	type scored struct {
		e paletteEntry
		n int
	}
	var out []scored
	for _, e := range entries {
		title := e.action.Title()
		tw := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return r > 0x7f || !wordChar(byte(r)) })
		rest := e.action.Group + " " + e.action.Name + " " + strings.Join(e.keys, " ")
		total := 0
		for _, w := range words {
			tn, rn := fuzzy(w, title), fuzzy(w, rest)
			if tn < 0 && rn < 0 {
				total = -1
				break
			}
			total += max(tn, 0) + max(rn, 0)
			if slices.Contains(tw, w) {
				total += 10
			}
		}
		if total >= 0 {
			out = append(out, scored{e, total})
		}
	}
	slices.SortStableFunc(out, func(a, b scored) int { return b.n - a.n })
	es := make([]paletteEntry, len(out))
	for i, s := range out {
		es[i] = s.e
	}
	return es
}

// fuzzy scores q against s, ignoring case: -1 unless q's characters appear
// in s in order. As in fzf, each matched character scores 1, 8 more at the
// start of a word and 6 more right after the previous match; a gap between
// matches costs 3, and 1 more for each character past the first. The best
// alignment counts, so "tab" in "next tab" scores its word, not "nexT tAB".
func fuzzy(q, s string) int {
	if q == "" {
		return 0
	}
	q, s = strings.ToLower(q), strings.ToLower(s)
	const none = -1 << 30
	// prev[i] is the best score of q[:j] with q[j-1] matched at s[i].
	prev := make([]int, len(s))
	cur := make([]int, len(s))
	for j := range len(q) {
		// gap is the best prev[k]+k for k < i-1: a match at i after a gap
		// of i-k-1 characters scores prev[k]-(i-k+1), which is gap-i-1.
		gap := none
		for i := range len(s) {
			if j > 0 && i >= 2 && prev[i-2] != none {
				gap = max(gap, prev[i-2]+i-2)
			}
			cur[i] = none
			if s[i] != q[j] {
				continue
			}
			pts := 1
			if i == 0 || !wordChar(s[i-1]) {
				pts += 8
			}
			if j == 0 {
				cur[i] = pts
				continue
			}
			c := none
			if i > 0 && prev[i-1] != none {
				c = prev[i-1] + 6
			}
			if gap != none {
				c = max(c, gap-i-1)
			}
			if c != none {
				cur[i] = c + pts
			}
		}
		prev, cur = cur, prev
	}
	out := -1
	for _, n := range prev {
		out = max(out, n)
	}
	return out
}

func wordChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

// key applies one key press to the open palette: Escape closes it, the
// arrows, Tab and Ctrl+J/K move, typing filters, and Enter picks the
// highlighted row.
func (p *palette) key(rows []paletteEntry, e key.Event) *paletteEntry {
	if e.State != key.Press || modifierKey(e.Name) {
		return nil
	}
	move := func(d int) { p.sel = min(max(p.sel+d, 0), max(len(rows)-1, 0)) }
	switch {
	case e.Name == key.NameEscape:
		p.close()
	case e.Name == key.NameReturn || e.Name == key.NameEnter:
		return p.pick(rows, p.sel)
	case e.Name == key.NameUpArrow, e.Name == "K" && e.Modifiers == key.ModCtrl,
		e.Name == key.NameTab && e.Modifiers == key.ModShift:
		move(-1)
	case e.Name == key.NameDownArrow, e.Name == "J" && e.Modifiers == key.ModCtrl, e.Name == key.NameTab:
		move(1)
	case e.Name == key.NamePageUp:
		move(-10)
	case e.Name == key.NamePageDown:
		move(10)
	case e.Name == key.NameDeleteBackward:
		if _, size := utf8.DecodeLastRuneInString(p.query); size > 0 {
			p.query, p.sel = p.query[:len(p.query)-size], 0
		}
	case e.Modifiers&^key.ModShift == 0 && (e.Name == key.NameSpace || len(e.Name) == 1 && e.Name[0] > ' ' && e.Name[0] <= '~'):
		c := strings.ToLower(string(e.Name))
		if e.Name == key.NameSpace {
			c = " "
		}
		if len(p.query) < 64 {
			p.query, p.sel = p.query+c, 0
		}
	}
	return nil
}

// pick is row i run: it closes the palette and returns the row. "Go to
// tab 1–9" asks for the digit instead, and the palette stays open.
func (p *palette) pick(rows []paletteEntry, i int) *paletteEntry {
	if i < 0 || i >= len(rows) {
		return nil
	}
	if rows[i].action.Name == gotoAny {
		p.query, p.sel = "go to tab ", 0
		return nil
	}
	p.close()
	return &rows[i]
}

// paletteKey runs one key in the open palette. Its own shortcut closes it.
func (u *ui) paletteKey(st *model.State, e key.Event) {
	b := u.nav.bind()
	if b.Action(e) == "command_palette" {
		if e.State == key.Press {
			u.pal.close()
		}
	} else if r := u.pal.key(paletteRows(b, u.pal.query, u.gui.Recent), e); r != nil {
		u.runEntry(st, *r)
	}
	if !u.pal.open {
		u.nav.swallow = e.Name // its release must not reach the pane
	}
}

// runEntry runs the palette's row e and puts it first among the recent
// ones.
func (u *ui) runEntry(st *model.State, e paletteEntry) {
	if r := pushRecent(u.gui.Recent, e.id()); !slices.Equal(r, u.gui.Recent) {
		u.gui.Recent = r
		if u.report != nil { // a real window, not a test
			saveGUIState(u.gui)
		}
	}
	u.runAction(st, e.action)
}

// runAction runs a as its key would; the palette's Enter and click come
// here. Copy, paste and scrolling go to the focused pane's view.
func (u *ui) runAction(st *model.State, a config.Action) {
	var msg any
	switch {
	case a.Tab:
		msg = u.nav.tabOp(st, a.Name)
	case a.Pane:
		msg = u.nav.paneOp(st, a.Name)
	case config.PaneAction(a.Name):
		if p := u.panes[u.nav.focused()]; p != nil {
			p.view.Run(a.Name)
		}
	case a.Name == "open_settings":
		u.toggleSettings()
	default:
		msg = u.nav.globalOp(st, a.Name)
	}
	if msg != nil {
		u.send(msg)
	}
}
