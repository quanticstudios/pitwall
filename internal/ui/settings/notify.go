package settings

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	gl "gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// notifyPage is the Notifications category's state.
type notifyPage struct {
	quiet   widget.Editor // quiet_hours being typed
	focused bool          // quiet had focus last frame
	err     string        // why the typed quiet hours did not save
}

// notifyAgents are the names muted_agents takes, with their labels.
var notifyAgents = []struct{ name, label, desc string }{
	{"claude", "Claude Code", ""},
	{"codex", "Codex", ""},
	{"pi", "pi", ""},
	{"gemini", "Gemini CLI", ""},
	{"opencode", "OpenCode", ""},
	{"terminal", "Terminals", "pitwall notify, a bell, or OSC 9 from a program in a shell."},
}

func (p *Page) notifications() []section {
	n := p.s.Notifications
	approvals := "An agent asks permission, or has a plan to approve."
	if runtime.GOOS == "linux" {
		approvals += " The notification has Allow and Deny, and a click on it shows the pane."
	}
	states := []row{
		{label: "Approvals", desc: approvals, extra: "notification permission plan approval allow deny", control: p.toggle("notifications", "approval", n.Approval)},
		{label: "Questions", desc: "An agent asks you something, or a terminal asks for you.", extra: "notification input question", control: p.toggle("notifications", "input", n.Input)},
		{label: "Finished turns", desc: "An agent finishes a turn.", extra: "notification done completed", control: p.toggle("notifications", "done", n.Done)},
		{label: "Failed turns", desc: "An agent's turn ends in an error.", extra: "notification failed error", control: p.toggle("notifications", "failed", n.Failed)},
	}
	sound := row{label: "Sound", desc: "Plays with each notification, through paplay, pw-play or afplay.", extra: "notification sound bell audio beep",
		control: p.soundChoice(n.Sound)}
	if n.Sound != "" && n.Sound != "bell" {
		sound.desc = "Plays " + shortPath(n.Sound) + " with each notification. Set sound under [notifications] to change the file."
	}
	quiet := row{label: "Quiet hours", desc: "As 22:00-08:00. Then nothing plays a sound, and only approvals, errors and what triage rates now notify. Empty for none.",
		extra: "notification quiet hours night do not disturb dnd", control: p.quietField(n.QuietHours)}
	if p.nt.err != "" {
		quiet.below = func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, p.th.UIFont, p.th.Sp(theme.Small), p.th.Red, p.nt.err)
		}
	}
	var agents []row
	for _, a := range notifyAgents {
		muted := slices.Contains(n.MutedAgents, a.name)
		agents = append(agents, row{label: a.label, desc: a.desc, extra: "notification mute muted_agents " + a.name,
			control: p.switchOf("mute:"+a.name, !muted, func() { p.mute(n.MutedAgents, a.name, !muted) })})
	}
	return []section{
		{title: "Notify when", rows: states},
		{rows: []row{sound, quiet}},
		{title: "Agents", desc: "Turn one off to stop all its notifications.", rows: agents},
	}
}

// soundChoice is Off, Bell, and File when sound names one.
func (p *Page) soundChoice(sound string) gl.Widget {
	opts, cur := []string{"Off", "Bell"}, "Off"
	switch sound {
	case "":
	case "bell":
		cur = "Bell"
	default:
		opts, cur = append(opts, filepath.Base(sound)), filepath.Base(sound)
	}
	return p.segmented("notifications.sound", opts, cur, func(o string) {
		switch o {
		case "Off":
			p.save("notifications", "sound", nil)
		case "Bell":
			p.saveValue("notifications", "sound", config.Quote("bell"))
		}
	})
}

// quietField edits quiet_hours, saved on Enter or when it loses focus.
func (p *Page) quietField(cur string) gl.Widget {
	return func(gtx gl.Context) gl.Dimensions {
		e := &p.nt.quiet
		e.SingleLine, e.Submit = true, true
		submit := false
		for {
			ev, ok := e.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				submit = true
			}
		}
		focused := gtx.Focused(e)
		if submit || p.nt.focused && !focused {
			p.saveQuiet(e.Text(), cur)
		} else if !focused && e.Text() != cur && p.nt.err == "" {
			e.SetText(cur)
		}
		p.nt.focused = focused
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(140))
		return p.field(gtx, e, "22:00-08:00", false)
	}
}

func (p *Page) saveQuiet(s, cur string) {
	s = strings.TrimSpace(s)
	p.nt.err = ""
	switch {
	case s == cur:
	case s == "":
		p.save("notifications", "quiet_hours", nil)
	default:
		if _, _, err := config.ParseQuietHours(s); err != nil {
			p.nt.err = err.Error()
			return
		}
		p.saveValue("notifications", "quiet_hours", config.Quote(s))
	}
}

// mute writes muted_agents with name in it or out of it.
func (p *Page) mute(muted []string, name string, on bool) {
	muted = slices.DeleteFunc(slices.Clone(muted), func(s string) bool { return s == name })
	if on {
		muted = append(muted, name)
	}
	if len(muted) == 0 {
		p.save("notifications", "muted_agents", nil)
		return
	}
	q := make([]string, len(muted))
	for i, s := range muted {
		q[i] = config.Quote(s)
	}
	p.saveValue("notifications", "muted_agents", "["+strings.Join(q, ", ")+"]")
}
