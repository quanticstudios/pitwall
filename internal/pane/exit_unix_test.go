//go:build linux || darwin

package pane

import (
	"errors"
	"testing"
	"time"
)

type failedExit struct{}

func (failedExit) Wait() (int, error) { return -1, errors.New("watch failed") }

func TestWatcherFailureFallsBackToEOF(t *testing.T) {
	p := &Pane{sys: sys{waiter: failedExit{}}, readDone: make(chan struct{})}
	done := make(chan int, 1)
	go func() { done <- p.reap() }()
	select {
	case <-done:
		t.Fatal("watch failure reported exit before PTY EOF")
	case <-time.After(20 * time.Millisecond):
	}
	close(p.readDone)
	select {
	case code := <-done:
		if code != -1 {
			t.Fatalf("EOF status %d, want unknown", code)
		}
	case <-time.After(time.Second):
		t.Fatal("PTY EOF did not report exit after watch failure")
	}
}
