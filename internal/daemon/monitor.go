package daemon

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/quanticstudios/sector/procs"
	"github.com/quanticstudios/sector/sample"

	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// Monitor tabs: while a window shows one, the daemon samples its own
// machine with sector and sends each window a MonitorSample. With no
// watcher no goroutine runs and nothing reads /proc.

// monitorFloor is the shortest interval a MonitorWatch may ask for.
const monitorFloor = 500 * time.Millisecond

// monitorErrs keeps a failing sampler to a log line a minute.
var monitorErrs = logs.Limiter{Every: time.Minute}

// monitor is the daemon's sampler state, guarded by its own mu.
type monitor struct {
	mu       sync.Mutex
	watchers map[watchKey]time.Duration
	errSent  map[*client]string // the Err each client got last, so it gets one once
	running  bool
	wake     chan struct{} // a watch changed: the loop checks at once
}

// watchKey is one window's watch by one tab id. A window that switches
// between two tabs of this daemon may stop one after starting
// the other.
type watchKey struct {
	c  *client
	ws string
}

// sectorReader reads this machine with one sample.Sampler and one
// procs.Scanner for the daemon's life, so the first reading after a pause
// reports average rates over the pause. Only monitorLoop calls it.
func sectorReader() func() (sample.Snapshot, procs.Table, error) {
	s, p := sample.New(), procs.NewScanner()
	return func() (sample.Snapshot, procs.Table, error) {
		snap, err := s.Sample()
		if err != nil {
			return sample.Snapshot{}, procs.Table{}, err
		}
		t, err := p.Scan()
		return snap, t, err
	}
}

// watchMonitor sets c's interval for Monitor tab ws, 0 to stop, and starts
// the loop for the first watcher.
func (d *Daemon) watchMonitor(ctx context.Context, c *client, ws string, iv time.Duration) {
	m := &d.mon
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watchers == nil {
		m.watchers, m.errSent, m.wake = map[watchKey]time.Duration{}, map[*client]string{}, make(chan struct{}, 1)
	}
	k := watchKey{c, ws}
	if iv <= 0 {
		delete(m.watchers, k)
	} else {
		m.watchers[k] = max(iv, monitorFloor)
	}
	select {
	case m.wake <- struct{}{}:
	default:
	}
	if !m.running && len(m.watchers) > 0 {
		m.running = true
		go d.monitorLoop(ctx)
	}
}

// unwatchMonitor drops every watch of c, which disconnected.
func (d *Daemon) unwatchMonitor(c *client) {
	d.unwatch(func(k watchKey) bool { return k.c == c })
	d.mon.mu.Lock()
	delete(d.mon.errSent, c)
	d.mon.mu.Unlock()
}

// unwatchTab drops every watch of tab ws, which closed. A Monitor tab
// watches by its own id, the monitor in place of the panes by the id of
// the tab it shows over.
func (d *Daemon) unwatchTab(ws string) {
	d.unwatch(func(k watchKey) bool { return k.ws == ws })
}

// unwatch drops the watches drop picks and wakes the loop to check.
func (d *Daemon) unwatch(drop func(watchKey) bool) {
	m := &d.mon
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.watchers {
		if drop(k) {
			delete(m.watchers, k)
		}
	}
	if m.wake != nil {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
}

// monitorLoop samples at the shortest interval any watcher asked for,
// until the last one leaves or ctx ends.
func (d *Daemon) monitorLoop(ctx context.Context) {
	m := &d.mon
	for {
		m.mu.Lock()
		iv := time.Duration(0)
		for _, w := range m.watchers {
			if iv == 0 || w < iv {
				iv = w
			}
		}
		if iv == 0 || ctx.Err() != nil {
			m.running = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()

		d.sendMonitor(d.readMonitor())

		select {
		case <-ctx.Done():
		case <-time.After(iv):
		case <-m.wake:
		}
	}
}

// readMonitor reads the machine, outside d.mu, and maps agent groups to
// the panes they run in.
func (d *Daemon) readMonitor() proto.MonitorSample {
	read := d.o.Monitor
	if read == nil {
		return proto.MonitorSample{Err: "this daemon has no machine reader"}
	}
	snap, table, err := read()
	if err != nil {
		if ok, held := monitorErrs.Allow("monitor", time.Now()); ok {
			log.Printf("monitor: %q%s", err, heldNote(held))
		}
		return proto.MonitorSample{Err: err.Error()}
	}
	return proto.MonitorSample{Snapshot: snap, Table: table, Tabs: d.agentPanes(table)}
}

// agentPanes maps the Key of each agent group in t to the pane whose
// session its agent is in. An agent started outside pitwall matches none.
func (d *Daemon) agentPanes(t procs.Table) map[string]string {
	d.mu.Lock()
	bySid := map[int]string{}
	for id, det := range d.live.det {
		if det.sid > 0 {
			bySid[det.sid] = id
		}
	}
	d.mu.Unlock()
	tabs := map[string]string{}
	for _, g := range t.Groups {
		if g.Kind != procs.Agent || g.PID <= 0 {
			continue
		}
		if id, ok := bySid[sessionOf(g.PID)]; ok {
			tabs[g.Key] = id
		}
	}
	return tabs
}

// sendMonitor puts s in each watching client's slot. A client gets an Err
// once, until another one or a reading comes.
func (d *Daemon) sendMonitor(s proto.MonitorSample) {
	m := &d.mon
	m.mu.Lock()
	to := map[*client]bool{}
	for k := range m.watchers {
		if m.errSent[k.c] == s.Err && s.Err != "" {
			continue
		}
		to[k.c] = true
		m.errSent[k.c] = s.Err
	}
	m.mu.Unlock()
	for c := range to {
		c.push(func() { c.sample = &s })
	}
}
