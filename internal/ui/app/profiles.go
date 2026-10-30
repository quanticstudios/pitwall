package app

import (
	"slices"

	gl "gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// profileChoice is shared by New task and New tab. The empty name keeps
// the daemon's existing environment and does not add an account mark.
type profileChoice struct {
	names, labels []string
	picked        int
	buttons       []widget.Clickable
	pending       any
	sending       bool
}

func (p *profileChoice) reset(s config.Settings) {
	p.names, p.labels, p.picked = []string{""}, []string{"Default"}, 0
	var names []string
	for name := range s.Profiles {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		p.names = append(p.names, name)
		p.labels = append(p.labels, s.ProfileLabel(name))
	}
	p.buttons = make([]widget.Clickable, len(p.names))
}

func (p *profileChoice) name() string {
	if p.picked < 0 || p.picked >= len(p.names) {
		return ""
	}
	return p.names[p.picked]
}

func (p *profileChoice) update(gtx gl.Context) {
	for i := range p.buttons {
		for p.buttons[i].Clicked(gtx) {
			p.picked = i
		}
	}
}

// pickTabProfile delays a new shell tab until the user picks an account.
// With no configured profiles, send follows its existing path unchanged.
func (u *ui) pickTabProfile(msg any) bool {
	if len(u.cfg.Profiles) == 0 || u.profile.sending {
		return false
	}
	switch m := msg.(type) {
	case proto.NewTab:
		if m.Profile != "" {
			return false
		}
	case proto.NewSession:
		if m.Profile != "" || len(m.Cmd) != 0 || len(m.Env) != 0 {
			return false
		}
	default:
		return false
	}
	u.profile.reset(u.cfg)
	u.profile.pending = msg
	u.nav.sessions = nil // a tab is expected only after the picker is confirmed
	u.modal.open(modalProfile, "")
	return true
}

func (u *ui) confirmProfileTab() {
	name := u.profile.name()
	if name != "" && linkLevel(u.b) < proto.ProfileLevel {
		u.modal.pathErr = "Account profiles work once the older pitwall daemon restarts."
		return
	}
	msg := u.profile.pending
	switch m := msg.(type) {
	case proto.NewTab:
		m.Profile = name
		msg = m
	case proto.NewSession:
		m.Profile = name
		msg = m
	}
	u.profile.sending = true
	st := u.b.State()
	u.nav.expectSession(&st)
	ok := u.send(msg)
	u.profile.sending = false
	if ok {
		u.modal.close()
		u.profile.pending = nil
	} else {
		u.nav.sessions = nil
	}
}

func (u *ui) profileTabBody(gtx gl.Context) gl.Dimensions {
	u.profile.update(gtx)
	th := u.th
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, "New tab")
		}),
		gl.Rigid(gl.Spacer{Height: 16}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, th.Sp(theme.Body), th.Muted, "Account profile")
		}),
		gl.Rigid(gl.Spacer{Height: 6}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.chips(gtx, u.profile.buttons, u.profile.labels, u.profile.picked)
		}),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, th.Sp(theme.Small), th.Red, u.modal.pathErr)
		}),
		gl.Rigid(gl.Spacer{Height: 20}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.buttonRow(gtx, dialogButton{&u.modal.ok, "Open tab", kit.Primary}, dialogButton{&u.modal.cancel, "Cancel", kit.Secondary})
		}),
	)
}
