package app

import (
	"slices"
	"strconv"
	"strings"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// nav is the window's selection and the Alt-hold switcher, kept free of Gio
// windows so it can be tested on its own. A tab is a workspace: the user
// moves between workspaces, each holding one model.Tab of panes.
type nav struct {
	session string            // the session the window shows
	lastWS  map[string]string // session -> the tab it last showed
	shown   bool              // the last sync had a visible tab in session
	closed  bool              // nothing left to show: the window closes
	// newSession is the name of a session a SessionNew is making; the
	// window switches to it once it shows up.
	newSession string
	// sessionUI asks the window to open the session switcher: "pick",
	// "new" or "rename".
	sessionUI string
	// palette asks the window to open the command palette.
	palette bool
	// task asks the window to open the New task dialog.
	task bool
	// find asks the window to open the find bar on the focused pane.
	find bool
	// prAct asks the window to run open_pr, merge_pr or rerun_checks on
	// the open tab: a browser or a dialog, which nav has no hold of.
	prAct string
	// cleanup asks the window to open the Clean up worktrees dialog.
	cleanup bool

	workspace string            // active tab (workspace) id
	tab       string            // its model.Tab, "" when it has none
	focus     map[string]string // focusKey(workspace, tab) -> focused pane
	keys      *config.Bindings  // nil: the default preset
	altHeld   bool              // the hold modifier is down on its own: the switcher shows
	pinned    bool              // the switcher stays open without the hold

	// rows are the tab ids the sidebar lists in st, top to bottom, which
	// goto_N counts; nil counts every tab (n.ordered).
	rows func(st *model.State) []string

	sidebarHidden bool // toggle_sidebar flips it; the window slides the sidebar
	panelOpen     bool // toggle_panel flips it; kept for the window's life only

	// An OpenPane is in flight: the workspace and the panes it had, so the
	// pane that shows up next gets focus.
	openingWS string
	opening   map[string]bool

	// A NewTab or NewSession is in flight: the workspaces there were, so
	// the one that shows up next is selected.
	sessions map[string]bool

	// tabMode is on between the tab prefix and the next key; renameTab asks
	// the sidebar to start an inline rename of that workspace; swallow is a
	// key whose release still belongs to tab mode.
	tabMode   bool
	renameTab string
	swallow   key.Name

	// paneMode is on from the pane prefix until Escape, Enter, the prefix
	// or a key it does not know. zoom is the pane drawn alone over its tab,
	// while it keeps focus; area is the pane area the window last drew.
	paneMode bool
	zoom     string
	area     layout.Rect

	// Tabs attached here but still detached in the state, shown until the
	// state agrees.
	attach map[string]bool

	// The previous sync's sidebar order and the shown tab's panes, to land
	// focus next to what disappeared.
	prevOrder []string
	prevKey   string
	prevPanes []string
}

func focusKey(ws, tab string) string { return ws + "\x00" + tab }

// shownTab is the tab the window draws for w: its ActiveTab, else its first.
func shownTab(w *model.Workspace) *model.Tab {
	if w == nil || len(w.Tabs) == 0 {
		return nil
	}
	for i := range w.Tabs {
		if w.Tabs[i].ID == w.ActiveTab {
			return &w.Tabs[i]
		}
	}
	return &w.Tabs[0]
}

// tabOf is the tab of w holding pane, or nil.
func tabOf(w *model.Workspace, pane string) *model.Tab {
	for i := range w.Tabs {
		if slices.Contains(panesOf(w.Tabs[i].Layout), pane) {
			return &w.Tabs[i]
		}
	}
	return nil
}

// patch applies the local overrides to st, copying its workspaces first so
// the backend's slice is never written.
func (n *nav) patch(st *model.State) {
	if len(n.attach) == 0 {
		return
	}
	st.Workspaces = slices.Clone(st.Workspaces)
	seen := map[string]bool{}
	for i := range st.Workspaces {
		w := &st.Workspaces[i]
		seen[w.ID] = true
		if n.attach[w.ID] {
			if w.Detached {
				w.Detached = false
			} else {
				delete(n.attach, w.ID)
			}
		}
	}
	for id := range n.attach {
		if !seen[id] {
			delete(n.attach, id)
		}
	}
}

// attachSession shows the detached tab ws at once, before the state
// un-detaches it.
func (n *nav) attachSession(st *model.State, ws string) {
	n.attach[ws] = true
	n.selectWorkspace(st, ws, "")
}

// setFocus focuses pane in the shown tab.
func (n *nav) setFocus(pane string) { n.focus[focusKey(n.workspace, n.tab)] = pane }

// expectSession records the workspaces before a NewTab or NewSession.
func (n *nav) expectSession(st *model.State) {
	n.sessions = map[string]bool{}
	for _, w := range st.Workspaces {
		n.sessions[w.ID] = true
	}
}

// expectPane records the active workspace's panes before an OpenPane.
func (n *nav) expectPane(st *model.State) {
	n.openingWS, n.opening = n.workspace, map[string]bool{}
	for _, p := range n.panes(st) {
		n.opening[p] = true
	}
}

func (n *nav) switcherVisible() bool { return n.altHeld || n.pinned }

// ordered is the tabs of the window's session in sidebar order
// (model.State.Ordered), without detached ones.
func (n *nav) ordered(st *model.State) []model.Workspace { return orderedIn(st, n.session) }

// orderedIn is session's tabs in sidebar order without detached ones.
func orderedIn(st *model.State, session string) []model.Workspace {
	return slices.DeleteFunc(st.Ordered(session), func(w model.Workspace) bool { return w.Detached })
}

// pickSession repairs the window's session: one that is gone hands over to
// the most recently used session with a visible tab, else the most
// recently used one; losing the last visible tab moves on the same way, or
// closes the window when no other session has a visible tab.
func (n *nav) pickSession(st *model.State) {
	if n.newSession != "" {
		if s := st.SessionNamed(n.newSession); s != nil {
			n.newSession = ""
			n.enter(s.ID)
		}
	}
	lost := n.shown && len(orderedIn(st, n.session)) == 0
	if st.Session(n.session) != nil && !lost {
		return
	}
	var best, any *model.Session
	for i := range st.Sessions {
		s := &st.Sessions[i]
		if s.ID == n.session {
			continue
		}
		if any == nil || s.UsedAt.After(any.UsedAt) {
			any = s
		}
		if len(orderedIn(st, s.ID)) > 0 && (best == nil || s.UsedAt.After(best.UsedAt)) {
			best = s
		}
	}
	switch {
	case best != nil:
		n.enter(best.ID)
	case lost || n.session != "" && any == nil:
		n.closed, n.shown = true, false
	case any != nil:
		n.enter(any.ID)
	}
}

// enter makes session the window's, on the tab it showed last.
func (n *nav) enter(session string) {
	if session == n.session {
		return
	}
	if n.lastWS == nil {
		n.lastWS = map[string]string{}
	}
	if n.session != "" {
		n.lastWS[n.session] = n.workspace
	}
	n.session, n.workspace, n.shown = session, n.lastWS[session], false
	n.tabMode, n.paneMode, n.zoom = false, false, ""
}

// switchSession shows session id, on the tab it showed last.
func (n *nav) switchSession(st *model.State, id string) {
	if st.Session(id) == nil {
		return
	}
	n.enter(id)
	n.sync(st)
}

// cycleSession moves d sessions in the switcher's order, wrapping.
func (n *nav) cycleSession(st *model.State, d int) {
	i := slices.IndexFunc(st.Sessions, func(s model.Session) bool { return s.ID == n.session })
	if i < 0 || len(st.Sessions) < 2 {
		return
	}
	n.switchSession(st, st.Sessions[((i+d)%len(st.Sessions)+len(st.Sessions))%len(st.Sessions)].ID)
}

// groupOf is w's group, or "" when it is ungrouped or its group is gone.
func groupOf(st *model.State, w model.Workspace) string {
	for _, p := range st.Projects {
		if p.ID == w.ProjectID {
			return p.ID
		}
	}
	return ""
}

func findWorkspace(st *model.State, id string) *model.Workspace {
	for i := range st.Workspaces {
		if st.Workspaces[i].ID == id {
			return &st.Workspaces[i]
		}
	}
	return nil
}

// panes are the panes of the active workspace's shown tab.
func (n *nav) panes(st *model.State) []string {
	if t := shownTab(findWorkspace(st, n.workspace)); t != nil {
		return panesOf(t.Layout)
	}
	return nil
}

// focused is the focused pane of the shown tab, or "".
func (n *nav) focused() string { return n.focus[focusKey(n.workspace, n.tab)] }

// sync applies the overrides to st and repairs the selection after a state
// change. A session that went away hands over to the next one in sidebar
// order; a pane that went away to its neighbour in the tab; a tab that went
// away to the session's active tab.
func (n *nav) sync(st *model.State) {
	if n.focus == nil {
		n.focus, n.attach = map[string]string{}, map[string]bool{}
	}
	n.patch(st)
	n.pickSession(st)
	if n.sessions != nil {
		for _, w := range n.ordered(st) {
			if !n.sessions[w.ID] {
				n.workspace, n.sessions = w.ID, nil
				break
			}
		}
	}
	order := make([]string, 0, len(st.Workspaces))
	for _, w := range n.ordered(st) {
		order = append(order, w.ID)
	}
	if w := findWorkspace(st, n.workspace); w == nil || w.Detached || w.SessionID != n.session {
		n.workspace = after(n.prevOrder, order, n.workspace)
	}
	n.prevOrder = order
	n.shown = len(order) > 0
	n.tab = ""
	if t := shownTab(findWorkspace(st, n.workspace)); t != nil {
		n.tab = t.ID
	}
	k := focusKey(n.workspace, n.tab)
	ps := n.panes(st)
	defer func() {
		n.prevKey, n.prevPanes = k, ps
		if n.zoom != n.focused() {
			n.zoom = ""
		}
	}()
	if n.opening != nil && n.openingWS == n.workspace {
		for _, p := range ps {
			if !n.opening[p] {
				n.focus[k], n.opening = p, nil
				return
			}
		}
	}
	cur := n.focus[k]
	if slices.Contains(ps, cur) {
		return
	}
	if k == n.prevKey && cur != "" {
		if p := after(n.prevPanes, ps, cur); p != "" {
			n.focus[k] = p
			return
		}
	}
	if len(ps) > 0 {
		n.focus[k] = ps[0]
	} else {
		delete(n.focus, k)
	}
}

// after picks what replaces gone: the first item after it in prev that is
// still in now, else the nearest one before it, else now's first.
func after(prev, now []string, gone string) string {
	if i := slices.Index(prev, gone); i >= 0 {
		for _, id := range prev[i+1:] {
			if slices.Contains(now, id) {
				return id
			}
		}
		for j := i - 1; j >= 0; j-- {
			if slices.Contains(now, prev[j]) {
				return prev[j]
			}
		}
	}
	if len(now) > 0 {
		return now[0]
	}
	return ""
}

// selectWorkspace activates id, switching to its session, and focuses pane
// when it is one of its panes, else the pane it last had focused.
func (n *nav) selectWorkspace(st *model.State, id, pane string) {
	if w := findWorkspace(st, id); w != nil && w.SessionID != n.session {
		n.enter(w.SessionID)
	}
	n.workspace = id
	if w := findWorkspace(st, id); w != nil && pane != "" {
		if t := tabOf(w, pane); t != nil {
			n.focus[focusKey(id, t.ID)] = pane
		}
	}
	n.sync(st)
}

// cycleWorkspace moves d tabs in sidebar order, wrapping. With a hold
// modifier and the switcher hidden it stays in the tab's group, as aide's
// Alt+J/K do; otherwise it crosses groups.
func (n *nav) cycleWorkspace(st *model.State, d int) {
	ws := n.ordered(st)
	if n.bind().Hold != 0 && !n.switcherVisible() {
		cur := findWorkspace(st, n.workspace)
		var local []model.Workspace
		for _, w := range ws {
			if cur != nil && groupOf(st, w) == groupOf(st, *cur) {
				local = append(local, w)
			}
		}
		ws = local
	}
	for i, w := range ws {
		if w.ID == n.workspace {
			n.selectWorkspace(st, ws[(i+d+len(ws))%len(ws)].ID, "")
			return
		}
	}
}

// cycleGroup moves to the first tab of the section d sections away,
// wrapping. A section is a group, or a run of ungrouped tabs between groups.
func (n *nav) cycleGroup(st *model.State, d int) {
	var firsts []string // each group's first tab, in sidebar order
	last := "\x00"
	cur := -1
	for _, w := range n.ordered(st) {
		if g := groupOf(st, w); g != last {
			last = g
			firsts = append(firsts, w.ID)
		}
		if w.ID == n.workspace {
			cur = len(firsts) - 1
		}
	}
	if len(firsts) == 0 {
		return
	}
	if cur < 0 {
		cur = 0
	}
	n.selectWorkspace(st, firsts[((cur+d)%len(firsts)+len(firsts))%len(firsts)], "")
}

// looseTab opens a tab outside every group, at the end of the session,
// with a shell where the focused pane is, or in the active tab's folder.
func (n *nav) looseTab(st *model.State) proto.NewSession {
	n.expectSession(st)
	m := proto.NewSession{SessionID: n.session, FromPane: n.focused(), Loose: true}
	if w := findWorkspace(st, n.workspace); w != nil {
		m.Cwd = st.LivePath(*w)
	}
	return m
}

// newTab opens a tab right after after, in its group, with a shell where
// after's focused pane is. With after "" it follows the active tab; with
// group set and the active tab elsewhere, it goes last in group. A group
// with no tabs, or a window with none, gets a fresh session there.
func (n *nav) newTab(st *model.State, after, group string) any {
	n.expectSession(st)
	if after == "" {
		after = n.workspace
		if w := findWorkspace(st, after); group != "" && (w == nil || groupOf(st, *w) != group) {
			after = ""
			for _, w := range n.ordered(st) {
				if groupOf(st, w) == group {
					after = w.ID
				}
			}
		}
	}
	w := findWorkspace(st, after)
	if w == nil {
		cwd := ""
		for _, p := range st.Projects {
			if p.ID == group {
				cwd = p.Root
			}
		}
		return proto.NewSession{Cwd: cwd, GroupID: group, SessionID: n.session}
	}
	from := ""
	if t := shownTab(w); t != nil && w.ID == n.workspace {
		from = n.focus[focusKey(w.ID, t.ID)]
	}
	return proto.NewTab{WorkspaceID: w.ID, FromPane: from}
}

func (n *nav) cyclePane(st *model.State, d int) {
	ps := n.panes(st)
	for i, p := range ps {
		if p == n.focused() {
			n.setFocus(ps[(i+d+len(ps))%len(ps)])
			return
		}
	}
}

// jumpAttention focuses the pane, in any session, that most recently
// started needing the user (model.NeedsYou) and is unseen, the most urgent
// by triage first (model.UrgencyRank), a finished turn after everything
// else of its urgency; the focused pane counts as seen, so pressing again
// walks on. With nothing unseen it goes to the needs-you pane of highest
// priority, then the next one on each press. With none it does nothing.
// A pane in another session switches the window to that session.
func (n *nav) jumpAttention(st *model.State) {
	shown := map[string]bool{}
	for _, w := range st.Workspaces {
		shown[w.ID] = !w.Detached
	}
	here := func(a model.Activity) bool { return a.WorkspaceID == n.workspace && a.PaneID == n.focused() }
	var unseen, seen []model.Activity
	for _, a := range st.Activities {
		switch {
		case !model.NeedsYou(a.State) || !shown[a.WorkspaceID]:
		case a.Unseen && !here(a):
			unseen = append(unseen, a)
		default:
			seen = append(seen, a)
		}
	}
	var to model.Activity
	switch {
	case len(unseen) > 0:
		slices.SortStableFunc(unseen, func(a, b model.Activity) int {
			if ra, rb := model.UrgencyRank(a), model.UrgencyRank(b); ra != rb {
				return rb - ra
			}
			if ca, cb := a.State == model.StateCompleted, b.State == model.StateCompleted; ca != cb {
				if ca {
					return 1
				}
				return -1
			}
			return b.UpdatedAt.Compare(a.UpdatedAt)
		})
		to = unseen[0]
	case len(seen) > 0:
		model.SortActivities(seen)
		to = seen[(slices.IndexFunc(seen, here)+1)%len(seen)]
	default:
		return
	}
	n.selectWorkspace(st, to.WorkspaceID, to.PaneID)
}

// bind is the active bindings; a zero nav uses the default preset.
func (n *nav) bind() *config.Bindings {
	if n.keys == nil {
		return config.Preset(config.DefaultPreset)
	}
	return n.keys
}

// keyFilters are the filters the window polls before any pane sees input,
// so bound chords and the hold modifier never reach a PTY. Copy, paste and
// scrolling are the terminal view's own.
func (n *nav) keyFilters() []key.Filter {
	b := n.bind()
	all := key.ModAlt | key.ModShift | key.ModCtrl | key.ModSuper | key.ModCommand
	var fs []key.Filter
	if hk := b.HoldKey(); hk != "" {
		fs = append(fs, key.Filter{Name: hk, Optional: all})
	}
	// The goto_tab modifiers on their own show the sidebar's digits.
	mods, _ := gotoKeys(b)
	for _, k := range modKeys {
		if mods&k.mod != 0 {
			fs = append(fs, key.Filter{Name: k.name, Optional: all})
		}
	}
	for _, c := range b.WindowChords() {
		fs = append(fs, key.Filter{Name: c.Name, Required: c.Mods})
	}
	if n.switcherVisible() {
		fs = append(fs, key.Filter{Name: key.NameEscape})
		if n.modalSwitcher() {
			for _, k := range switcherKeys {
				fs = append(fs, key.Filter{Name: k})
			}
		}
	}
	if n.tabMode || n.paneMode {
		// Tab mode takes every key; Tab is a system key and needs its name.
		fs = append(fs, key.Filter{Optional: all}, key.Filter{Name: key.NameTab, Optional: all})
	}
	if n.swallow != "" {
		fs = append(fs, key.Filter{Name: n.swallow, Optional: all})
	}
	return fs
}

// switcherKeys move through an open switcher that has no hold modifier, and
// Enter closes it.
var switcherKeys = []key.Name{"J", "K", key.NameDownArrow, key.NameUpArrow, key.NameReturn, key.NameEnter}

// modalSwitcher reports whether the switcher is open without a hold
// modifier, so it takes plain keys until Enter, Escape or a click.
func (n *nav) modalSwitcher() bool { return n.pinned && n.bind().Hold == 0 }

func modifierKey(k key.Name) bool {
	switch k {
	case key.NameCtrl, key.NameShift, key.NameAlt, key.NameSuper, key.NameCommand:
		return true
	}
	return false
}

// tabKey runs one tab-mode key from [keys.tab]: new, close, rename,
// previous/next, go to 1-9. Shift does not matter unless a chord names it.
// Anything else only leaves the mode.
func (n *nav) tabKey(st *model.State, e key.Event) any {
	if e.Modifiers&^key.ModShift != 0 {
		return nil
	}
	b := n.bind()
	a := b.TabAction(e)
	if a == "" && e.Modifiers != 0 {
		a = b.TabAction(key.Event{Name: e.Name})
	}
	return n.tabOp(st, a)
}

// paneKey runs one pane-mode key from [keys.pane] and reports whether it
// was one; anything else ends the mode. Shift does not matter unless a
// chord names it.
func (n *nav) paneKey(st *model.State, e key.Event) (any, bool) {
	if e.Modifiers&^key.ModShift != 0 {
		return nil, false
	}
	b := n.bind()
	a := b.PaneModeAction(e)
	if a == "" && e.Modifiers != 0 {
		a = b.PaneModeAction(key.Event{Name: e.Name})
	}
	if a == "" {
		return nil, false
	}
	return n.paneOp(st, a), true
}

// paneOp runs a [keys.pane] action.
func (n *nav) paneOp(st *model.State, a string) any {
	root := n.root(st)
	area := n.area
	if area.W <= 0 || area.H <= 0 {
		area = layout.Rect{W: 1600, H: 900}
	}
	switch a {
	case "new", "split_down", "split_right":
		if n.workspace == "" {
			return nil
		}
		dir := layout.Horizontal
		if r, ok := rectsOf(root, area, 0)[n.focused()]; a == "split_down" || a == "new" && ok && r.H > r.W {
			dir = layout.Vertical
		}
		n.expectPane(st)
		return proto.OpenPane{WorkspaceID: n.workspace, TabID: n.tab, Target: n.focused(), Dir: dir}
	case "close":
		if n.focused() != "" {
			return proto.ClosePane{Pane: n.focused()}
		}
	case "focus_left", "focus_right", "focus_up", "focus_down":
		d := map[string]layout.Direction{"focus_left": layout.Left, "focus_right": layout.Right, "focus_up": layout.Up, "focus_down": layout.Down}[a]
		if root != nil {
			if p, ok := layout.Neighbor(root, area, n.focused(), d); ok {
				n.setFocus(p)
				n.zoom = ""
			}
		}
	case "fullscreen":
		if n.zoom == "" && len(panesOf(root)) > 1 {
			n.zoom = n.focused()
		} else {
			n.zoom = ""
		}
	case "next":
		n.cyclePane(st, 1)
		n.zoom = ""
	}
	return nil
}

// root is the shown tab's split tree, or nil.
func (n *nav) root(st *model.State) *layout.Node {
	if t := shownTab(findWorkspace(st, n.workspace)); t != nil {
		return t.Layout
	}
	return nil
}

// zoomed is the pane drawn alone over its tab, or "".
func (n *nav) zoomed() string {
	if n.zoom != "" && n.zoom == n.focused() {
		return n.zoom
	}
	return ""
}

// tabOp runs a tab action: new, close, rename, prev, next, goto_N. Tabs are
// the sidebar's rows; goto_N counts the rows it shows, skipping collapsed
// groups.
func (n *nav) tabOp(st *model.State, op string) any {
	switch op {
	case "rename":
		n.renameTab = n.workspace
	case "new":
		return n.looseTab(st)
	case "new_in_group":
		return n.newTab(st, "", "")
	case "close":
		if findWorkspace(st, n.workspace) != nil {
			return proto.CloseTab{WorkspaceID: n.workspace}
		}
	case "prev":
		n.cycleWorkspace(st, -1)
	case "next":
		n.cycleWorkspace(st, 1)
	case "attention":
		n.jumpAttention(st)
	case "sessions":
		n.sessionUI = "pick"
	}
	if d, ok := strings.CutPrefix(op, "goto_"); ok {
		var ids []string
		if n.rows != nil {
			ids = n.rows(st)
		} else {
			for _, w := range n.ordered(st) {
				ids = append(ids, w.ID)
			}
		}
		if j, err := strconv.Atoi(d); err == nil && j >= 1 && j <= len(ids) {
			n.selectWorkspace(st, ids[j-1], "")
		}
	}
	return nil
}

// ctrlByte is the control character a Ctrl+letter chord types, if any.
func ctrlByte(e key.Event) ([]byte, bool) {
	if e.Modifiers&^key.ModShift != key.ModCtrl || len(e.Name) != 1 || e.Name[0] < '@' || e.Name[0] > '_' {
		return nil, false
	}
	return []byte{e.Name[0] & 0x1f}, true
}

// key applies one key event and returns a proto message to send, if any.
func (n *nav) key(st *model.State, e key.Event) any {
	b := n.bind()
	if e.Name == n.swallow && !n.tabMode && !n.paneMode && e.Modifiers&key.ModAlt == 0 {
		if e.State == key.Release {
			n.swallow = ""
		}
		return nil // the rest of a key tab mode used
	}
	if b.Is("tab_prefix", e) {
		if e.State != key.Press {
			return nil
		}
		if !n.tabMode {
			n.tabMode, n.paneMode = true, false
			return nil
		}
		n.tabMode = false
		if p, ok := ctrlByte(e); ok && n.focused() != "" {
			return proto.Input{Pane: n.focused(), Data: p}
		}
		return nil
	}
	if b.Is("pane_prefix", e) {
		if e.State != key.Press {
			return nil
		}
		if !n.paneMode {
			n.paneMode, n.tabMode = true, false
			return nil
		}
		n.paneMode = false
		if p, ok := ctrlByte(e); ok && n.focused() != "" {
			return proto.Input{Pane: n.focused(), Data: p}
		}
		return nil
	}
	hold := b.HoldKey()
	if n.paneMode && (hold == "" || e.Name != hold) {
		if e.State != key.Press || modifierKey(e.Name) {
			return nil
		}
		if e.Modifiers&key.ModAlt == 0 && b.Action(e) == "" {
			n.swallow = e.Name
			msg, ok := n.paneKey(st, e)
			n.paneMode = ok
			return msg
		}
		n.paneMode = false // as in tab mode, a bound chord keeps its meaning
	}
	if n.tabMode && (hold == "" || e.Name != hold) {
		if e.State != key.Press || modifierKey(e.Name) {
			return nil
		}
		n.tabMode = false
		if e.Modifiers&key.ModAlt == 0 && b.Action(e) == "" {
			n.swallow = e.Name
			return n.tabKey(st, e)
		}
		// A bound chord leaves the mode and keeps its usual meaning.
	}
	if hold != "" && e.Name == hold {
		// Like aide, the hold modifier with another one held is not a hold.
		n.altHeld = e.State == key.Press && e.Modifiers&^b.Hold == 0
		return nil
	}
	if e.State != key.Press {
		return nil
	}
	if e.Name == key.NameEscape && n.switcherVisible() {
		n.altHeld, n.pinned = false, false
		return nil
	}
	if n.modalSwitcher() && e.Modifiers == 0 {
		switch e.Name {
		case "J", key.NameDownArrow:
			n.cycleWorkspace(st, 1)
			return nil
		case "K", key.NameUpArrow:
			n.cycleWorkspace(st, -1)
			return nil
		case key.NameReturn, key.NameEnter:
			n.pinned = false
			return nil
		}
	}
	return n.globalOp(st, b.Action(e))
}

// globalOp runs a [keys] action the window owns. The prefixes enter their
// mode, as the command palette runs them; keys handle them in key.
func (n *nav) globalOp(st *model.State, act string) any {
	ws := n.workspace
	switch act {
	case "tab_prefix":
		n.tabMode, n.paneMode = true, false
	case "pane_prefix":
		n.paneMode, n.tabMode = true, false
	case "new_task":
		n.task = true
	case "command_palette":
		n.palette = true
	case "next_group":
		n.cycleGroup(st, 1)
	case "prev_group":
		n.cycleGroup(st, -1)
	case "prev_pane":
		n.cyclePane(st, -1)
	case "next_pane":
		n.cyclePane(st, 1)
	case "pin_switcher":
		n.pinned = !n.pinned
	case "toggle_sidebar":
		n.sidebarHidden = !n.sidebarHidden
	case "toggle_panel":
		n.panelOpen = !n.panelOpen
	case "find":
		n.find = n.focused() != ""
	case "cleanup_worktrees":
		n.cleanup = true
	case "jump_attention":
		n.jumpAttention(st)
	case "allow_prompt", "deny_prompt":
		return n.answer(st, act == "allow_prompt")
	case "session_switcher":
		n.sessionUI = "pick"
	case "session_new":
		n.sessionUI = "new"
	case "session_rename":
		n.sessionUI = "rename"
	case "session_next":
		n.cycleSession(st, 1)
	case "session_prev":
		n.cycleSession(st, -1)
	case "switcher":
		if n.switcherVisible() {
			n.altHeld, n.pinned = false, false
		} else {
			n.pinned = true
		}
	case "split_right", "split_down":
		if ws == "" {
			return nil
		}
		dir := layout.Horizontal
		if act == "split_down" {
			dir = layout.Vertical
		}
		n.expectPane(st)
		return proto.OpenPane{WorkspaceID: ws, TabID: n.tab, Target: n.focused(), Dir: dir}
	case "close_pane":
		if n.focused() != "" {
			return proto.ClosePane{Pane: n.focused()}
		}
	case "new_tab":
		return n.tabOp(st, "new_in_group")
	case "view_diff", "create_pr":
		return n.review(st, ws, act)
	case "open_pr", "merge_pr", "rerun_checks":
		n.prAct = act
	case "close_tab", "next_tab", "prev_tab":
		return n.tabOp(st, strings.TrimSuffix(act, "_tab"))
	}
	if d, ok := strings.CutPrefix(act, "goto_tab_"); ok {
		return n.tabOp(st, "goto_"+d)
	}
	return nil
}
