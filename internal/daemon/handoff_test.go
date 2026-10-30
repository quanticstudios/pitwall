//go:build linux || darwin

package daemon

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// The panes every handoff test runs, by id. $D is the test's directory.
var handoffPanes = []struct{ id, script string }{
	{"sh0", ""}, {"sh1", ""}, {"sh2", ""}, {"sh3", ""},
	{"echo0", `stty -echo; echo ready; while IFS= read -r l; do echo "out:$l"; done`},
	{"echo1", `stty -echo; echo ready; while IFS= read -r l; do echo "out:$l"; done`},
	// A dev server: output that never stops.
	{"stream", `echo ready; i=0; while :; do i=$((i+1)); echo "s$i"; sleep 0.05; done`},
	// Output written while the old daemon is frozen: its hook opens the fifo.
	// It writes more than the PTY holds, so it also blocks until a daemon
	// reads again.
	{"burst", `echo ready; cat "$D/burst" >/dev/null; i=0; while [ $i -lt 2000 ]; do i=$((i+1)); echo "n$i ` + strings.Repeat("x", 40) + `"; done; echo burst-end; exec cat >/dev/null`},
	// Input queued behind a program that does not read it yet.
	{"input", `stty raw -echo; echo ready; cat "$D/input" >/dev/null; exec cat >"$D/input.out"`},
	// An agent TUI stopped in the middle of an escape sequence.
	{"tui", `stty -echo; printf '\033[?1049h\033[3;5Hagent-tui ready\033[1;3'; cat "$D/tui" >/dev/null; printf '1mBOLD'; exec cat >/dev/null`},
}

// TestHandoffHelper is a daemon process for TestHandoff: the old one
// serving the panes above, or the new one taking them over.
func TestHandoffHelper(t *testing.T) {
	role, dir := os.Getenv("PITWALL_HANDOFF_ROLE"), os.Getenv("PITWALL_HANDOFF_DIR")
	if role == "" {
		return
	}
	// FAULT is a step, which fails; with "!" the process dies there, and
	// with "~" it starts shutting down there and carries on.
	fault := os.Getenv("PITWALL_HANDOFF_FAULT")
	want := strings.TrimRight(fault, "!~")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	go func() { <-signals; cancel() }()
	var ln net.Listener
	handoffFault = func(step string) error {
		if step == "old:freeze" {
			f, err := os.OpenFile(filepath.Join(dir, "burst"), os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			f.Close()
		}
		switch {
		case step != want:
			return nil
		case strings.HasSuffix(fault, "!"):
			os.Exit(3)
		case strings.HasSuffix(fault, "~"):
			cancel()
			for { // until Serve's shutdown closed the listener
				f, err := ln.(*net.UnixListener).File()
				if err != nil {
					return nil
				}
				f.Close()
				time.Sleep(time.Millisecond)
			}
		}
		return errors.New("injected")
	}
	sock := filepath.Join(dir, "d.sock")
	o := (&fakes{statsCalls: map[string]int{}}).options()
	o.StartPane = func(c pane.Config) (Pane, error) {
		c.Env = append(c.Env, "D="+dir)
		p, err := pane.Start(c)
		if err != nil {
			return nil, err
		}
		return p, nil
	}
	o.NewVT = vt.New
	o.Scrollback = func() int { return 5000 }
	o.Load = func() (model.State, error) { return handoffState(), nil }
	var d *Daemon
	var err error
	if role == "old" {
		if d, err = NewWith(o); err == nil {
			ln, err = net.Listen("unix", sock)
		}
	} else {
		d, ln, err = Takeover(sock, o)
	}
	if err == nil {
		fmt.Println("serving")
		err = d.Serve(ctx, ln)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// handoffState is a session with a tab per pane above, and a held tab
// whose command exited before.
func handoffState() model.State {
	st := model.State{Sessions: []model.Session{{ID: "s", Name: "main"}}}
	add := func(id string, p model.Pane) {
		st.Workspaces = append(st.Workspaces, model.Workspace{ID: "w" + id, SessionID: "s", Name: id, Path: os.TempDir(),
			ActiveTab: "t" + id, Tabs: []model.Tab{{ID: "t" + id, Layout: layout.Leaf(id)}}})
		p.ID, p.WorkspaceID, p.Cwd = id, "w"+id, os.TempDir()
		st.Panes = append(st.Panes, p)
	}
	for _, hp := range handoffPanes {
		cmd := []string{"sh"}
		if hp.script != "" {
			cmd = []string{"sh", "-c", hp.script}
		}
		add(hp.id, model.Pane{Cmd: cmd})
	}
	add("held", model.Pane{Cmd: []string{"make"}, Held: true, Exited: true, ExitCode: 5})
	return st
}

// daemonProc is a helper daemon process.
type daemonProc struct {
	cmd   *exec.Cmd
	lines chan string
	exit  chan error
	err   bytes.Buffer
}

func startDaemonProc(t *testing.T, dir, role, fault string) *daemonProc {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHandoffHelper$")
	cmd.Env = append(os.Environ(), "PITWALL_HANDOFF_ROLE="+role, "PITWALL_HANDOFF_DIR="+dir, "PITWALL_HANDOFF_FAULT="+fault)
	d := &daemonProc{cmd: cmd, lines: make(chan string, 16), exit: make(chan error, 1)}
	cmd.Stderr = &d.err
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		s := bufio.NewScanner(out)
		for s.Scan() {
			d.lines <- s.Text()
		}
		close(d.lines)
		d.exit <- cmd.Wait()
	}()
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-d.exit:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
		}
	})
	return d
}

// serving waits for the helper to serve, or reports its error.
func (d *daemonProc) serving(t *testing.T) error {
	t.Helper()
	timeout := time.After(15 * time.Second)
	for {
		select {
		case l, ok := <-d.lines:
			if !ok {
				err := <-d.exit
				d.exit <- err
				return fmt.Errorf("%v: %s", err, d.err.String())
			}
			if l == "serving" {
				return nil
			}
		case <-timeout:
			t.Fatal("daemon process neither served nor exited")
		}
	}
}

func (d *daemonProc) waitExit(t *testing.T) error {
	t.Helper()
	select {
	case err := <-d.exit:
		d.exit <- err
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("daemon process did not exit")
		return nil
	}
}

// paneText is all of a pane's lines, scrollback included.
func paneText(c *testClient, id string) string {
	c.send(proto.Text{Pane: id, Sel: vt.Selection{B: vt.Pos{Line: 100_000}, Mode: vt.SelectLines}})
	m := c.waitFor("text of "+id, func(m any) bool { r, ok := m.(proto.TextResult); return ok && r.Pane == id })
	return m.(proto.TextResult).Text
}

func awaitText(t *testing.T, c *testClient, id, what string, ok func(string) bool) string {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		s := paneText(c, id)
		if ok(s) {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s never showed %s:\n%s", id, what, s)
		}
	}
}

func awaitContains(t *testing.T, c *testClient, id, want string) string {
	t.Helper()
	return awaitText(t, c, id, strconv.Quote(want), func(s string) bool { return strings.Contains(s, want) })
}

func typeIn(c *testClient, id, s string) { c.send(proto.Input{Pane: id, Data: []byte(s)}) }

// trigger opens and closes the fifo a pane's cat waits on.
func trigger(t *testing.T, dir, name string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

var streamLine = regexp.MustCompile(`(?m)^s(\d+)$`)

// streamed is the stream pane's numbers, failing on a gap or a repeat.
func streamed(t *testing.T, text string) []int {
	t.Helper()
	var ns []int
	for _, m := range streamLine.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		if len(ns) > 0 && n != ns[len(ns)-1]+1 {
			t.Fatalf("stream jumps from %d to %d", ns[len(ns)-1], n)
		}
		ns = append(ns, n)
	}
	return ns
}

// A new daemon takes over ten running panes and a held one from the old
// daemon, or, when a step fails or the new daemon dies, the old one keeps
// serving them. Either way no output or input is lost.
func TestHandoff(t *testing.T) {
	if testing.Short() {
		t.Skip("starts daemon processes")
	}
	for _, fault := range []string{"", "old:freeze", "old:send", "old:ready", "new:receive", "new:prepare", "new:ready", "new:commit", "new:ready!", "new:commit!", "old:ready~"} {
		name := fault
		if name == "" {
			name = "ok"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testHandoff(t, fault)
		})
	}
}

func testHandoff(t *testing.T, fault string) {
	// why: t.TempDir holds the test name, which can push the socket path
	// past sockaddr_un's 104 bytes on macOS.
	dir, err := os.MkdirTemp("", "pwh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	for _, f := range []string{"burst", "input", "tui"} {
		if err := syscall.Mkfifo(filepath.Join(dir, f), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(dir, "d.sock")
	old := startDaemonProc(t, dir, "old", fault)
	if err := old.serving(t); err != nil {
		t.Fatal(err)
	}
	c := dial(t, sock, "cli")
	for _, hp := range handoffPanes {
		if hp.script != "" {
			awaitContains(t, c, hp.id, "ready")
		}
	}
	for i := range 4 {
		typeIn(c, fmt.Sprintf("sh%d", i), fmt.Sprintf("echo before-$((%d+1))\n", 40+i))
	}
	typeIn(c, "echo0", "a1\n")
	typeIn(c, "echo1", "a1\n")
	// More input than the PTY holds, to a program not reading yet: the
	// writer blocks inside a message with more queued behind it.
	var sent bytes.Buffer
	for i := range 40 {
		var msg bytes.Buffer
		for j := 0; msg.Len() < 4000; j++ {
			fmt.Fprintf(&msg, "m%02d-%03d\r\n", i, j)
		}
		sent.Write(msg.Bytes())
		typeIn(c, "input", msg.String())
	}
	for i := range 4 {
		awaitContains(t, c, fmt.Sprintf("sh%d", i), fmt.Sprintf("before-%d", 41+i))
	}
	awaitContains(t, c, "echo0", "out:a1")
	awaitContains(t, c, "echo1", "out:a1")
	// A command tab that ended while the old daemon ran stays, held.
	if e := c.request(proto.NewSession{Cwd: dir, Cmd: []string{"sh", "-c", "echo job-done; exit 4"}}); e != "" {
		t.Fatal(e)
	}
	var job string
	for deadline := time.Now().Add(15 * time.Second); job == ""; time.Sleep(20 * time.Millisecond) {
		c.send(proto.Sync{}) // every Input above is queued once Sync answers
		st := c.waitState("the old state", func(model.State) bool { return true })
		for _, p := range st.Panes {
			if p.Held && p.Exited && p.ExitCode == 4 {
				job = p.ID
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the command tab never exited")
		}
	}

	next := startDaemonProc(t, dir, "new", fault)
	survivor := next
	moves := fault == "" || strings.HasSuffix(fault, "~")
	if err := next.serving(t); err != nil {
		if moves {
			t.Fatal(err)
		}
		if !strings.HasSuffix(fault, "!") && !strings.Contains(err.Error(), fault) {
			t.Fatalf("new daemon failed with %v, want %s", err, fault)
		}
		survivor = old
	} else {
		if !moves {
			t.Fatalf("handoff succeeded despite a fault at %s", fault)
		}
		if err := old.waitExit(t); err != nil {
			t.Fatalf("old daemon after handoff: %v: %s", err, old.err.String())
		}
	}
	// A fresh connection reaches whichever daemon serves now.
	c = dial(t, sock, "cli")
	c.send(proto.Sync{})
	st := c.waitState("the state", func(model.State) bool { return true })
	if len(st.Panes) != len(handoffPanes)+2 {
		t.Fatalf("%d panes after the handoff", len(st.Panes))
	}
	codes := map[string]int{"held": 5, job: 4}
	for _, p := range st.Panes {
		code, held := codes[p.ID]
		if p.Exited != held || held && p.ExitCode != code {
			t.Fatalf("pane %s: exited %v code %d", p.ID, p.Exited, p.ExitCode)
		}
	}
	awaitContains(t, c, "held", "make exited with code 5")
	awaitContains(t, c, job, "job-done")

	for i := range 4 {
		id := fmt.Sprintf("sh%d", i)
		awaitContains(t, c, id, fmt.Sprintf("before-%d", 41+i)) // scrollback came over
		typeIn(c, id, fmt.Sprintf("echo after-$((%d+1))\n", 50+i))
		awaitContains(t, c, id, fmt.Sprintf("after-%d", 51+i))
	}
	for _, id := range []string{"echo0", "echo1"} {
		typeIn(c, id, "a2\n")
		awaitText(t, c, id, "out:a1 then out:a2", func(s string) bool {
			i := strings.Index(s, "out:a1")
			return i >= 0 && strings.Contains(s[i:], "out:a2")
		})
	}

	// The stream ran on, without a gap.
	ns := streamed(t, paneText(c, "stream"))
	last := ns[len(ns)-1]
	awaitText(t, c, "stream", fmt.Sprintf("s%d", last+5), func(s string) bool {
		ns := streamed(t, s)
		return ns[len(ns)-1] >= last+5
	})

	// Every line written during the freeze arrived once, in order.
	burst := awaitContains(t, c, "burst", "burst-end")
	var want strings.Builder
	for i := 1; i <= 2000; i++ {
		fmt.Fprintf(&want, "n%d %s\n", i, strings.Repeat("x", 40))
	}
	if !strings.Contains(burst, want.String()+"burst-end") {
		t.Fatalf("burst output lost or repeated:\n%s", burst)
	}

	// The parser state came over: the escape sequence finishes as bold.
	trigger(t, dir, "tui")
	tui := awaitContains(t, c, "tui", "BOLD")
	if !strings.Contains(tui, "agent-tui ready") || strings.Contains(tui, "1mBOLD") {
		t.Fatalf("TUI screen:\n%s", tui)
	}

	// Every byte of queued input reached the program, in order.
	typeIn(c, "input", "tail\r\n")
	sent.WriteString("tail\r\n")
	trigger(t, dir, "input")
	out := filepath.Join(dir, "input.out")
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		got, _ := os.ReadFile(out)
		if len(got) >= sent.Len() {
			if !bytes.Equal(got, sent.Bytes()) {
				t.Fatalf("program read %d bytes, sent %d, first difference at %d", len(got), sent.Len(), mismatch(got, sent.Bytes()))
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("program read %d of %d bytes", len(got), sent.Len())
		}
	}

	// The survivor still stops its panes.
	survivor.cmd.Process.Signal(syscall.SIGTERM)
	if err := survivor.waitExit(t); err != nil {
		t.Fatalf("stopping the daemon: %v: %s", err, survivor.err.String())
	}
	// The writer was inside a message when the old daemon froze, so input
	// moved rather than reaching the PTY first. Read once the log is closed.
	if moves && !regexp.MustCompile(`and [1-9]\d* bytes of unwritten input`).MatchString(next.err.String()) {
		t.Fatalf("no input moved:\n%s", next.err.String())
	}
}

func mismatch(a, b []byte) int {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}
