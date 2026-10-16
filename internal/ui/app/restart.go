package app

import (
	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// daemonBehind reports whether this window's daemon is older than the
// window: the same proto.Version at a lower Level. It still serves the
// window, but what the newer Levels added does nothing until it restarts,
// so the update button offers that. Only a local daemon: on a --host the
// restart would start that machine's own pitwall, which may be as old.
func (u *ui) daemonBehind() bool {
	l, ok := u.b.(Linker)
	return ok && Host == "" && l.Link().State == LinkUp && l.Link().Level < proto.Level
}

// updateLabel is the update button's text: the updater's, else "Restart
// to finish" while the daemon is behind.
func (u *ui) updateLabel() string {
	if s := u.updates.label(); s != "" || !u.daemonBehind() {
		return s
	}
	return updated
}

func (u *ui) confirmRestart() {
	if l, ok := u.b.(Linker); ok {
		l.Restart()
	}
	u.modal.close()
}

func (u *ui) restartBody(gtx gl.Context) gl.Dimensions {
	th := u.th
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, "Restart pitwall's background service?")
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, th.Sp(theme.Large), th.Muted,
				"It still runs the pitwall from before the update, so the new features that need it do nothing yet. "+
					"Restarting stops every program running in a pane; agents resume where they left off.")
		}),
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.buttons(gtx, "Later", "Restart now", kit.Danger)
		}),
	)
}
