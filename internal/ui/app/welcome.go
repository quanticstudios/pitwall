package app

import (
	"errors"
	"image"
	"os"
	"os/exec"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
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
	{"omp", "Oh My Pi", "https://github.com/can1357/oh-my-pi"},
	{"cursor-agent", "Cursor CLI", "https://cursor.com/cli"},
	{"amp", "Amp", "https://ampcode.com"},
	{"aider", "Aider", "https://aider.chat"},
	{"agy", "Antigravity", "https://antigravity.google"},
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
	// preset marks an [[agents.preset]]: cmd with args, its tab getting vars.
	preset     bool
	args, vars []string
	terminal   bool // Claude Code (terminal): the TUI where Claude Code opens a chat
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
			p := agent.Identify(a.cmd, "")
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
			if a.cmd == "claude" && u.chatClaude() {
				u.send(welcomeChat(st, u.nav.session, u.nav.focused()))
				continue
			}
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
	// Handle Escape while the welcome modal holds focus, swallow plain/Shift
	// keys on the backdrop so typing cannot reach the terminal underneath, and
	// keep keyboard focus on welcome when no higher modal holds it.
	focusTag := w.focusedTag(gtx)
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: &w.backdrop},
			key.Filter{Focus: &w.backdrop, Name: key.NameEscape},
			key.Filter{Focus: focusTag, Name: key.NameEscape},
			key.Filter{Focus: &w.backdrop, Optional: key.ModShift},
		)
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press && e.Name == key.NameEscape {
			u.endWelcome()
			return
		}
	}
	// The panes dim behind the card; a press on them dismisses it, as one
	// outside an overlay closes it.
	if u.backdrop(gtx, anim.At(gtx, w.shownAt, anim.Dialog), &w.backdrop) {
		u.endWelcome()
		return
	}
	if !u.keysHeld(st) {
		gtx.Execute(key.FocusCmd{Tag: focusTag})
	}
	u.card(gtx, &w.tag, w.shownAt, u.welcomeBody)
}

// modal reports whether the centered modal welcome card is currently shown
// (as opposed to the non-modal bottom no-agents card).
func (w *welcome) modal() bool { return w.on && len(w.agents) > 0 }

// focusedTag returns whichever welcome control already has keyboard focus, or
// &w.backdrop so the dimmed terminal behind the card never receives keys.
func (w *welcome) focusedTag(gtx gl.Context) event.Tag {
	if gtx.Focused(&w.dismiss) {
		return &w.dismiss
	}
	if gtx.Focused(&w.install) {
		return &w.install
	}
	for i := range w.start {
		if gtx.Focused(&w.start[i]) {
			return &w.start[i]
		}
	}
	return &w.backdrop
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

// drawNoAgents is the first run without an agent on PATH: a wrapping
// multi-line card at the bottom of the pane area with where to get each,
// and Dismiss.
func (u *ui) drawNoAgents(gtx gl.Context) {
	th, w := u.th, &u.welcome
	size := gtx.Constraints.Max
	margin := gtx.Dp(16)
	padX, padY := gtx.Dp(14), gtx.Dp(10)
	gapX, gapY := gtx.Dp(12), gtx.Dp(6)
	maxBoxW := min(size.X-2*margin, gtx.Dp(640))
	innerW := maxBoxW - 2*padX
	if innerW < gtx.Dp(120) || size.Y < gtx.Dp(60) {
		return
	}

	type part struct {
		c    *widget.Clickable
		call op.CallOp
		size image.Point
	}
	measure := gtx
	measure.Constraints = gl.Constraints{Max: image.Pt(innerW, size.Y)}
	msg, ms := textCall(measure, th, medium(th.UIFont), th.Sp(theme.Body), th.Fg, "No agent CLI on PATH. Install one:")
	dismissCol := th.Muted
	if w.dismiss.Hovered() {
		dismissCol = th.Fg
	}
	dc, ds := textCall(measure, th, th.UIFont, th.Sp(theme.Body), dismissCol, "Dismiss")

	links := make([]part, 0, len(welcomeAgents))
	for i, a := range welcomeAgents {
		col := th.Primary
		if w.links[i].Hovered() {
			col = theme.Mix(th.Primary, th.Fg, 0.3)
		}
		c, s := textCall(measure, th, th.UIFont, th.Sp(theme.Body), col, a.name)
		links = append(links, part{c: &w.links[i], call: c, size: s})
	}

	// Wrap agent links into rows that fit within innerW.
	type line struct {
		items []part
		w, h  int
	}
	var rows []line
	for _, p := range links {
		if len(rows) == 0 || rows[len(rows)-1].w+gapX+p.size.X > innerW {
			rows = append(rows, line{items: []part{p}, w: p.size.X, h: p.size.Y})
			continue
		}
		cur := &rows[len(rows)-1]
		cur.items = append(cur.items, p)
		cur.w += gapX + p.size.X
		cur.h = max(cur.h, p.size.Y)
	}

	headSameRow := ms.X+gapX+ds.X <= innerW
	contentW := max(ms.X, ds.X)
	headH := ms.Y + gapY + ds.Y
	if headSameRow {
		contentW = ms.X + gapX + ds.X
		headH = max(ms.Y, ds.Y)
	}
	linksH := 0
	for i, r := range rows {
		contentW = max(contentW, r.w)
		if i > 0 {
			linksH += gapY
		}
		linksH += r.h
	}
	box := image.Pt(min(maxBoxW, contentW+2*padX), padY+headH+gapY+linksH+padY)
	if box.Y+2*margin > size.Y {
		return
	}
	defer op.Offset(image.Pt((size.X-box.X)/2, size.Y-box.Y-margin)).Push(gtx.Ops).Pop()
	rad := gtx.Dp(theme.RadiusPopover)
	kit.Surface(gtx, image.Rectangle{Max: box}, rad, kit.Floating, th.BorderStrong, th.Surface)
	area := clip.Rect{Max: box}.Push(gtx.Ops)
	event.Op(gtx.Ops, &w.tag)
	area.Pop()

	drawClick := func(at image.Point, p part) {
		o := op.Offset(at).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(p.size)
		kit.Clickable(g, p.c, func(gtx gl.Context) gl.Dimensions {
			if gtx.Focused(p.c) {
				kit.FocusRing(gtx, th, image.Rectangle{Max: p.size}, gtx.Dp(theme.RadiusControl))
			}
			p.call.Add(gtx.Ops)
			return gl.Dimensions{Size: p.size}
		})
		o.Pop()
	}

	y := padY
	if headSameRow {
		o := op.Offset(image.Pt(padX, y+(headH-ms.Y)/2)).Push(gtx.Ops)
		msg.Add(gtx.Ops)
		o.Pop()
		drawClick(image.Pt(box.X-padX-ds.X, y+(headH-ds.Y)/2), part{c: &w.dismiss, call: dc, size: ds})
	} else {
		o := op.Offset(image.Pt(padX, y)).Push(gtx.Ops)
		msg.Add(gtx.Ops)
		o.Pop()
		drawClick(image.Pt(padX, y+ms.Y+gapY), part{c: &w.dismiss, call: dc, size: ds})
	}
	y += headH + gapY
	for _, r := range rows {
		x := padX
		for _, p := range r.items {
			drawClick(image.Pt(x, y+(r.h-p.size.Y)/2), p)
			x += p.size.X + gapX
		}
		y += r.h + gapY
	}
}
