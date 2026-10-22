package daemon

import (
	"net"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// listen opens a listener on a free port of 127.0.0.1 and returns its port.
func listen(t *testing.T) (net.Listener, int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	return ln, ln.Addr().(*net.TCPAddr).Port
}

// TestListening: a port with a listener is found, and goes once it closes.
func TestListening(t *testing.T) {
	ln, p := listen(t)
	b := model.PortBlock{First: p, Last: p}
	if got := listening(b); !slices.Equal(got, []int{p}) {
		t.Fatalf("listening = %v, want [%d]", got, p)
	}
	ln.Close()
	if got := listening(b); got != nil {
		t.Fatalf("after close, listening = %v", got)
	}
	if got := listening(model.PortBlock{}); got != nil {
		t.Fatalf("no block: %v", got)
	}
}

// TestCheckPorts: State.Listening follows the listeners, and only a change
// in the open ports sends a new state.
func TestCheckPorts(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ln, p := listen(t)
	d.mu.Lock()
	d.st.Sessions = []model.Session{{ID: "s", Name: "s"}}
	d.st.Workspaces = []model.Workspace{
		{ID: "w", SessionID: "s", Ports: model.PortBlock{First: p, Last: p}},
		{ID: "plain", SessionID: "s"},
	}
	d.mu.Unlock()
	check := func(want []int, sent bool) {
		t.Helper()
		v := d.state().Version
		d.checkPorts()
		st := d.state()
		if !slices.Equal(st.Listening["w"], want) || len(st.Listening) > 1 {
			t.Fatalf("Listening = %v, want w: %v", st.Listening, want)
		}
		if got := st.Version != v; got != sent {
			t.Fatalf("sent a state: %v, want %v", got, sent)
		}
	}
	check([]int{p}, true)
	check([]int{p}, false)
	ln.Close()
	check(nil, true)
	check(nil, false)
}
