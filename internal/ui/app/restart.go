package app

import (
	"log"

	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/update"
)

// Behind reports whether a daemon of this proto.Version, at level and
// running build, is older than this pitwall: a lower Level, or an older
// release. Builds compare only when both are release tags, so a dev or
// dirty build on either side counts by Level alone.
func Behind(level int, build string) bool {
	return level < proto.Level || update.Newer(Version, build)
}

// daemonBehind reports whether this window's daemon is Behind. It still
// serves the window, but what the newer pitwall added does nothing until
// it restarts, so the update button offers that. Only a local daemon: on a
// --host the restart would start that machine's own pitwall, which may be
// as old.
func (u *ui) daemonBehind() bool {
	l, ok := u.b.(Linker)
	if !ok || curHost() != "" {
		return false
	}
	lk := l.Link()
	return lk.State == LinkUp && Behind(lk.Level, lk.Build)
}

// windowBehind reports whether this window's local daemon runs a newer
// release than the window: an update installed a new binary and moved the
// daemon onto it, while this window kept running the old one. Only
// Relaunch can fix that, so without it the answer is false.
func (u *ui) windowBehind() bool {
	l, ok := u.b.(Linker)
	if !ok || curHost() != "" || Relaunch == nil {
		return false
	}
	lk := l.Link()
	return lk.State == LinkUp && update.Newer(lk.Build, Version)
}

// relaunchWindow opens a new window on the installed binary, on this
// session and tab, and closes this one. The daemon keeps running.
func (u *ui) relaunchWindow(st *model.State) {
	name := ""
	if s := st.Session(u.nav.session); s != nil {
		name = s.Name
	}
	if name == "" || u.nav.workspace == "" {
		return
	}
	if err := Relaunch(name, u.nav.workspace); err != nil {
		log.Printf("relaunch on a newer daemon: %q", err)
		u.sendFailed("Could not reopen the window: " + err.Error())
		return
	}
	log.Printf("the daemon runs a newer release than this window; reopened it")
	u.relaunched = true
}

// drawStaleNotice asks, at the top of the panes, for a window restart
// while windowBehind, until Later.
func (u *ui) drawStaleNotice(gtx gl.Context, st *model.State) {
	n := &u.stale
	for n.restart.Clicked(gtx) {
		u.relaunchWindow(st)
	}
	for n.later.Clicked(gtx) {
		n.hidden = true
	}
	if n.hidden || !u.windowBehind() {
		return
	}
	if n.shownAt.IsZero() {
		n.shownAt = gtx.Now
	}
	r := layout.Rect{W: gtx.Constraints.Max.X, H: gtx.Constraints.Max.Y}
	u.actionPill(gtx, r, true, "Restart the window to finish updating", &n.restart, "Restart", &n.later, "Later", &n.box, n.shownAt)
}

// updateLabel is the update button's text: the updater's, else "Restart
// to finish" while the window or the daemon is behind.
func (u *ui) updateLabel() string {
	if s := u.updates.label(); s != "" || !u.windowBehind() && !u.daemonBehind() {
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
					"Restarting hands every pane to the new version with its program still running. "+
					"If that fails, it stops the programs in panes; agents resume where they left off.")
		}),
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.buttons(gtx, "Later", "Restart now", kit.Danger)
		}),
	)
}
