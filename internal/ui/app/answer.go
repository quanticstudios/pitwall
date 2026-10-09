package app

import (
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// canAnswer reports whether the daemon takes proto.Answer, for the
// sidebar's Allow and Deny.
func (u *ui) canAnswer() bool { return u.link().Level >= proto.Since(proto.Answer{}) }

// answer is allow_prompt's or deny_prompt's message: Allow or Deny on the
// focused pane's permission prompt, else on the one the shown tab's row
// shows; nil when neither is one.
func (n *nav) answer(st *model.State, allow bool) any {
	var tab []model.Activity
	for _, a := range st.Activities {
		if a.WorkspaceID == n.workspace {
			tab = append(tab, a)
		}
	}
	i := slices.IndexFunc(tab, func(a model.Activity) bool { return a.PaneID == n.focused() })
	a := model.Aggregate(tab)
	if i >= 0 && remote.Answerable(tab[i]) {
		a = &tab[i]
	}
	if a == nil || !remote.Answerable(*a) {
		return nil
	}
	return proto.Answer{Pane: a.PaneID, At: a.UpdatedAt.UnixNano(), Allow: allow}
}

// queueJump asks the window to show a's pane, from a click on its
// notification. Any goroutine may call it.
func (u *ui) queueJump(a model.Activity) {
	u.focusMu.Lock()
	u.jumpReq = &a
	u.focusMu.Unlock()
}

// applyJump shows the pane queueJump asked for, as the jump key does.
func (u *ui) applyJump(st *model.State) {
	u.focusMu.Lock()
	a := u.jumpReq
	u.jumpReq = nil
	u.focusMu.Unlock()
	if a == nil || findWorkspace(st, a.WorkspaceID) == nil {
		return
	}
	u.settings.Hide()
	u.nav.tabMode, u.nav.paneMode = false, false
	u.nav.selectWorkspace(st, a.WorkspaceID, a.PaneID)
}
