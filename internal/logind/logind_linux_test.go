package logind

import (
	"context"
	"io"
	"os"
	"reflect"
	"slices"
	"sync"
	"syscall"
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

type fakeInhibitObject struct {
	dbus.BusObject
	method string
	args   []any
	fd     dbus.UnixFD
}

func (o *fakeInhibitObject) CallWithContext(_ context.Context, method string, _ dbus.Flags, args ...any) *dbus.Call {
	o.method, o.args = method, args
	return &dbus.Call{Body: []any{o.fd}}
}

func TestInhibitModes(t *testing.T) {
	for _, tc := range []struct{ what, why, mode string }{
		{"shutdown:sleep", "Save open tabs", "delay"},
		{"idle:sleep", "An agent works", "block"},
	} {
		// A temporary file stands in for the descriptor logind would send.
		f, err := os.CreateTemp(t.TempDir(), "inhibitor")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		fd, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		o := &fakeInhibitObject{fd: dbus.UnixFD(fd)}
		lock, err := (systemBus{o}).inhibitFor(context.Background(), tc.what, tc.why, tc.mode)
		if err != nil {
			syscall.Close(fd)
			t.Fatal(err)
		}
		if o.method != manager+".Inhibit" || !reflect.DeepEqual(o.args, []any{tc.what, "pitwall", tc.why, tc.mode}) {
			t.Fatalf("%s %v", o.method, o.args)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Fstat(fd, &syscall.Stat_t{}); err == nil {
			t.Fatal("closing the inhibitor left its descriptor open")
		}
	}
}
