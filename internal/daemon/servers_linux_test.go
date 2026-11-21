package daemon

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestHelperListen is the listener TestServersLive runs as a child
// process; it does nothing in a normal test run.
func TestHelperListen(t *testing.T) {
	if os.Getenv("PITWALL_TEST_LISTEN") != "1" {
		t.Skip("helper process")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	fmt.Println(ln.Addr().(*net.TCPAddr).Port)
	time.Sleep(time.Minute)
	os.Exit(0)
}

// TestServersLive finds a real listener in a real session through /proc,
// as a pane's, and stops it with a real SIGTERM.
func TestServersLive(t *testing.T) {
	c := exec.Command(os.Args[0], "-test.run=^TestHelperListen$")
	c.Env = append(os.Environ(), "PITWALL_TEST_LISTEN=1")
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Process.Kill(); c.Wait() })
	var port int
	if _, err := fmt.Fscan(out, &port); err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, out)
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	d.mu.Lock()
	d.st.Sessions = []model.Session{{ID: "s", Name: "s"}}
	d.st.Workspaces = []model.Workspace{{ID: "w", SessionID: "s"}}
	d.st.Panes = []model.Pane{{ID: "p", WorkspaceID: "w"}}
	d.panes = map[string]Pane{"p": fgPane{fg: c.Process.Pid}}
	d.mu.Unlock()
	d.checkServers()
	got := d.state().Servers["w"]
	if len(got) != 1 || got[0].PID != c.Process.Pid || !slices.Equal(got[0].Ports, []int{port}) || got[0].Name == "" {
		t.Fatalf("Servers = %+v, want pid %d on %d", got, c.Process.Pid, port)
	}
	r := d.serverStop(context.Background(), proto.ServerStop{Workspace: "w", PID: c.Process.Pid, Port: port})
	if r.Err != "" || r.Running {
		t.Fatalf("stop: %+v", r)
	}
	if err := c.Wait(); err == nil {
		t.Fatal("the listener exited by itself, not by the signal")
	}
	if len(d.state().Servers) != 0 {
		t.Fatalf("still listed: %+v", d.state().Servers)
	}
}
