package panel

import (
	"image"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestServerRows(t *testing.T) {
	rows := serverRows([]model.Server{
		{PID: 7, Name: "node", Ports: []int{5173, 3000}},
		{PID: 9, Name: "python3", Ports: []int{4000}, Agent: true},
	})
	var got []string
	for _, r := range rows {
		got = append(got, r.key()+" "+serverWho(r))
	}
	want := []string{"7:5173 Terminal · node · pid 7", "7:3000 Terminal · node · pid 7", "9:4000 Agent · python3 · pid 9"}
	// By port.
	want = []string{want[1], want[2], want[0]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows %q, want %q", got, want)
	}
	if serverWho(serverPort{model.Server{PID: 3}, 1}) != "Terminal · pid 3" {
		t.Fatal("a nameless process")
	}
}

// serversFrame lays tr out once at 1200x800 dp with snapshot s.
func serversFrame(tr *Transcript, s *session.Snapshot, r *input.Router, now time.Time) {
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1200, 800)), Now: now, Source: r.Source()}
	tr.Layout(gtx, theme.Dark(), s, false)
	r.Frame(&ops)
}

// TestServersStopFlow walks a server's Stop: Stop sends SIGTERM, a server
// that outlives it offers Force stop, which asks, and Kill sends SIGKILL;
// Cancel and an error go back to Stop.
func TestServersStopFlow(t *testing.T) {
	var tr Transcript
	tr.init()
	tr.Servers.List = []model.Server{{PID: 42, Name: "node", Ports: []int{3000}}}
	s := &session.Snapshot{}
	var r input.Router
	now := time.Now()
	frame := func() {
		now = now.Add(time.Millisecond)
		serversFrame(&tr, s, &r, now)
	}
	if tr.ServersOpen() {
		t.Fatal("open before ShowServers")
	}
	tr.ShowServers()
	if !tr.ServersOpen() || !tr.FilesOpen() {
		t.Fatal("ShowServers did not open the view")
	}
	frame()
	click := func(id string) {
		t.Helper()
		c := tr.clicks[id]
		if c == nil {
			t.Fatalf("no button %s on screen", id)
		}
		c.Click()
		frame()
	}
	click("srv:open:42:3000")
	if got := tr.ServerActions(); !reflect.DeepEqual(got, []ServerAction{{Open: "http://localhost:3000"}}) {
		t.Fatalf("open: %+v", got)
	}
	click("srv:stop:42:3000")
	if got := tr.ServerActions(); !reflect.DeepEqual(got, []ServerAction{{PID: 42, Port: 3000}}) {
		t.Fatalf("stop: %+v", got)
	}
	click("srv:stop:42:3000") // a second click while it stops sends nothing
	if got := tr.ServerActions(); len(got) != 0 {
		t.Fatalf("stop twice: %+v", got)
	}
	tr.ServerStopped(42, 3000, false, true, "")
	frame()
	click("srv:force:42:3000")
	if tr.ServerActions() != nil {
		t.Fatal("Force stop killed without asking")
	}
	click("srv:cancel:42:3000")
	click("srv:force:42:3000")
	click("srv:kill:42:3000")
	if got := tr.ServerActions(); !reflect.DeepEqual(got, []ServerAction{{PID: 42, Port: 3000, Force: true}}) {
		t.Fatalf("kill: %+v", got)
	}
	tr.ServerStopped(42, 3000, true, false, "")
	if r := tr.side.srv.rows["42:3000"]; r.phase != srvIdle || r.err != "" {
		t.Fatalf("after kill: %+v", r)
	}
	tr.ServerStopped(42, 3000, false, false, "permission denied")
	if r := tr.side.srv.rows["42:3000"]; r.phase != srvIdle || r.err != "permission denied" {
		t.Fatalf("error: %+v", r)
	}
	// A server that goes takes its row state along.
	tr.Servers.List = nil
	frame()
	if len(tr.side.srv.rows) != 0 {
		t.Fatalf("rows of gone servers: %v", tr.side.srv.rows)
	}
	tr.ShowServers()
	if tr.FilesOpen() {
		t.Fatal("ShowServers did not close the open view")
	}
}

// TestServersRemote: servers on another machine have no Open, whose
// localhost is not this one's.
func TestServersRemote(t *testing.T) {
	var tr Transcript
	tr.init()
	tr.Servers = ServerList{List: []model.Server{{PID: 1, Name: "x", Ports: []int{80}}}, Remote: true}
	tr.ShowServers()
	var r input.Router
	now := time.Now()
	serversFrame(&tr, &session.Snapshot{}, &r, now)
	tr.click("srv:open:1:80").Click()
	serversFrame(&tr, &session.Snapshot{}, &r, now.Add(time.Millisecond))
	if got := tr.ServerActions(); len(got) != 0 {
		t.Fatalf("a remote server opened here: %+v", got)
	}
}

// TestServerShots writes PNGs of the Servers view to $PITWALL_TOOL_SHOTS.
func TestServerShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TOOL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TOOL_SHOTS is not set")
	}
	s := toolChat()
	list := []model.Server{
		{PID: 48213, Name: "node", Ports: []int{3000, 3001}, Agent: true},
		{PID: 48377, Name: "vite", Ports: []int{5173}},
		{PID: 51022, Name: "python3", Ports: []int{8000}, Agent: true},
	}
	tr := &Transcript{}
	tr.init()
	tr.Servers.List = list
	tr.ShowServers()
	shoot(t, tr, s, filepath.Join(dir, "servers-panel.png"), image.Pt(1400, 760), 1.5)

	tr = &Transcript{}
	tr.init()
	tr.Servers.List = list
	tr.ShowServers()
	tr.side.srv.row("48377:5173").phase = srvStubborn
	tr.side.srv.row("51022:8000").phase = srvConfirm
	tr.side.srv.row("48213:3001").phase = srvStopping
	shoot(t, tr, s, filepath.Join(dir, "servers-stop.png"), image.Pt(1400, 760), 1.5)

	tr = &Transcript{}
	tr.init()
	tr.ShowServers()
	shoot(t, tr, s, filepath.Join(dir, "servers-empty.png"), image.Pt(1400, 500), 1.5)
}
