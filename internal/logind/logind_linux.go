package logind

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/godbus/dbus/v5"
)

const (
	dest    = "org.freedesktop.login1"
	objPath = "/org/freedesktop/login1"
	manager = "org.freedesktop.login1.Manager"
)

// Watch takes a delay lock on shutdown and sleep, so logind waits for
// pitwall before either, up to its InhibitDelayMaxSec. When logind
// announces one, Watch calls prepare, shutdown true for a shutdown, and then
// lets the lock go. After a wake, or a shutdown called off, which runs
// cancelled first, it takes a new lock. Watch returns nil when ctx ends, and
// an error at once without a system bus or logind, as in a container, or
// later when the bus goes away.
func Watch(ctx context.Context, prepare func(shutdown bool), cancelled func()) error {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(objPath), dbus.WithMatchInterface(manager)); err != nil {
		return err
	}
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)
	return watch(ctx, systemBus{conn.Object(dest, objPath)}, signals, prepare, cancelled)
}

type systemBus struct{ obj dbus.BusObject }

func (b systemBus) inhibit() (io.Closer, error) {
	return b.inhibitFor(context.Background(), "shutdown:sleep", "Save open tabs", "delay")
}

func (b systemBus) inhibitFor(ctx context.Context, what, why, mode string) (io.Closer, error) {
	var fd dbus.UnixFD
	if err := b.obj.CallWithContext(ctx, manager+".Inhibit", 0, what, "pitwall", why, mode).Store(&fd); err != nil {
		return nil, err
	}
	// why: the net package receives the fd close-on-exec, so no pane started
	// later holds the lock after pitwall lets it go.
	return os.NewFile(uintptr(fd), "logind inhibitor"), nil
}

// InhibitIdle blocks sleep until the returned lock closes. Screens may lock and sleep.
func InhibitIdle(ctx context.Context) (io.Closer, error) {
	conn, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	lock, err := (systemBus{conn.Object(dest, objPath)}).inhibitFor(ctx, "sleep", "An agent works", "block")
	if err != nil {
		conn.Close()
		return nil, err
	}
	return idleInhibitor{lock, conn}, nil
}

type idleInhibitor struct {
	lock io.Closer
	bus  *dbus.Conn
}

func (i idleInhibitor) Close() error { return errors.Join(i.lock.Close(), i.bus.Close()) }

func (i idleInhibitor) Err() error {
	if !i.bus.Connected() {
		return errors.New("the system bus connection closed")
	}
	return nil
}

// inhibitor takes a delay lock; closing it lets logind go ahead.
type inhibitor interface{ inhibit() (io.Closer, error) }

// watch is Watch on a bus, which tests fake.
func watch(ctx context.Context, b inhibitor, signals <-chan *dbus.Signal, prepare func(shutdown bool), cancelled func()) error {
	lock, err := b.inhibit()
	if err != nil {
		return err
	}
	defer func() {
		if lock != nil {
			lock.Close()
		}
	}()
	for {
		var s *dbus.Signal
		select {
		case <-ctx.Done():
			return nil
		case s = <-signals:
		}
		if s == nil {
			return errors.New("the system bus connection closed")
		}
		shutdown := s.Name == manager+".PrepareForShutdown"
		if !shutdown && s.Name != manager+".PrepareForSleep" || len(s.Body) != 1 {
			continue
		}
		start, ok := s.Body[0].(bool)
		if !ok {
			continue
		}
		if start {
			prepare(shutdown)
			if lock != nil {
				lock.Close()
				lock = nil
			}
			continue
		}
		if shutdown {
			cancelled()
		}
		if lock == nil {
			if lock, err = b.inhibit(); err != nil {
				return err
			}
		}
	}
}
