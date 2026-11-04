package logind

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

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
		{"sleep", "An agent works", "block"},
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

func TestInhibitIdleCancelledConnect(t *testing.T) {
	dir, err := os.MkdirTemp("", "pwbus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "bus")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", "unix:path="+path)
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); accepted <- c }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		lock, err := InhibitIdle(ctx)
		if lock != nil {
			lock.Close()
		}
		done <- err
	}()
	select {
	case c := <-accepted:
		if c == nil {
			t.Fatal("bus did not accept")
		}
		t.Cleanup(func() { c.Close() })
	case <-time.After(time.Second):
		t.Fatal("inhibitor did not connect to the fake bus")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled authentication succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("inhibitor stayed in authentication after cancellation")
	}
}

type fakeLoginManager struct {
	args chan []string
	fd   dbus.UnixFD
}

func (m *fakeLoginManager) Inhibit(what, who, why, mode string) (dbus.UnixFD, *dbus.Error) {
	m.args <- []string{what, who, why, mode}
	return m.fd, nil
}

func TestInhibitIdleRequest(t *testing.T) {
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon is unavailable")
	}
	dir, err := os.MkdirTemp("", "pwbus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cmd := exec.Command(bin, "--session", "--nofork", "--address=unix:path="+filepath.Join(dir, "bus"), "--print-address=1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	scan := bufio.NewScanner(out)
	if !scan.Scan() {
		t.Fatalf("private bus address: %v", scan.Err())
	}
	address := scan.Text()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dbus.Connect(address, dbus.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	f, err := os.CreateTemp(dir, "inhibitor")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m := &fakeLoginManager{args: make(chan []string, 1), fd: dbus.UnixFD(f.Fd())}
	if err := conn.Export(m, objPath, manager); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(dest, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("own fake login manager: %v, %v", reply, err)
	}
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", address)
	lock, err := InhibitIdle(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if args := <-m.args; !slices.Equal(args, []string{"sleep", "pitwall", "An agent works", "block"}) {
		t.Fatalf("InhibitIdle request: %v", args)
	}
	fd := lock.(idleInhibitor).lock.(*os.File).Fd()
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFD, 0)
	if errno != 0 || flags&syscall.FD_CLOEXEC == 0 {
		t.Fatalf("inhibitor fd flags %#x, error %v; want close-on-exec", flags, errno)
	}
}
