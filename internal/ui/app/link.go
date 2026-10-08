package app

import (
	"errors"
	"image/color"
	"log"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// LinkState is the state of the window's daemon connection.
type LinkState int

const (
	LinkUp   LinkState = iota
	LinkDown           // lost; the backend tries again with backoff
	// LinkRestart: the daemon speaks another proto.Version. The window asks
	// before restarting it, since that stops every program in a pane.
	LinkRestart
	// LinkStale: a newer pitwall restarted the daemon; the window reopens
	// itself on the binary installed now.
	LinkStale
)

// Link is a Linker's connection state.
type Link struct {
	State LinkState
	Note  string // the dialog's second line: progress, or why a try failed
	Epoch int    // counts connections made; a new one gets sizes and the session again
	Level int    // the daemon's proto.Level
}

// Linker is optionally implemented by a Backend whose daemon connection can
// be lost or refused.
type Linker interface {
	Link() Link
	Reconnect() // try again now
	Restart()   // LinkRestart: replace the daemon with this version's
}

// link is the backend's connection state, LinkUp without a Linker.
func (u *ui) link() Link {
	if l, ok := u.b.(Linker); ok {
		return l.Link()
	}
	return Link{}
}

// syncLink starts over on a new connection, which may be to a new daemon:
// every pane's size and the shown session are sent again.
func (u *ui) syncLink(l Link) {
	if l.Epoch == u.epoch {
		return
	}
	u.epoch = l.Epoch
	for _, p := range u.panes {
		p.sentCols, p.sentRows = 0, 0
	}
	u.showSent = ""
}

// layoutLink draws the dialog for a connection that is not up, over the
// whole window, and runs its buttons.
func (u *ui) layoutLink(gtx gl.Context, st *model.State, l Link) {
	lk, _ := u.b.(Linker)
	if l.State == LinkUp || lk == nil {
		return
	}
	if l.State == LinkStale && !u.relaunchTried {
		u.relaunchTried = true
		name := ""
		if s := st.Session(u.nav.session); s != nil {
			name = s.Name
		}
		err := errors.New("no session to reopen on")
		if Relaunch != nil && name != "" {
			err = Relaunch(name, u.nav.workspace)
		}
		if err == nil {
			log.Printf("the daemon is newer than this window; reopened it")
			u.relaunched = true
			return
		}
		log.Printf("reopen on a newer daemon: %q; asking to restart it instead", err)
	}
	for u.linkOK.Clicked(gtx) {
		if l.State == LinkDown {
			lk.Reconnect()
		} else {
			lk.Restart()
		}
	}
	for u.linkLater.Clicked(gtx) {
		u.quit = true
	}

	th := u.th
	title, body, ok, later := "Disconnected from pitwall's background service.", "", "Reconnect", ""
	okBg, okFg := th.Primary, th.OnPrimary
	if l.State != LinkDown {
		v := "pitwall"
		if Version != "" {
			v += " " + Version
		}
		title = v + " needs to restart its background service; programs running in panes will stop."
		body = "Agents resume after the restart. Later closes this window and leaves everything running."
		ok, later = "Restart now", "Later"
		okBg, okFg = th.Red, theme.Hex("#ffffff")
	}

	// Like the modal: a dimmed window that takes every press, and a card.
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, color.NRGBA{A: 0xc8}, clip.Rect{Max: size}.Op())
	bg := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &u.linkBackdrop)
	bg.Pop()
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &u.linkBackdrop, Kinds: pointer.Press}); !ok {
			break
		}
	}
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return para(gtx, th, semibold(th.UIFont), 16, th.Fg, title)
		}),
	}
	for _, line := range []struct {
		text string
		c    color.NRGBA
	}{{body, th.Muted}, {l.Note, th.Muted}} {
		if line.text != "" {
			kids = append(kids, gl.Rigid(gl.Spacer{Height: 8}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return para(gtx, th, th.UIFont, 14, line.c, line.text)
			}))
		}
	}
	kids = append(kids, gl.Rigid(gl.Spacer{Height: 20}.Layout), gl.Rigid(func(gtx gl.Context) gl.Dimensions {
		return u.buttonPair(gtx, &u.linkLater, &u.linkOK, later, ok, okBg, okFg)
	}))
	u.card(gtx, &u.linkBackdrop, func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
	})
}
