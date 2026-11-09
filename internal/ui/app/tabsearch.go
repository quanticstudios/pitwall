package app

import (
	"cmp"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// tabHit is one row of tab search: a tab of any session, a closed tab, or
// the last row, which hands the query to Search all sessions.
type tabHit struct {
	ws     *model.Workspace
	closed *model.ClosedTab
	all    bool
}

// tabRows is what tab search lists for query in st: the tabs, those of
// session here first, then the closed tabs, each best match first, and with
// all set a last row for Search all sessions. With no query, the most
// recently used come first.
// Each word of query must match, as rank's do, the tab's title or the rest
// of what tabText gives, or be one typo away from a word of it.
func tabRows(st *model.State, here, query string, all bool) []tabHit {
	words := strings.Fields(strings.ToLower(query))
	type scored struct {
		h    tabHit
		n    int
		at   time.Time
		live bool
		here bool
	}
	var out []scored
	add := func(h tabHit, title, rest string, at time.Time) {
		if n := tabScore(words, title, rest); n >= 0 {
			out = append(out, scored{h, n, at, h.ws != nil, h.ws != nil && h.ws.SessionID == here})
		}
	}
	for i := range st.Workspaces {
		w := &st.Workspaces[i]
		title, rest := tabText(st, *w)
		add(tabHit{ws: w}, title, rest, tabUsed(st, *w))
	}
	for i := range st.Closed {
		c := &st.Closed[i]
		title, rest := closedText(*c)
		add(tabHit{closed: c}, title, rest, c.ClosedAt)
	}
	slices.SortStableFunc(out, func(a, b scored) int {
		if a.live != b.live {
			if a.live {
				return -1
			}
			return 1
		}
		if a.here != b.here {
			if a.here {
				return -1
			}
			return 1
		}
		return cmp.Or(b.n-a.n, b.at.Compare(a.at))
	})
	hits := make([]tabHit, 0, len(out)+1)
	for _, s := range out {
		hits = append(hits, s.h)
	}
	if all && len(words) > 0 {
		hits = append(hits, tabHit{all: true})
	}
	return hits
}

// tabScore is rank's score of words against a tab's title and the rest of
// its text, -1 when a word matches neither. A word fuzzy does not match
// scores 1 when it is one typo from a word of either.
func tabScore(words []string, title, rest string) int {
	total := 0
	tw := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool { return r > 0x7f || !wordChar(byte(r)) })
	for _, w := range words {
		tn, rn := fuzzy(w, title), fuzzy(w, rest)
		switch {
		case tn >= 0 || rn >= 0:
			total += max(tn, 0) + max(rn, 0)
		case typo(w, title+" "+rest):
			total++
		default:
			return -1
		}
		if slices.Contains(tw, w) {
			total += 10
		}
	}
	return total
}

// typo reports whether q, of four characters or more, is one edit from a
// word of s: a character added, dropped, changed, or two swapped.
func typo(q, s string) bool {
	if len(q) < 4 {
		return false
	}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r > 0x7f || !wordChar(byte(r)) }) {
		if oneEdit(q, w) {
			return true
		}
	}
	return false
}

// oneEdit reports whether a and b differ by at most one insertion,
// deletion, substitution or swap of neighbours.
func oneEdit(a, b string) bool {
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > 1 {
		return false
	}
	i := 0
	for i < len(a) && a[i] == b[i] {
		i++
	}
	if i == len(a) {
		return true
	}
	if len(a) == len(b) {
		return a[i+1:] == b[i+1:] || // changed
			i+1 < len(a) && a[i] == b[i+1] && a[i+1] == b[i] && a[i+2:] == b[i+2:] // swapped
	}
	return a[i:] == b[i+1:] // added
}

// tabText is what tab search matches of tab w: its title, and its name,
// branch, group, repo and folder, agent, pull request and first prompts.
func tabText(st *model.State, w model.Workspace) (title, rest string) {
	parts := []string{w.Name, w.Branch, groupName(st, w), sidebar.AgentName(sidebar.AgentOf(st, w))}
	parts = append(parts, folderWords(w)...)
	if pr, ok := st.PRs[w.ID]; ok && pr.Number > 0 {
		parts = append(parts, "#"+strconv.Itoa(pr.Number))
	}
	for _, c := range st.Chats {
		if c.ID == w.Chat {
			parts = append(parts, c.Title, c.RecentPrompt)
		}
	}
	for _, p := range st.Panes {
		if p.WorkspaceID == w.ID {
			parts = append(parts, p.Prompt)
		}
	}
	return tabTitle(w), strings.Join(parts, " ")
}

// closedText is tabText for closed tab c.
func closedText(c model.ClosedTab) (title, rest string) {
	parts := []string{c.Tab.Name, c.Tab.Branch, c.Group.Name, c.Pane.Prompt}
	if model.IsAgent(c.Pane.Provider) {
		parts = append(parts, sidebar.AgentName(c.Pane.Provider))
	}
	parts = append(parts, folderWords(c.Tab)...)
	if c.PR > 0 {
		parts = append(parts, "#"+strconv.Itoa(c.PR))
	}
	return tabTitle(c.Tab), strings.Join(parts, " ")
}

// folderWords are the base names of w's repo and folder.
func folderWords(w model.Workspace) []string {
	var out []string
	for _, d := range []string{w.RepoRoot, w.WorktreeRoot, w.Path} {
		if d != "" {
			out = append(out, filepath.Base(d))
		}
	}
	return out
}

// tabUsed is when tab w was last busy: its own time or its agents' latest.
func tabUsed(st *model.State, w model.Workspace) time.Time {
	at := w.UpdatedAt
	for _, a := range st.Activities {
		if a.WorkspaceID == w.ID && a.UpdatedAt.After(at) {
			at = a.UpdatedAt
		}
	}
	return at
}

// tabSearchRows is what the open tab search lists now.
func (u *ui) tabSearchRows(st *model.State) []tabHit {
	if u.pal.peek {
		return u.peekable(tabRows(st, u.nav.session, u.pal.query, false))
	}
	return tabRows(st, u.nav.session, u.pal.query, u.canSearchAll())
}

// peekable is hits without the rows peek_tab cannot peek at: closed tabs
// and the shown one.
func (u *ui) peekable(hits []tabHit) []tabHit {
	return slices.DeleteFunc(hits, func(h tabHit) bool { return h.ws == nil || h.ws.ID == u.nav.workspace })
}

// tabSearchKey runs one key in the open tab search. Its own shortcut and
// Escape close it, Enter picks the highlighted row, Delete forgets a closed
// one, and the rest edit and move as in the command palette.
func (u *ui) tabSearchKey(st *model.State, e key.Event) {
	p := &u.pal
	if e.State != key.Press {
		return
	}
	rows := u.tabSearchRows(st)
	switch {
	case e.Name == key.NameEscape || u.nav.bind().Action(e) == "tab_search":
		p.close()
	case e.Name == key.NameReturn || e.Name == key.NameEnter:
		if p.sel < len(rows) {
			u.pickTab(st, rows[p.sel])
		}
	case e.Name == key.NameDeleteForward:
		if p.sel < len(rows) && rows[p.sel].closed != nil {
			u.send(proto.ForgetTab{ID: rows[p.sel].closed.ID})
		}
	default:
		p.key(make([]paletteEntry, len(rows)), e)
	}
	if !p.open {
		u.nav.swallow = e.Name
	}
}

// pickTab runs tab search's row h: it shows a tab, switching session and
// host as needed, reopens a closed one, in its repository when its folder is
// gone, or hands the query to Search all sessions.
func (u *ui) pickTab(st *model.State, h tabHit) {
	p := &u.pal
	switch {
	case p.peek && h.ws != nil:
		u.openPeek(st, h.ws.ID, "")
	case h.all:
		p.tabs, p.search, p.sel = false, true, 0
		u.all.preview, u.all.sent = nil, ""
		u.updateSearchAll()
		return
	case h.ws != nil:
		if h.ws.Detached {
			u.send(proto.DetachSession{WorkspaceID: h.ws.ID, Detached: false})
			u.nav.attach[h.ws.ID] = true
		}
		u.nav.selectWorkspace(st, h.ws.ID, "")
	case h.closed != nil:
		c := *h.closed // the send may change the slice it points into
		if c.Gone && c.Root() == "" {
			return // nowhere to open it; the row says so
		}
		u.send(proto.ReopenTab{ID: c.ID, AtRoot: c.Gone})
		u.nav.reopen = c.ID
	}
	p.close()
}
