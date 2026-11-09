package app

import (
	"cmp"
	"image"
	"io"
	"log"
	"strings"

	"gioui.org/io/clipboard"
	gl "gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// ClipboardHoster is optionally implemented by a Backend whose panes run
// on several machines.
type ClipboardHoster interface {
	// ClipboardHost is the host whose pane made State().Clipboard's write
	// numbered seq, "" for this machine. ok is false once a newer write
	// replaced it.
	ClipboardHost(seq uint64) (host string, ok bool)
}

// clipHost is the host of the OSC 52 write c, "" for this machine. ok is
// false for a write a newer one replaced, which the window skips.
func (u *ui) clipHost(c model.Clipboard) (string, bool) {
	if h, ok := u.b.(ClipboardHoster); ok {
		host, cur := h.ClipboardHost(c.Seq)
		return cmp.Or(host, Host), cur
	}
	return Host, true
}

// clipAsk is an OSC 52 write from a pane on host that waits for an answer.
type clipAsk struct{ host, text string }

// putClipboard puts text on this machine's clipboard.
func putClipboard(gtx gl.Context, text string) {
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(text))})
}

// askClipboard settles the waiting write by its host's answer, once one is
// remembered, else opens the clipboard dialog for it once no other dialog
// shows. An open dialog keeps the text it shows: a click there answers for
// that text, and a newer write waits for a dialog of its own.
func (u *ui) askClipboard(gtx gl.Context) {
	a := u.clipAsk
	if a.host == "" {
		return
	}
	switch u.cfg.HostClipboard[a.host] {
	case "allow":
		putClipboard(gtx, a.text)
	case "deny":
	default:
		if u.modal.kind != modalNone {
			return
		}
		u.modal.open(modalClipboard, "")
		u.modal.clip = a
	}
	u.clipAsk = clipAsk{}
}

// clipboardEvents answers the clipboard dialog. Allow writes the text and
// Deny drops it; with Remember ticked the answer goes to [host_clipboard].
// Enter does neither, so typing into a pane as the dialog opens answers
// nothing; Escape drops the text without remembering.
func (u *ui) clipboardEvents(gtx gl.Context) {
	m := &u.modal
	for m.check.Clicked(gtx) {
		m.remember = !m.remember
	}
	if allow, deny := m.ok.Clicked(gtx), m.cancel.Clicked(gtx); allow || deny {
		u.answerClipboard(gtx, allow)
	}
}

// answerClipboard closes the clipboard dialog with Allow or Deny.
func (u *ui) answerClipboard(gtx gl.Context, allow bool) {
	m := &u.modal
	if allow {
		putClipboard(gtx, m.clip.text)
	}
	if m.remember {
		u.rememberClipboard(m.clip.host, allow)
	}
	m.close()
}

// rememberClipboard keeps host's answer in config.toml's [host_clipboard],
// and in the loaded config until the window reads the file again.
func (u *ui) rememberClipboard(host string, allow bool) {
	v := "deny"
	if allow {
		v = "allow"
	}
	if u.cfg.HostClipboard == nil {
		u.cfg.HostClipboard = map[string]string{}
	}
	u.cfg.HostClipboard[host] = v
	if u.cfg.Path == "" {
		return
	}
	if err := config.SetKey(u.cfg.Path, "host_clipboard", config.Key(host), config.Quote(v)); err != nil {
		log.Printf("host clipboard: %q", err)
	}
}

// clipboardBody is the dialog that asks before a program on another host
// sets this machine's clipboard.
func (u *ui) clipboardBody(gtx gl.Context) gl.Dimensions {
	th, c := u.th, u.modal.clip
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, c.host+" wants to set your clipboard")
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, th.UIFont, th.Sp(theme.Large), th.Muted, "A program in a pane on "+c.host+" sent this text. Allow puts it on this machine's clipboard, so your next paste here pastes it.")
		}),
		gl.Rigid(gl.Spacer{Height: 8}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min = image.Point{}
			return widget.Label{MaxLines: 3}.Layout(gtx, th.Shaper, th.MonoFont, th.Sp(theme.Small), c.text, colorCall(gtx, th.Fg))
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.checkbox(gtx, &u.modal.check, u.modal.remember, "Remember for "+c.host, "Change it in Settings, Terminal, Machines.")
		}),
		gl.Rigid(gl.Spacer{Height: 24}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return u.buttons(gtx, "Deny", "Allow", kit.Primary)
		}),
	)
}
