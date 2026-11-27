package app

import (
	"cmp"
	"log"
	"slices"

	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// dockLayout draws the inbox dock of tab ws's chat in its composer's
// column, then sends what its buttons answered.
func (u *ui) dockLayout(gtx gl.Context, st *model.State, ws string, env chatin.Env) gl.Dimensions {
	if !u.dockInit {
		u.dock.Open, u.dockInit = !u.cfg.Chat.DockFolded, true
	}
	full := gtx.Constraints.Max.X
	w := max(0, min(full-2*gtx.Dp(theme.SpaceXL), gtx.Dp(env.Width)))
	x := (full - w) / 2
	if env.Ledger {
		x, w, _ = chatin.LedgerColumn(gtx, full, gtx.Dp(env.Gutter))
	}
	d := u.dock.Layout(gtx, u.th, st.Inbox, ws, x, w)
	if u.dock.Toggled() {
		u.saveDockFold(!u.dock.Open)
	}
	for _, a := range u.dock.Actions() {
		u.dockAction(st, a)
	}
	return d
}

// dockAction answers an inbox item the way its card or sidebar row
// would: Allow and Deny as proto.Answer, a question's or plan's answer as
// proto.Press. Anything else, or an answer this daemon cannot take,
// shows the item's tab, where its card or form waits.
func (u *ui) dockAction(st *model.State, a panel.DockAction) {
	it := a.Item
	switch a.Answer {
	case panel.DockAllow, panel.DockDeny:
		i := slices.IndexFunc(st.Activities, func(x model.Activity) bool { return x.PaneID == it.PaneID })
		if i >= 0 && remote.Answerable(st.Activities[i]) && st.Activities[i].UpdatedAt.UnixNano() == it.At && u.canAnswer() {
			u.send(proto.Answer{Pane: it.PaneID, At: it.At, Allow: a.Answer == panel.DockAllow})
			return
		}
	case "":
	default:
		if u.link().Level >= proto.Since(proto.Press{}) {
			u.send(proto.Press{Pane: it.PaneID, At: it.At, Label: a.Answer})
			return
		}
	}
	if findWorkspace(st, it.WorkspaceID) == nil {
		return
	}
	u.nav.peek = ""
	u.nav.selectWorkspace(st, it.WorkspaceID, it.PaneID)
}

// saveDockFold keeps the dock folded or open in config.toml.
func (u *ui) saveDockFold(folded bool) {
	u.cfg.Chat.DockFolded = folded
	var err error
	path := cmp.Or(u.cfg.Path, config.Path())
	if folded {
		err = config.SetKey(path, "chat", "inbox_dock", config.Quote("folded"))
	} else {
		err = config.RemoveKey(path, "chat", "inbox_dock")
	}
	if err != nil {
		log.Printf("inbox dock: %q", err)
	}
}
