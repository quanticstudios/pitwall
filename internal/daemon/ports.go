package daemon

import (
	"maps"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// portPoll is how often the port blocks of worktree tabs are probed; tests
// shorten it.
var portPoll = 3 * time.Second

// listening is the ports of b that accept a TCP connection on 127.0.0.1 or
// ::1, ascending. Each connection is closed at once. A dev server bound to
// "localhost" can be on either address.
func listening(b model.PortBlock) []int {
	var out []int
	for p := b.First; b.First != 0 && p <= b.Last; p++ {
		for _, host := range []string{"127.0.0.1", "::1"} {
			c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(p)), 200*time.Millisecond)
			if err == nil {
				c.Close()
				out = append(out, p)
				break
			}
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
	for id, b := range blocks {
		if l := listening(b); l != nil {
			got[id] = l
		}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	maps.DeleteFunc(got, func(id string, _ []int) bool { return d.workspace(id) == nil }) // closed while probed
	if maps.EqualFunc(got, d.st.Listening, slices.Equal) {
		return
	}
	d.st.Listening = got
	d.changed()
}
