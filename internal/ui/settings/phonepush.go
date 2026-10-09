package settings

import (
	"context"
	"sync"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/remote"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// pushTest is the Phone page's Send test: one at a time, its result
// written by the goroutine that sends it.
type pushTest struct {
	mu     sync.Mutex
	device string // the device of the last test
	busy   bool
	result string
	ok     bool
}

// sendTest pushes a test notice to device id.
func (p *Page) sendTest(id string) {
	t := &p.ph.test
	t.mu.Lock()
	if t.busy {
		t.mu.Unlock()
		return
	}
	t.device, t.busy, t.result = id, true, ""
	t.mu.Unlock()
	dir := p.phoneDir()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := (&remote.Pusher{Dir: dir}).Test(ctx, id)
		t.mu.Lock()
		defer t.mu.Unlock()
		t.busy, t.ok, t.result = false, err == nil, "Sent. It shows on the phone within a few seconds."
		if err != nil {
			t.result = err.Error()
		}
	}()
}

// pushRow is device d's Push notifications row: on or off, and Send test.
func (p *Page) pushRow(d remote.Device, btn func(id, label string, kind btnKind, click func()) gl.Widget) row {
	th := p.th
	r := row{label: "Push notifications", extra: "push notification notify alert test " + d.Name}
	if !remote.Subscribed(p.phoneDir(), d.ID) {
		r.desc = "Off for " + d.Name + ". To turn them on, open the page on the phone and tap Turn on. On an iPhone, first add the page to the Home Screen and open it from there."
		r.control = func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, th.UIFont, p.th.Sp(theme.Body), th.Muted, "Off")
		}
		return r
	}
	r.desc = "On for " + d.Name + ": approvals, questions, errors and finished turns you have not seen reach it, even with every window closed."
	r.control = btn("rpush:"+d.ID, "Send test", secondary, func() { p.sendTest(d.ID) })
	t := &p.ph.test
	t.mu.Lock()
	busy, result, ok := t.busy, t.result, t.ok
	mine := t.device == d.ID
	t.mu.Unlock()
	if !mine || !busy && result == "" {
		return r
	}
	r.below = func(gtx gl.Context) gl.Dimensions {
		col, text := th.Red, result
		switch {
		case busy:
			gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(100 * time.Millisecond)})
			col, text = th.Muted, "Sending…"
		case ok:
			col = th.Green
		}
		return p.para(gtx, th.UIFont, p.th.Sp(theme.Small), col, text)
	}
	return r
}
