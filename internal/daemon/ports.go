package daemon

import (
	"maps"
	"net"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// portPoll is how often the port blocks of worktree tabs are probed; tests
// shorten it.
var portPoll = 3 * time.Second

// probeDial connects for a probe; tests swap it.
var probeDial = net.DialTimeout

// listening is the ports of b that accept a TCP connection on 127.0.0.1 or
// ::1, ascending. A dev server bound to "localhost" can be on either
// address. Every port and address is dialed at once, so a loopback that
// drops instead of refusing costs one timeout, not one per port.
func listening(b model.PortBlock) []int {
	if b.First == 0 {
		return nil
	}
	open := make([]atomic.Bool, b.Last-b.First+1)
	var wg sync.WaitGroup
	for i := range open {
		for _, host := range []string{"127.0.0.1", "::1"} {
			wg.Go(func() {
				if c, err := probeDial("tcp", net.JoinHostPort(host, strconv.Itoa(b.First+i)), 200*time.Millisecond); err == nil {
					c.Close()
					open[i].Store(true)
				}
			})
		}
	}
	wg.Wait()
	var out []int
	for i := range open {
		if open[i].Load() {
			out = append(out, b.First+i)
		}
	}
	return out
}

// checkPorts sets State.Listening from a probe of every tab's port block,
// and sends a change only when some tab's open ports did change.
func (d *Daemon) checkPorts() {
	d.mu.Lock()
	blocks := map[string]model.PortBlock{}
	for _, w := range d.st.Workspaces {
		if w.Ports.First != 0 {
			blocks[w.ID] = w.Ports
		}
	}
	d.mu.Unlock()

	got := map[string][]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for id, b := range blocks {
		wg.Go(func() {
			if l := listening(b); l != nil {
				mu.Lock()
				got[id] = l
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	d.mu.Lock()
	defer d.mu.Unlock()
	maps.DeleteFunc(got, func(id string, _ []int) bool { return d.workspace(id) == nil }) // closed while probed
	if maps.EqualFunc(got, d.st.Listening, slices.Equal) {
		return
	}
	d.st.Listening = got
	d.changed()
}
