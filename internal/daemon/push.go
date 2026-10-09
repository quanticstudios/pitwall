package daemon

import (
	"context"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// pushLoop pushes to the paired phones what a window would notify, by the
// desktop's triggers and intervals, until ctx is done. It decides here,
// not in a window, so a phone hears with no window open. changed wakes
// it through d.pushWake.
func (d *Daemon) pushLoop(ctx context.Context, p *remote.Pusher) {
	wake := make(chan struct{}, 1)
	d.mu.Lock()
	d.pushWake = wake
	h, _ := model.DecideNotifications(model.Notifications{}, d.attended(), false, "", time.Now())
	version := d.st.Version
	d.mu.Unlock()
	defer func() {
		d.mu.Lock()
		if d.pushWake == wake {
			d.pushWake = nil
		}
		d.mu.Unlock()
	}()
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-timer.C:
		}
		d.mu.Lock()
		if h.Idle() && d.st.Version == version {
			d.mu.Unlock()
			continue
		}
		version = d.st.Version
		var out []model.Activity
		h, out = model.DecideNotifications(h, d.attended(), false, "", time.Now())
		var notices []remote.Notice
		for _, a := range out {
			if w := d.workspace(a.WorkspaceID); w != nil && !w.Detached {
				notices = append(notices, remote.NoticeOf(d.st, a))
			}
		}
		d.mu.Unlock()
		for _, n := range notices {
			p.Send(ctx, n)
		}
		timer.Stop()
		if due := h.Next(); !due.IsZero() {
			timer.Reset(time.Until(due))
		}
	}
}
