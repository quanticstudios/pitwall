package app

import (
	"errors"
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// linked is the fake with a connection whose Link the test sets.
type linked struct {
	*FakeBackend
	link  Link
	shows int
}

func (l *linked) Link() Link { return l.link }
func (l *linked) Reconnect() {}
func (l *linked) Restart()   {}

func (l *linked) Send(msg any) error {
	if _, ok := msg.(proto.SessionShow); ok {
		l.shows++
	}
	return l.FakeBackend.Send(msg)
}

// A new connection gets the shown session and every pane's size again, and
// a window older than the daemon reopens itself once, on the session and
// tab it shows.
func TestLinkChanges(t *testing.T) {
	b := &linked{FakeBackend: NewFakeBackend(), link: Link{Epoch: 1}}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	frame()
	frame()
	if b.shows != 1 || len(u.panes) == 0 {
		t.Fatalf("%d SessionShows, %d panes after the first frames", b.shows, len(u.panes))
	}
	for _, p := range u.panes {
		if p.sentCols == 0 {
			t.Fatal("a pane's size was not sent")
		}
	}
	b.link = Link{Epoch: 2}
	frame()
	if b.shows != 2 {
		t.Fatalf("%d SessionShows, want one more on a new connection", b.shows)
	}
	for id, p := range u.panes {
		if p.sentCols != 0 { // the fake's frames fit already; a new daemon's would not
			t.Fatalf("pane %s keeps the size sent on the old connection", id)
		}
	}

	var reopened []string
	Relaunch = func(session, ws string) error {
		reopened = append(reopened, session+"/"+ws)
		return errors.New("no display")
	}
	t.Cleanup(func() { Relaunch = nil })
	b.link.State = LinkStale
	frame()
	frame()
	st := b.State()
	want := st.Session(u.nav.session).Name + "/" + u.nav.workspace
	if len(reopened) != 1 || reopened[0] != want || u.relaunched {
		t.Fatalf("reopened %v, relaunched %v; want one failed try on %s", reopened, u.relaunched, want)
	}
	Relaunch = func(string, string) error { return nil }
	u.relaunchTried = false
	frame()
	if !u.relaunched {
		t.Fatal("the window did not close after reopening")
	}
}
