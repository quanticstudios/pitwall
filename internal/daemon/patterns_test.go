package daemon

import (
	"context"
	"testing"
	"time"
)

func TestScreenPatternUpdaterLifetime(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	_, _, stop := runDaemon(t, &fakes{}, func(o *Options) {
		o.UpdateScreenPatterns = func(ctx context.Context) {
			close(started)
			<-ctx.Done()
			close(stopped)
		}
	})
	defer stop()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not start the pattern updater")
	}
	// runDaemon's stop cancels Serve and waits for its goroutines.
	t.Cleanup(func() {
		select {
		case <-stopped:
		default:
			t.Error("Serve left the pattern updater running")
		}
	})
}
