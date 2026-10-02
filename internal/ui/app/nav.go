package app

import (
	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// nav is the window's selection and the Alt-hold switcher, kept free of Gio
// windows so it can be tested on its own.
type nav struct {
	workspace string            // active workspace id
	focus     map[string]string // workspace id -> focused pane
	altHeld   bool              // Alt is down on its own: the switcher shows
	pinned    bool              // Alt+Space keeps the switcher open after release

	// An OpenPane is in flight: the workspace and the panes it had, so the
	// pane that shows up next gets focus.
	openingWS string
	opening   map[string]bool
}

// expectPane records the active workspace's panes before an OpenPane.
func (n *nav) expectPane(st *model.State) {
	n.openingWS, n.opening = n.workspace, map[string]bool{}
	for _, p := range n.panes(st) {
		n.opening[p] = true
	}
}

func (n *nav) switcherVisible() bool { return n.altHeld || n.pinned }

// ordered is aide's navigation order: project order, then each project's
// workspace order, then workspaces whose project is unknown. Archived
// workspaces are skipped.
func ordered(st *model.State) []model.Workspace {
	var out []model.Workspace
	seen := map[string]bool{}
	for _, p := range st.Projects {
		for _, w := range st.Workspaces {
			if w.ProjectID == p.ID && !w.Archived {
				out = append(out, w)
				seen[w.ID] = true
			}
		}
	}
	for _, w := range st.Workspaces {
		if !seen[w.ID] && !w.Archived {
			out = append(out, w)
		}
	}
	return out
}

func findWorkspace(st *model.State, id string) *model.Workspace {
	for i := range st.Workspaces {
		if st.Workspaces[i].ID == id {
			return &st.Workspaces[i]
		}
	}
	return nil
}

func (n *nav) panes(st *model.State) []string {
	if w := findWorkspace(st, n.workspace); w != nil {
		return panesOf(w.Layout)
	}
	return nil
}

// focused is the focused pane of the active workspace, or "".
func (n *nav) focused() string { return n.focus[n.workspace] }

// sync repairs the selection after a state change: a deleted workspace falls
// back to the first one, a closed pane to the first pane left.
func (n *nav) sync(st *model.State) {
	if n.focus == nil {
		n.focus = map[string]string{}
	}
	if w := findWorkspace(st, n.workspace); w == nil || w.Archived {
		n.workspace = ""
		if ws := ordered(st); len(ws) > 0 {
			n.workspace = ws[0].ID
		}
	}
	ps := n.panes(st)
	if n.opening != nil && n.openingWS == n.workspace {
		for _, p := range ps {
			if !n.opening[p] {
				n.focus[n.workspace], n.opening = p, nil
				return
			}
		}
	}
	for _, p := range ps {
		if p == n.focus[n.workspace] {
			return
		}
	}
	if len(ps) > 0 {
		n.focus[n.workspace] = ps[0]
	} else {
		delete(n.focus, n.workspace)
	}
}

// selectWorkspace activates id and focuses pane when it is one of its panes,
// else the pane it last had focused.
func (n *nav) selectWorkspace(st *model.State, id, pane string) {
	n.workspace = id
	if pane != "" {
		n.focus[id] = pane
	}
	n.sync(st)
}

func (n *nav) cycleWorkspace(st *model.State, d int) {
	ws := ordered(st)
	if !n.switcherVisible() {
		cur := findWorkspace(st, n.workspace)
		var local []model.Workspace
		for _, w := range ws {
			if cur != nil && w.ProjectID == cur.ProjectID {
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
			n.focus[n.workspace] = ps[(i+d+len(ps))%len(ps)]
			return
		}
	}
}

// altKeys are the key names nav consumes with Alt alone held.
var altKeys = []key.Name{
	"J", "K", "H", "L", key.NameDownArrow, key.NameUpArrow, key.NameLeftArrow, key.NameRightArrow,
	key.NameSpace, "N", "1", "2", "3", "4", "5", "6", "7", "8", "9",
}

// keyFilters are the filters the window polls before any pane sees input,
// so these chords and Alt on its own never reach a PTY.
func (n *nav) keyFilters() []key.Filter {
	all := key.ModAlt | key.ModShift | key.ModCtrl | key.ModSuper | key.ModCommand
	altShift := key.ModAlt | key.ModShift
	fs := []key.Filter{
		{Name: key.NameAlt, Optional: all},
		{Name: "N", Required: altShift},
		{Name: "W", Required: altShift},
		{Name: "T", Required: altShift},
	}
	for _, k := range altKeys {
		fs = append(fs, key.Filter{Name: k, Required: key.ModAlt})
	}
	if n.switcherVisible() {
		fs = append(fs, key.Filter{Name: key.NameEscape})
	}
	return fs
}

// key applies one key event and returns a proto message to send, if any.
func (n *nav) key(st *model.State, e key.Event) any {
	if e.Name == key.NameAlt {
		// Like aide, Alt with Ctrl/Shift/Super held is not a hold.
		n.altHeld = e.State == key.Press && e.Modifiers&^key.ModAlt == 0
		return nil
	}
	if e.State != key.Press {
		return nil
	}
	if e.Name == key.NameEscape {
		n.altHeld, n.pinned = false, false
		return nil
	}
	shift := e.Modifiers&key.ModShift != 0
	ws := n.workspace
	switch e.Name {
	case "J", key.NameDownArrow:
		n.cycleWorkspace(st, 1)
	case "K", key.NameUpArrow:
		n.cycleWorkspace(st, -1)
	case "H", key.NameLeftArrow:
		n.cyclePane(st, -1)
	case "L", key.NameRightArrow:
		n.cyclePane(st, 1)
	case key.NameSpace:
		n.pinned = !n.pinned
	case "N":
		if ws == "" {
			return nil
		}
		dir := layout.Horizontal
		if shift {
			dir = layout.Vertical
		}
		n.expectPane(st)
		return proto.OpenPane{WorkspaceID: ws, Target: n.focused(), Dir: dir}
	case "W":
		if shift && n.focused() != "" {
			return proto.ClosePane{Pane: n.focused()}
		}
	case "T":
		if w := findWorkspace(st, ws); shift && w != nil {
			return proto.NewWorkspace{ProjectID: w.ProjectID}
		}
	default: // 1..9
		if i := int(e.Name[0] - '1'); i < len(ordered(st)) {
			n.selectWorkspace(st, ordered(st)[i].ID, "")
		}
	}
	return nil
}
