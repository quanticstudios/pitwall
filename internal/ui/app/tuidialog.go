package app

import (
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A slash command that draws its own screen in Claude Code's TUI, such as
// /config or /mcp, opens that screen in a dialog over the chat: the pane's
// own terminal view, sized to the dialog, taking the keys. The dialog goes
// once the TUI draws its input box again, as Esc or the command's own exit
// leaves it, or on its close button; chat_terminal from it goes to the full
// TUI. A pane chat's command goes to its pane as any message does. A chat
// tab's opens the tab's terminal view on the same session (proto.ChatTerminal)
// behind the dialog, types the command once the TUI is up, and goes back to
// the chat when the dialog closes.

// tuiCommands are Claude Code's slash commands that open a screen of their
// own. /model and /effort are the composer's chips, and /usage, /context and
// /cost the chat's cards, so they stay out.
var tuiCommands = map[string]bool{
	"login": true, "config": true, "settings": true, "status": true, "permissions": true, "allowed-tools": true,
	"mcp": true, "agents": true, "hooks": true, "memory": true, "theme": true, "resume": true, "plugin": true,
	"plugins": true, "ide": true, "export": true, "rewind": true, "tasks": true, "bashes": true, "doctor": true,
	"output-style": true, "privacy-settings": true, "add-dir": true, "install-github-app": true, "feedback": true,
	"bug": true, "terminal-setup": true,
}

// tuiCommand is the command text runs when it is one of tuiCommands, ""
// otherwise.
func tuiCommand(text string) string {
	t := strings.TrimSpace(text)
	name, _, _ := strings.Cut(strings.TrimPrefix(t, "/"), " ")
	if !strings.HasPrefix(t, "/") || !tuiCommands[name] {
		return ""
	}
	return t
}

// tuiWait is how long a command gets to bring its screen up. One that
// only prints, as /agents does in some versions, never opens the dialog.
const tuiWait = 5 * time.Second

// tuiDialog is a TUI-only command's dialog over a chat.
type tuiDialog struct {
	cmd   string    // the command, typed as sent
	pane  string    // the pane it shows; a chat tab's terminal view, "" until it opens
	tab   bool      // a chat tab's: the terminal view goes when the dialog does
	typed bool      // the command went to the pane
	enter time.Time // a chat tab's: when the command's text went, Enter follows it
	since time.Time // when the command went to the pane
	up    bool      // its screen came up: the TUI's input box left the screen

	close, full widget.Clickable
	backdrop    int
}

// step moves the dialog on by whether the TUI draws its input box at now,
// and reports whether it shows and whether it stays: it shows once the
// command's screen is up, a chat tab's from the start, and goes when the
// box is back or the screen never came.
func (d *tuiDialog) step(ready bool, now time.Time) (show, keep bool) {
	if !d.typed {
		return d.tab, true
	}
	if !ready {
		d.up = true
	}
	switch {
	case d.up && ready:
		return false, false
	case !d.up && now.Sub(d.since) > tuiWait:
		return false, false
	}
	return d.up || d.tab, true
}

// openTUI opens chat id's dialog for cmd. ok false leaves cmd to go as a
// message: a chat tab mid-turn, one that is not Claude's, or a command with
// arguments, which the driver runs headless, as /config key=value.
func (u *ui) openTUI(st *model.State, id, cmd string, now time.Time) bool {
	d := &tuiDialog{cmd: cmd, since: now}
	if c := findChat(st, id); paneChatOf(st, id) == nil {
		if c == nil || c.Provider != model.ProviderClaude || chatBusy(c.State) || strings.Contains(cmd, " ") {
			return false
		}
		d.tab = true
		if c.Terminal == "" {
			u.send(proto.ChatTerminal{Chat: id, On: true})
		}
	} else {
		d.pane, d.typed = id, true
	}
	if u.tui == nil {
		u.tui = map[string]*tuiDialog{}
	}
	u.tui[id] = d
	return true
}

// closeTUI closes chat id's dialog; a chat tab's goes back to the chat.
func (u *ui) closeTUI(st *model.State, id string) {
	d := u.tui[id]
	if d == nil {
		return
	}
	delete(u.tui, id)
	if c := findChat(st, id); d.tab && c != nil && c.Terminal != "" {
		u.send(proto.ChatTerminal{Chat: id})
	}
}

// tuiShown is chat id's dialog when it shows this frame, nil otherwise. It
// types a chat tab's command once its TUI is up, and closes the dialog
// that is done.
func (u *ui) tuiShown(st *model.State, id string, now time.Time) *tuiDialog {
	d := u.tui[id]
	if d == nil {
		return nil
	}
	if d.tab {
		c := findChat(st, id)
		if c == nil || d.pane != "" && c.Terminal != d.pane {
			delete(u.tui, id) // the terminal view went from elsewhere
			return nil
		}
		d.pane = c.Terminal
		if d.pane == "" || findPane(st, d.pane) == nil {
			if now.Sub(d.since) > tuiWait {
				u.closeTUI(st, id)
				return nil
			}
			return d
		}
	}
	g, _, ok := u.b.Frame(d.pane)
	ready := ok && agent.InputReady(model.ProviderClaude, g)
	if d.tab && !d.typed {
		switch {
		case !d.enter.IsZero() && now.Sub(d.enter) >= tuiEnterAfter:
			u.send(proto.Input{Pane: d.pane, Data: []byte{'\r'}})
			d.typed, d.since = true, now
		case d.enter.IsZero() && ready:
			// why: Enter in the same write reads as a pasted newline.
			u.send(proto.Input{Pane: d.pane, Data: []byte(d.cmd)})
			d.enter = now
		}
		if u.invalidate != nil {
			u.invalidate()
		}
		return d
	}
	show, keep := d.step(ready, now)
	if !keep {
		u.closeTUI(st, id)
		return nil
	}
	if u.invalidate != nil && !show {
		u.invalidate() // poll the screen until the command's comes up
	}
	if !show {
		return nil
	}
	return d
}

// tuiEnterAfter is the pause between a command's text and its Enter.
const tuiEnterAfter = 150 * time.Millisecond

// tuiRect is the dialog's card in a chat area in: inset on every side, so
// the chat shows around it, and never smaller than a usable terminal.
func tuiRect(gtx gl.Context, in image.Rectangle) image.Rectangle {
	m := gtx.Dp(theme.Space2XL)
	if in.Dx() < gtx.Dp(480) || in.Dy() < gtx.Dp(360) {
		m = gtx.Dp(theme.SpaceS)
	}
	return in.Inset(m)
}

// layoutTUI draws chat id's dialog d over the chat area in, in the
// coordinates layoutPane takes: a scrim over the chat, a card with the
// command and its close button, and the pane's terminal view in it.
func (u *ui) layoutTUI(gtx gl.Context, st *model.State, id string, d *tuiDialog, in image.Rectangle) {
	th := u.th
	for d.close.Clicked(gtx) {
		if d.up && findPane(st, d.pane) != nil { // leave the TUI at its prompt, as Esc would
			u.send(proto.Input{Pane: d.pane, Data: []byte{0x1b}})
		}
		u.closeTUI(st, id)
		return
	}
	for d.full.Clicked(gtx) {
		u.tuiFull(st, id)
		return
	}
	// The scrim takes the presses meant for the chat under it.
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &d.backdrop, Kinds: pointer.Press | pointer.Scroll, ScrollY: pointer.ScrollRange{Min: -1 << 30, Max: 1 << 30}}); !ok {
			break
		}
	}
	paint.FillShape(gtx.Ops, scrim(th, 1), clip.UniformRRect(in, roundedFor(gtx, false)).Op(gtx.Ops))
	area := clip.Rect(in).Push(gtx.Ops)
	event.Op(gtx.Ops, &d.backdrop)
	area.Pop()

	card := tuiRect(gtx, in)
	kit.Surface(gtx, card, gtx.Dp(theme.RadiusCard), kit.Modal, th.BorderSubtle, th.Surface)
	head := gtx.Dp(chatHeadHeight)
	{
		o := op.Offset(card.Min).Push(gtx.Ops)
		cl := clip.UniformRRect(image.Rectangle{Max: card.Size()}, gtx.Dp(theme.RadiusCard)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(card.Dx(), head))
		h := u.tuiHead(id)
		u.strip(g, th.Surface, []gl.FlexChild{
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return sidebar.Icon(gtx, "terminal", gtx.Dp(headIcon), th.Muted) }),
			gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout),
			gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
				dm := label(gtx, th, th.MonoFont, th.Sp(theme.Body), th.Fg, d.cmd)
				return gl.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, dm.Size.Y)}
			}),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return u.tipped(gtx, h, &d.full, withKey("Full terminal", u.keyOf("chat_terminal")), func(gtx gl.Context) gl.Dimensions {
					return ghostIconButton(gtx, th, &d.full, "terminal", "Terminal")
				})
			}),
			gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return u.tipped(gtx, h, &d.close, "Close", func(gtx gl.Context) gl.Dimensions {
					return kit.IconButton(gtx, th, &d.close, kit.Ghost, kit.Medium, func(gtx gl.Context, col color.NRGBA, size int) {
						sidebar.Icon(gtx, "x", size, col)
					})
				})
			}),
		})
		cl.Pop()
		o.Pop()
	}
	body := image.Rectangle{Min: image.Pt(card.Min.X, card.Min.Y+head), Max: card.Max}.Inset(gtx.Dp(theme.SpaceS))
	if body.Empty() {
		return
	}
	if d.pane == "" || findPane(st, d.pane) == nil { // a chat tab's TUI on its way
		o := op.Offset(body.Min).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(body.Size())
		gl.Center.Layout(g, func(gtx gl.Context) gl.Dimensions { return kit.Loading(gtx, th, "Starting Claude Code") })
		o.Pop()
		return
	}
	// The terminal takes the keys, Esc included, which the TUI reads as
	// leaving its screen; chat_terminal still reaches the window.
	u.layoutPane(gtx, st, u.paneView(st, d.pane), d.pane, layout.Rect{X: body.Min.X, Y: body.Min.Y, W: body.Dx(), H: body.Dy()}, true, false, false, nil)
}

// tuiHead is chat id's dialog's tooltip state, kept with the pane chat's
// header so a tooltip hovered in either times as one.
func (u *ui) tuiHead(id string) *chatHead {
	if h := u.paneHeads[id]; h != nil {
		return h
	}
	return &u.chatHead
}

// tuiFull leaves chat id's dialog for the full TUI: a pane chat flips to
// its terminal, a chat tab keeps its terminal view and drops the dialog.
func (u *ui) tuiFull(st *model.State, id string) {
	d := u.tui[id]
	if d == nil {
		return
	}
	delete(u.tui, id)
	if !d.tab {
		u.flipPaneChat(id)
	}
}
