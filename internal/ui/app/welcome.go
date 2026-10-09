package app

import (
	"errors"
	"image"
	"os"
	"os/exec"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/settings"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// welcomeAgents are the agent CLIs the first-run card looks for on PATH,
// in its order, with where to get each.
var welcomeAgents = []struct{ cmd, name, url string }{
	{"claude", "Claude Code", "https://github.com/anthropics/claude-code"},
	{"codex", "Codex", "https://github.com/openai/codex"},
	{"gemini", "Gemini CLI", "https://github.com/google-gemini/gemini-cli"},
	{"opencode", "OpenCode", "https://github.com/sst/opencode"},
	{"pi", "pi", "https://github.com/badlogic/pi-mono"},
	{"cursor-agent", "Cursor CLI", "https://cursor.com/cli"},
	{"amp", "Amp", "https://ampcode.com"},
	{"aider", "Aider", "https://aider.chat"},
}

// welcome is the first-run card: the agents on PATH with their hook
// status, a Start button for each, and the keys that matter. With no agent
// on PATH it is one line of install links instead.
type welcome struct {
	on       bool
	read     bool // agents is read
	hooksGen int  // the hooksDialog run agents' hook status is from
	agents   []foundAgent

	start                    []widget.Clickable // one per agent
	links                    []widget.Clickable // one per welcomeAgents entry
	install, dismiss, cancel widget.Clickable
	tag                      int       // the card's area, which keeps presses off the pane under it
	backdrop                 int       // the dimmed panes around the card
	shownAt                  time.Time // the card's first frame, for its entrance

}

type foundAgent struct {
	cmd, name string
	hooks     bool // every hook pitwall installs is in its config
	hookless  bool // the agent has no hooks: pitwall reads its screen
}

// NoteFirstRun marks a first run, one with no daemon state, config or
// window state yet, so the window shows the welcome card until the user
// dismisses it or does anything else. main calls it before it starts a
// daemon, which writes the state.
func NoteFirstRun() {
	for _, p := range []string{store.Path(), config.Path(), guiStatePath()} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			return
		}
	}
	saveGUIState(guiState{Welcome: true})
}

// endWelcome hides the card for good.
func (u *ui) endWelcome() {
	u.welcome.on = false
	u.gui.Welcome = false
	saveGUIState(u.gui)
}

// byUser reports whether the window sends msg because the user did
// something, rather than to keep the daemon in step with the window: its
// sizes, the session and pane it shows, a pane's focus report.
func byUser(msg any) bool {
	switch m := msg.(type) {
	case proto.Resize, proto.SessionShow, proto.SeePane:
		return false
	case proto.Input:
		s := string(m.Data)
		return s != "\x1b[I" && s != "\x1b[O"
	}
	return true
}

// findAgents lists the welcomeAgents on PATH, with their hook status in
// the configs under home.
func findAgents(home string) []foundAgent {
	hooked := settings.HooksInstalled(home)
	var out []foundAgent
	for _, a := range welcomeAgents {
		if _, err := exec.LookPath(a.cmd); err == nil {
			p := model.ProviderCursor
			if a.cmd != agent.Command(p) {
				p = model.Provider(a.cmd)
			}
			out = append(out, foundAgent{cmd: a.cmd, name: a.name, hooks: hooked[a.cmd], hookless: !agent.HooksFor(p)})
		}
	}
	return out
}

// drawWelcome draws the welcome card over the panes while it is on. It
// reads the agents again after each run of the Install hooks dialog.
func (u *ui) drawWelcome(gtx gl.Context, st *model.State) {
	w := &u.welcome
	if !w.on {
		return
	}
	u.hooks.mu.Lock()
	gen, busy := u.hooks.gen, u.hooks.busy
	u.hooks.mu.Unlock()
	if !w.read || gen != w.hooksGen && !busy {
		home, _ := os.UserHomeDir()
		w.agents, w.read, w.hooksGen = findAgents(home), true, gen
		w.start = make([]widget.Clickable, len(w.agents))
	}
	for i, a := range w.agents {
		for w.start[i].Clicked(gtx) {
			u.nav.expectSession(st)
			u.send(proto.NewSession{SessionID: u.nav.session, FromPane: u.nav.focused(), Cmd: []string{a.cmd}})
		}
	}
	for w.install.Clicked(gtx) {
		u.openHooks()
	}
	for w.dismiss.Clicked(gtx) {
		u.endWelcome()
	}
	if len(w.links) == 0 {
		w.links = make([]widget.Clickable, len(welcomeAgents))
	}
	for i := range w.links {
		for w.links[i].Clicked(gtx) {
			openLink(welcomeAgents[i].url)
		}
	}
	if !w.on {
		return
	}
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &w.tag, Kinds: pointer.Press}); !ok {
			break
		}
	}
	if w.shownAt.IsZero() {
		w.shownAt = gtx.Now
	}
	if len(w.agents) == 0 {
		u.drawNoAgents(gtx)
		return
	}
	// The panes dim behind the card; a press on them dismisses it, as one
	// outside an overlay closes it.
	if u.backdrop(gtx, anim.At(gtx, w.shownAt, anim.Dialog), &w.backdrop) {
		u.endWelcome()
		return
	}
	u.card(gtx, &w.tag, w.shownAt, u.welcomeBody)
}

func (u *ui) welcomeBody(gtx gl.Context) gl.Dimensions {
	th, w := u.th, &u.welcome
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, "Agents on this machine")
		}),
		gl.Rigid(gl.Spacer{Height: 8}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, th.Sp(theme.Large), th.Muted, "Start one in a tab of its own. With its hooks installed, the sidebar shows when it works, finishes or waits on you.")
		}),
		gl.Rigid(gl.Spacer{Height: 16}.Layout),
	}
	missing := false
	for i, a := range w.agents {
		missing = missing || !a.hooks && !a.hookless
		if i > 0 {
			kids = append(kids, gl.Rigid(gl.Spacer{Height: 8}.Layout))
		}
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions { return u.welcomeAgent(gtx, a, &w.start[i]) }))
	}
	kids = append(kids, gl.Rigid(gl.Spacer{Height: 20}.Layout))
	b := u.nav.bind()
	keys := []struct{ chord, label string }{
		{firstChord(b.Global["jump_attention"]), "Go to the agent waiting on you"},
		{firstChord(b.Global["new_tab"]), "New tab"},
		{firstChord(b.Global["command_palette"]), "Every action and its keys"},
	}
	capW := 0 // the labels line up after the widest keycap
	for _, k := range keys {
		if k.chord != "" {
			_, sz := keycap(gtx, th, k.chord)
			capW = max(capW, sz.X)
		}
	}
	for _, k := range keys {
		if k.chord == "" {
			continue
		}
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			call, sz := keycap(gtx, th, k.chord)
			call.Add(gtx.Ops)
			o := op.Offset(image.Pt(capW+gtx.Dp(10), 0)).Push(gtx.Ops)
			lc, ls := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), th.Muted, k.label)
			op.Offset(image.Pt(0, (sz.Y-ls.Y)/2)).Add(gtx.Ops)
			lc.Add(gtx.Ops)
			o.Pop()
			return gl.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, sz.Y+gtx.Dp(6))}
		}))
	}
	kids = append(kids, gl.Rigid(gl.Spacer{Height: 18}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
		if missing {
			return u.buttonPair(gtx, &w.dismiss, &w.install, "Dismiss", "Install hooks", kit.Primary)
		}
		return u.buttonPair(gtx, &w.cancel, &w.dismiss, "", "Dismiss", kit.Secondary)
	}))
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

// welcomeAgent is one agent's row: its name and hook status, and its
// Start button on the right.
func (u *ui) welcomeAgent(gtx gl.Context, a foundAgent, start *widget.Clickable) gl.Dimensions {
	th := u.th
	h := gtx.Dp(30)
	name, ns := textCall(gtx, th, medium(th.UIFont), th.Sp(theme.Large), th.Fg, a.name)
	status, dot := "No hooks", th.Yellow
	switch {
	case a.hooks:
		status, dot = "Hooks installed", th.Green
	case a.hookless:
		status, dot = "Read from its screen", th.Muted
	}
	sc, ss := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, status)
	o := op.Offset(image.Pt(0, (h-ns.Y)/2)).Push(gtx.Ops)
	name.Add(gtx.Ops)
	o.Pop()
	x := ns.X + gtx.Dp(12)
	d := gtx.Dp(7)
	paint.FillShape(gtx.Ops, dot, clip.UniformRRect(image.Rect(x, (h-d)/2, x+d, (h-d)/2+d), d/2).Op(gtx.Ops))
	o = op.Offset(image.Pt(x+d+gtx.Dp(6), (h-ss.Y)/2)).Push(gtx.Ops)
	sc.Add(gtx.Ops)
	o.Pop()

	o = op.Offset(image.Pt(0, (h-gtx.Dp(28))/2)).Push(gtx.Ops)
	kit.Row(gtx, 0, func(gtx gl.Context) gl.Dimensions {
		return kit.Button(gtx, th, start, kit.Secondary, kit.Medium, "Start")
	})
	o.Pop()
	return gl.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
}

// drawNoAgents is the first run without an agent on PATH: one line at the
// bottom of the pane area with where to get each, and Dismiss.
func (u *ui) drawNoAgents(gtx gl.Context) {
	th, w := u.th, &u.welcome
	type part struct {
		c    *widget.Clickable // nil for plain text
		call op.CallOp
		size image.Point
	}
	msg, ms := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), th.Fg, "No agent CLI on PATH. Install one:")
	parts := []part{{call: msg, size: ms}}
	for i, a := range welcomeAgents {
		col := th.Primary
		if w.links[i].Hovered() {
			col = theme.Mix(th.Primary, th.Fg, 0.3)
		}
		c, s := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), col, a.name)
		parts = append(parts, part{c: &w.links[i], call: c, size: s})
	}
	hc, hs := textCall(gtx, th, th.UIFont, th.Sp(theme.Body), th.Muted, "Dismiss")
	parts = append(parts, part{c: &w.dismiss, call: hc, size: hs})
	pad, gap := gtx.Dp(12), gtx.Dp(12)
	box := image.Pt(pad, ms.Y+2*gtx.Dp(10))
	for i, p := range parts {
		if i > 0 {
			box.X += gap
		}
		box.X += p.size.X
	}
	box.X += pad
	size := gtx.Constraints.Max
	if box.X+gtx.Dp(24) > size.X {
		return
	}
	defer op.Offset(image.Pt((size.X-box.X)/2, size.Y-box.Y-gtx.Dp(16))).Push(gtx.Ops).Pop()
	rad := gtx.Dp(8)
	paint.FillShape(gtx.Ops, th.Border, clip.UniformRRect(image.Rectangle{Max: box}, rad).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(image.Rect(1, 1, box.X-1, box.Y-1), rad-1).Op(gtx.Ops))
	area := clip.Rect{Max: box}.Push(gtx.Ops)
	event.Op(gtx.Ops, &w.tag)
	area.Pop()
	x := pad
	for _, p := range parts {
		o := op.Offset(image.Pt(x, (box.Y-p.size.Y)/2)).Push(gtx.Ops)
		if p.c == nil {
			p.call.Add(gtx.Ops)
		} else {
			g := gtx
			g.Constraints = gl.Exact(p.size)
			p.c.Layout(g, func(gtx gl.Context) gl.Dimensions {
				pointer.CursorPointer.Add(gtx.Ops)
				p.call.Add(gtx.Ops)
				return gl.Dimensions{Size: p.size}
			})
		}
		o.Pop()
		x += p.size.X + gap
	}
}
