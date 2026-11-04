package logind

import (
	"errors"
	"testing"
)

func TestIdleProcessStatus(t *testing.T) {
	p := &idleProcess{done: make(chan struct{})}
	if err := p.Err(); err != nil {
		t.Fatal(err)
	}
	close(p.done)
	if err := p.Err(); err == nil {
		t.Fatal("an exited child still reports an inhibitor")
	}
	p.err = errors.New("assertion failed")
	if err := p.Err(); !errors.Is(err, p.err) {
		t.Fatalf("child error: %v", err)
	}
}
