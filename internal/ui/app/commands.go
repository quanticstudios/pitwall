package app

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

const shellIntegrationURL = "https://github.com/quanticstudios/pitwall/blob/main/docs/usage.md#shell-integration"

// CommandReader supplies replies to requests for command blocks.
type CommandReader interface{ CommandResults() []proto.CommandResult }

type commandBrowser struct {
	open, have                                     bool
	pane                                           string
	id                                             uint64
	result                                         proto.CommandResult
	sel                                            int
	list, preview                                  widget.List
	output                                         []string
	rows                                           []widget.Clickable
	copyCommand, copyOutput, copyBoth, docs, close widget.Clickable
	backdrop, card                                 int
	copy                                           string
	notice                                         string
}

func (u *ui) openCommandBrowser() {
	b := &u.browser
	id := b.id + 1
	*b = commandBrowser{open: true, pane: u.nav.focused(), id: id}
	b.list.Axis, b.preview.Axis = gl.Vertical, gl.Vertical
	if u.link().Level >= proto.CommandsLevel {
		u.send(proto.Commands{Pane: b.pane, ID: id})
	}
}

func (b *commandBrowser) take(r proto.CommandResult) {
	if r.Pane != b.pane || r.ID != b.id {
		return
	}
	b.result, b.have, b.sel = r, true, max(0, len(r.Blocks)-1)
	b.rows = make([]widget.Clickable, len(r.Blocks))
	b.list.Position.First = b.sel
	b.selectBlock(b.sel)
}

func (b *commandBrowser) selectBlock(i int) {
	b.sel = min(max(0, i), max(0, len(b.result.Blocks)-1))
	b.list.ScrollTo(b.sel)
	b.preview.Position = gl.Position{}
	if b.sel < len(b.result.Blocks) {
		b.output = commandOutputLines(b.result.Blocks[b.sel])
	}
	b.notice = ""
}

// Adapted from tuios (MIT): internal/input/scrollback_browser.go
func (u *ui) commandBrowserKey(gtx gl.Context, e key.Event) {
	if e.State != key.Press {
		return
	}
	b := &u.browser
	switch {
	case e.Name == key.NameEscape || e.Name == "Q":
		b.open = false
		u.nav.swallow = e.Name
	case e.Name == key.NameDownArrow || e.Name == "J":
		b.selectBlock(b.sel + 1)
	case e.Name == key.NameUpArrow || e.Name == "K":
		b.selectBlock(b.sel - 1)
	case e.Name == key.NameHome:
		b.selectBlock(0)
	case e.Name == key.NameEnd:
		b.selectBlock(len(b.result.Blocks) - 1)
	case e.Name == key.NamePageDown:
		b.preview.ScrollBy(10)
	case e.Name == key.NamePageUp:
		b.preview.ScrollBy(-10)
	case e.Name == "C":
		b.copy = "command"
	case e.Name == "Y" && e.Modifiers == key.ModShift:
		b.copy = "both"
	case e.Name == "Y":
		b.copy = "output"
	}
	gtx.Execute(op.InvalidateCmd{})
}

func (u *ui) commandBrowserStatus() string {
	b := &u.browser
	if b.notice != "" {
		return b.notice
	}
	switch {
	case u.link().Level < proto.CommandsLevel:
		return "Command blocks need a newer background service"
	case !b.have:
		return "Reading command blocks…"
	case !b.result.Integrated:
		return "This shell needs OSC 133 shell integration"
	case len(b.result.Blocks) == 0 && b.result.More:
		return "Command blocks exceed the 16 MiB browser limit"
	case len(b.result.Blocks) == 0:
		return "No commands recorded yet. Run a command with OSC 133 A, B, C and D marks"
	case b.result.More:
		return fmt.Sprintf("%d commands · older blocks exceed the 16 MiB browser limit", len(b.result.Blocks))
	default:
		return fmt.Sprintf("%d commands · snapshot of this pane", len(b.result.Blocks))
	}
}

func commandBlockMeta(b vt.CommandBlock) string {
	status := "running"
	if b.Finished {
		status = "exit unknown"
		if b.StatusKnown {
			status = fmt.Sprintf("exit %d", b.ExitCode)
		}
	}
	started := "start unknown"
	if !b.Started.IsZero() {
		started = b.Started.Local().Format("Jan 2 15:04:05")
	}
	return fmt.Sprintf("%s · %s · %d bytes", status, started, len(b.Output))
}

func (u *ui) copyCommandBlock(gtx gl.Context) {
	b := &u.browser
	if b.copy == "" {
		return
	}
	if b.sel < len(b.result.Blocks) {
		text := b.result.Blocks[b.sel].Copy(b.copy)
		if text == "" {
			b.notice = "No output to copy"
		} else {
			u.copyText(gtx, text, image.Rectangle{Max: gtx.Constraints.Max})
			b.notice = copiedText(text)
		}
	}
	b.copy = ""
	gtx.Execute(op.InvalidateCmd{})
}

func commandOutputLines(b vt.CommandBlock) []string {
	if b.Output == "" {
		return []string{"No output"}
	}
	return strings.Split(b.Output, "\n")
}

// PromptReader reports whether the pane retains OSC 133 prompt marks.
type PromptReader interface{ PromptMarks(pane string) bool }

func (u *ui) scrollPane(id string, lines, prompts int) {
	if prompts != 0 && u.link().Level >= proto.CommandsLevel {
		if shell, ok := u.b.(PromptReader); ok && !shell.PromptMarks(id) {
			u.openCommandBrowser()
			return
		}
	}
	u.send(proto.Scroll{Pane: id, Lines: lines, Prompts: prompts})
}
