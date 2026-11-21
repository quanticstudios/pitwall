package ports

import (
	"maps"
	"net"
	"os"
	"reflect"
	"runtime"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestParseNetTCP(t *testing.T) {
	v4 := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0000000000000000 100 0 0 10 0
   1: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 222 1 0000000000000000 100 0 0 10 0
   2: 0101A8C0:1F91 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 333 1 0000000000000000 100 0 0 10 0
   3: 0100007F:0BB9 0100007F:D431 01 00000000:00000000 00:00000000 00000000  1000        0 444 1 0000000000000000 100 0 0 10 0
   4: 0200007F:1389 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 555 1 0000000000000000 100 0 0 10 0
   5: 0100007F:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 0 1 0000000000000000 100 0 0 10 0
`
	v6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:1435 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 666 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000000000000:1436 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 777 1 0000000000000000 100 0 0 10 0
   2: 0000000000000000FFFF00000100007F:1437 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 888 1 0000000000000000 100 0 0 10 0
   3: 20010DB8000000000000000000000001:1438 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 999 1 0000000000000000 100 0 0 10 0
`
	got := map[string]int{}
	ParseNetTCP(v4, got)
	ParseNetTCP(v6, got)
	ParseNetTCP("", got)
	ParseNetTCP("header only\n", got)
	want := map[string]int{"111": 3000, "222": 8080, "555": 5001, "666": 5173, "777": 5174, "888": 5175}
	if !maps.Equal(got, want) {
		t.Fatalf("ParseNetTCP = %v, want %v (a LAN address, a connected socket, a zero inode and a global v6 address are left out)", got, want)
	}
}

func TestParseLsof(t *testing.T) {
	out := "p100\nn*:3000\nn127.0.0.1:3000\nn[::1]:3001\np200\nn192.168.1.5:9000\nnlocalhost:9001\np300\nn10.0.0.1:1\np400\nn[::]:4000 (LISTEN)\nnbogus\nn*:99999\n"
	got := ParseLsof(out)
	want := map[int][]int{100: {3000, 3001}, 200: {9001}, 400: {4000}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseLsof = %v, want %v", got, want)
	}
	if got := ParseLsof(""); len(got) != 0 {
		t.Fatalf("empty output = %v", got)
	}
}

// table builds a process table from pid, ppid, pgid, sid, name rows.
func table(rows ...Proc) Table {
	t := Table{}
	for _, r := range rows {
		t[r.PID] = r
	}
	return t
}

func TestFind(t *testing.T) {
	tb := table(
		Proc{1, 0, 1, 1, "systemd"},
		Proc{10, 1, 10, 10, "zsh"},      // pane A's leader: a shell
		Proc{11, 10, 11, 10, "npm"},     // its job
		Proc{12, 11, 11, 10, "node"},    // listens 3000 and 3001
		Proc{13, 1, 13, 10, "vite"},     // started in pane A, parent gone: still in its session, listens 5173
		Proc{20, 1, 20, 20, "claude"},   // pane B's leader: an agent, listens 9999 (its own: skipped)
		Proc{21, 20, 21, 20, "bash"},    // the agent's tool shell
		Proc{22, 21, 21, 20, "python3"}, // listens 8000
		Proc{30, 99, 30, 99, "claude"},  // a chat's agent process, under the daemon
		Proc{31, 30, 31, 99, "rails"},   // listens 3002
		Proc{40, 20, 40, 40, "codex"},   // a shared app-server pane B's agent started, in a session of its own
		Proc{41, 40, 41, 40, "node"},    // listens 4000: placed by cwd
		Proc{42, 40, 42, 40, "deno"},    // listens 4001: place finds no tab
		Proc{50, 1, 50, 50, "postgres"}, // nobody's, listens 5432
		Proc{60, 10, 60, 10, "sleep"},   // pane A, listens nowhere
		Proc{99, 1, 99, 99, "pitwall"},  // the daemon, listens 7777
	)
	ports := map[int][]int{12: {3000, 3001}, 13: {5173}, 20: {9999}, 22: {8000}, 31: {3002}, 41: {4000}, 42: {4001}, 50: {5432}, 99: {7777}}
	var asked []int
	listening := func(pids []int) map[int][]int {
		asked = pids
		out := map[int][]int{}
		for _, p := range pids {
			if ps, ok := ports[p]; ok {
				out[p] = ps
			}
		}
		return out
	}
	roots := []Root{{Tab: "A", PID: 10, Session: true}, {Tab: "B", PID: 20, Session: true, Agent: true}, {Tab: "C", PID: 30, Agent: true}}
	skip := func(p Proc) bool { return p.Name == "claude" || p.Name == "codex" || p.PID == 99 }
	place := func(pid int) string {
		if pid == 41 {
			return "B"
		}
		return ""
	}
	got := Find(tb, roots, []int{40}, place, skip, listening)
	want := map[string][]model.Server{
		"A": {{PID: 12, Name: "node", Ports: []int{3000, 3001}}, {PID: 13, Name: "vite", Ports: []int{5173}}},
		"B": {{PID: 41, Name: "node", Ports: []int{4000}, Agent: true}, {PID: 22, Name: "python3", Ports: []int{8000}, Agent: true}},
		"C": {{PID: 31, Name: "rails", Ports: []int{3002}, Agent: true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Find =\n%+v\nwant\n%+v", got, want)
	}
	for _, p := range []int{1, 20, 30, 40, 50, 99} {
		if slices.Contains(asked, p) {
			t.Errorf("Find asked for pid %d's sockets: an agent, the daemon, init or a stranger", p)
		}
	}
	if got := Find(tb, nil, nil, nil, skip, listening); got != nil {
		t.Errorf("no roots = %v", got)
	}
	// A root that exited leaves nothing.
	if got := Find(tb, []Root{{Tab: "Z", PID: 12345, Session: true}}, nil, nil, skip, listening); len(got) != 0 {
		t.Errorf("gone root = %v", got)
	}
}

func TestFindChatAgentInsidePane(t *testing.T) {
	// A chat's agent the pane started (its Terminal) owns what it runs,
	// even inside the pane's session.
	tb := table(Proc{10, 1, 10, 10, "zsh"}, Proc{11, 10, 11, 10, "claude"}, Proc{12, 11, 12, 10, "node"})
	listening := func([]int) map[int][]int { return map[int][]int{12: {3000}} }
	got := Find(tb, []Root{{Tab: "A", PID: 10, Session: true}, {Tab: "A", PID: 11, Agent: true}}, nil, nil, func(Proc) bool { return false }, listening)
	if s := got["A"]; len(s) != 1 || !s[0].Agent {
		t.Fatalf("Find = %+v, want node marked Agent", got)
	}
}

func TestUnderLoop(t *testing.T) {
	// A pid reused into a loop is visited once.
	kids := map[int][]int{1: {2}, 2: {3}, 3: {1, 4}}
	if got := Under(kids, 1); !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Fatalf("Under = %v", got)
	}
}

func TestGroup(t *testing.T) {
	tb := table(Proc{10, 1, 10, 10, "zsh"}, Proc{11, 10, 11, 10, "npm"}, Proc{12, 11, 11, 10, "node"}, Proc{20, 1, 20, 20, "claude"}, Proc{21, 20, 20, 20, "node"}, Proc{30, 1, 1, 1, "x"})
	keep := map[int]bool{10: true, 20: true}
	if g := Group(tb, 12, keep); g != 11 {
		t.Errorf("a job's group = %d, want 11", g)
	}
	if g := Group(tb, 21, keep); g != 0 {
		t.Errorf("a group the agent is in = %d, want 0 (signal the pid alone)", g)
	}
	if g := Group(tb, 30, keep); g != 0 {
		t.Errorf("group 1 = %d, want 0", g)
	}
	if g := Group(tb, 999, keep); g != 0 {
		t.Errorf("a gone pid = %d, want 0", g)
	}
}

// TestLive finds this test's own listener in the real process table.
func TestLive(t *testing.T) {
	if !Supported {
		t.Skip("no process table here")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	tb, err := Procs()
	if err != nil {
		t.Fatal(err)
	}
	me := tb[os.Getpid()]
	if me.PID != os.Getpid() || me.PPID != os.Getppid() || me.Name == "" {
		t.Fatalf("Procs has %+v for this process (pid %d, ppid %d)", me, os.Getpid(), os.Getppid())
	}
	got := Listening([]int{os.Getpid()})
	if !slices.Contains(got[os.Getpid()], port) {
		t.Fatalf("Listening = %v, want port %d", got, port)
	}
	if runtime.GOOS == "linux" {
		wd, _ := os.Getwd()
		if Cwd(os.Getpid()) != wd {
			t.Errorf("Cwd = %q, want %q", Cwd(os.Getpid()), wd)
		}
	}
}
