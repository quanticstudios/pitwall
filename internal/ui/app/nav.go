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
// windows so it can be tested on its own.
type nav struct {
	workspace string            // active workspace id
	tab       string            // the tab shown for it, "" when it has none
	focus     map[string]string // focusKey(workspace, tab) -> focused pane
	keys      *config.Bindings  // nil: the default preset
	altHeld   bool              // the hold modifier is down on its own: the switcher shows
	pinned    bool              // the switcher stays open without the hold

	sidebarHidden bool // toggle_sidebar flips it; the window slides the sidebar

	// An OpenPane is in flight: the workspace and the panes it had, so the
	// pane that shows up next gets focus.
	openingWS string
	opening   map[string]bool

	// A NewSession is in flight: the sessions there were, so the one that
	// shows up next is selected.
	sessions map[string]bool

	// tabMode is on between the tab prefix and the next key; renameTab asks
	// the tab strip to start an inline rename; swallow is a key whose
	// release still belongs to tab mode.
	tabMode   bool
	renameTab string
	swallow   key.Name

	// Local overrides applied to every state until it agrees: the tab the
	// window shows per session, and sessions attached here but still
	// detached in the state.
	pick   map[string]string
	attach map[string]bool
	out    []any // messages nav wants sent outside key handling

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
	if len(n.pick) == 0 && len(n.attach) == 0 {
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
		if t, ok := n.pick[w.ID]; ok {
			if w.ActiveTab == t || !slices.ContainsFunc(w.Tabs, func(x model.Tab) bool { return x.ID == t }) {
				delete(n.pick, w.ID)
			} else {
				w.ActiveTab = t
			}
		}
	}
	for id := range n.pick {
		if !seen[id] {
			delete(n.pick, id)
		}
	}
	for id := range n.attach {
		if !seen[id] {
			delete(n.attach, id)
		}
	}
}

// selectTab shows tab in the active session and tells the daemon, so an
// attach opens on it.
func (n *nav) selectTab(st *model.State, tab string) any {
	if n.workspace == "" || tab == "" {
		return nil
	}
	n.pick[n.workspace] = tab
	n.sync(st)
	return proto.SelectTab{WorkspaceID: n.workspace, TabID: tab}
}

// attachSession shows ws (and tab) at once, before the state un-detaches it.
func (n *nav) attachSession(st *model.State, ws, tab string) {
	n.attach[ws] = true
	if tab != "" {
		n.pick[ws] = tab
		n.out = append(n.out, proto.SelectTab{WorkspaceID: ws, TabID: tab})
	}
	n.selectWorkspace(st, ws, "")
}

// setFocus focuses pane in the shown tab.
func (n *nav) setFocus(pane string) { n.focus[focusKey(n.workspace, n.tab)] = pane }

// expectSession records the sessions before a NewSession.
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

// ordered is the sidebar's order: ungrouped sessions (including those
// whose group is gone), then group order, each group's in session order.
// Detached sessions are skipped.
func ordered(st *model.State) []model.Workspace {
	var out []model.Workspace
	for _, g := range append([]string{""}, projectIDs(st)...) {
		for _, w := range st.Workspaces {
			if groupOf(st, w) == g && !w.Detached {
				out = append(out, w)
			}
		}
	}
	return out
}

func projectIDs(st *model.State) []string {
	ids := make([]string, len(st.Projects))
	for i, p := range st.Projects {
		ids[i] = p.ID
	}
	return ids
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
		n.focus, n.pick, n.attach = map[string]string{}, map[string]string{}, map[string]bool{}
	}
	n.patch(st)
	if n.sessions != nil {
		for _, w := range ordered(st) {
			if !n.sessions[w.ID] {
				n.workspace, n.sessions = w.ID, nil
				break
			}
		}
	}
	order := make([]string, 0, len(st.Workspaces))
	for _, w := range ordered(st) {
		order = append(order, w.ID)
	}
	if w := findWorkspace(st, n.workspace); w == nil || w.Detached {
		n.workspace = after(n.prevOrder, order, n.workspace)
	}
	n.prevOrder = order
	n.tab = ""
	if t := shownTab(findWorkspace(st, n.workspace)); t != nil {
		n.tab = t.ID
	}
	k := focusKey(n.workspace, n.tab)
	ps := n.panes(st)
	defer func() { n.prevKey, n.prevPanes = k, ps }()
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

// selectWorkspace activates id and focuses pane when it is one of its panes,
// else the pane it last had focused.
// A pane in another tab brings its tab up.
func (n *nav) selectWorkspace(st *model.State, id, pane string) {
	n.workspace = id
	if w := findWorkspace(st, id); w != nil && pane != "" {
		if t := tabOf(w, pane); t != nil {
			if t != shownTab(w) {
				n.pick[id] = t.ID
				n.out = append(n.out, proto.SelectTab{WorkspaceID: id, TabID: t.ID})
			}
			n.focus[focusKey(id, t.ID)] = pane
		}
	}
	n.sync(st)
}

func (n *nav) cycleWorkspace(st *model.State, d int) {
	ws := ordered(st)
	if !n.switcherVisible() {
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

func (n *nav) cyclePane(st *model.State, d int) {
	ps := n.panes(st)
	for i, p := range ps {
		if p == n.focused() {
			n.setFocus(ps[(i+d+len(ps))%len(ps)])
			return
		}
	}
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
	if n.tabMode {
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
	if a == "rename" {
		n.renameTab = n.tab
		return nil
	}
	return n.tabOp(st, a)
}

// tabOp runs a tab action: new, close, prev, next, goto_N.
func (n *nav) tabOp(st *model.State, op string) any {
	w := findWorkspace(st, n.workspace)
	if w == nil {
		return nil
	}
	i := slices.IndexFunc(w.Tabs, func(t model.Tab) bool { return t.ID == n.tab })
	step := func(d int) any {
		if len(w.Tabs) < 2 || i < 0 {
			return nil
		}
		return n.selectTab(st, w.Tabs[(i+d+len(w.Tabs))%len(w.Tabs)].ID)
	}
	switch op {
	case "new":
		delete(n.pick, w.ID) // the daemon makes the new tab active
		return proto.NewTab{WorkspaceID: w.ID, FromPane: n.focused()}
	case "close":
		if n.tab != "" {
			return proto.CloseTab{WorkspaceID: w.ID, TabID: n.tab}
		}
	case "prev":
		return step(-1)
	case "next":
		return step(1)
	}
	if d, ok := strings.CutPrefix(op, "goto_"); ok {
		if j, err := strconv.Atoi(d); err == nil && j >= 1 && j <= len(w.Tabs) {
			return n.selectTab(st, w.Tabs[j-1].ID)
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
	if e.Name == n.swallow && !n.tabMode && e.Modifiers&key.ModAlt == 0 {
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
			n.tabMode = true
			return nil
		}
		n.tabMode = false
		if p, ok := ctrlByte(e); ok && n.focused() != "" {
			return proto.Input{Pane: n.focused(), Data: p}
		}
		return nil
	}
	hold := b.HoldKey()
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
	ws := n.workspace
	act := b.Action(e)
	switch act {
	case "next_session":
		n.cycleWorkspace(st, 1)
	case "prev_session":
		n.cycleWorkspace(st, -1)
	case "prev_pane":
		n.cyclePane(st, -1)
	case "next_pane":
		n.cyclePane(st, 1)
	case "pin_switcher":
		n.pinned = !n.pinned
	case "toggle_sidebar":
		n.sidebarHidden = !n.sidebarHidden
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
	case "new_session":
		n.expectSession(st)
		if w := findWorkspace(st, ws); w != nil {
			return proto.NewSession{Cwd: w.Path, FromPane: n.focused()}
		}
		return proto.NewSession{}
	case "new_tab", "close_tab", "next_tab", "prev_tab":
		return n.tabOp(st, strings.TrimSuffix(act, "_tab"))
	}
	if d, ok := strings.CutPrefix(act, "goto_tab_"); ok {
		return n.tabOp(st, "goto_"+d)
	}
	if d, ok := strings.CutPrefix(act, "jump_session_"); ok {
		if i, _ := strconv.Atoi(d); i >= 1 && i <= len(ordered(st)) {
			n.selectWorkspace(st, ordered(st)[i-1].ID, "")
		}
	}
	return nil
}
