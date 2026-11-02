package logind

import (
	"context"
	"io"
	"slices"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"
)

// fakeBus hands out locks and records what happens, in order.
type fakeBus struct {
	mu     sync.Mutex
	events []string
	held   int
}

func (b *fakeBus) note(e string) { b.mu.Lock(); b.events = append(b.events, e); b.mu.Unlock() }

func (b *fakeBus) inhibit() (io.Closer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.held++
	b.events = append(b.events, "lock")
	return closer{b}, nil
}

type closer struct{ b *fakeBus }

func (c closer) Close() error {
	c.b.mu.Lock()
	defer c.b.mu.Unlock()
	c.b.held--
	c.b.events = append(c.b.events, "unlock")
	return nil
}

func signal(name string, start bool) *dbus.Signal {
	return &dbus.Signal{Name: manager + "." + name, Body: []any{start}}
}

// TestWatch checks the lock is held until prepare returns, let go then,
// taken again after a wake or a cancelled shutdown, and released when the
// context ends; other signals change nothing.
func TestWatch(t *testing.T) {
	b := &fakeBus{}
	signals := make(chan *dbus.Signal)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() {
		done <- watch(ctx, b, signals, func(shutdown bool) {
			if shutdown {
				b.note("save for shutdown")
			} else {
				b.note("save for sleep")
			}
		}, func() { b.note("thaw") })
	}()
	signals <- &dbus.Signal{Name: manager + ".SessionNew", Body: []any{"2", dbus.ObjectPath("/s")}}
	signals <- signal("PrepareForSleep", true)
	signals <- signal("PrepareForSleep", false)
	signals <- signal("PrepareForShutdown", true)
	signals <- signal("PrepareForShutdown", false)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := []string{"lock", "save for sleep", "unlock", "lock", "save for shutdown", "unlock", "thaw", "lock", "unlock"}
	if !slices.Equal(b.events, want) || b.held != 0 {
		t.Fatalf("events %q, %d locks held; want %q", b.events, b.held, want)
	}
}

// TestWatchBusGone checks a closed bus ends Watch with an error and no lock held.
func TestWatchBusGone(t *testing.T) {
	b := &fakeBus{}
	signals := make(chan *dbus.Signal)
	close(signals)
	if err := watch(context.Background(), b, signals, func(bool) {}, func() {}); err == nil || b.held != 0 {
		t.Fatalf("watch = %v with %d locks held", err, b.held)
	}
}
