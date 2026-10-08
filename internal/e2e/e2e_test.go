package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/vt"
)

const timeout = 5 * time.Second

const permission = `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`
const stop = `{"hook_event_name":"Stop"}`

func TestEngine(t *testing.T) {
	isolate(t)
	startDaemon(t)
	gui := connect(t, "gui")
	w := newWorkspace(t, gui)
	p := openPane(t, gui, w.ID, []string{"sh", "-c", "printf hello; sleep 30"})
	gui.waitFor(t, timeout, frameContains(p.ID, "hello"))

	shell := openPane(t, gui, w.ID, []string{"sh"})
	gui.send(t, proto.Input{Pane: shell.ID, Data: []byte("echo typed\r")})
	gui.waitFor(t, timeout, frameContains(shell.ID, "\ntyped\n"))
	gui.send(t, proto.Resize{Pane: shell.ID, Cols: 100, Rows: 30})
	gui.send(t, proto.Input{Pane: shell.ID, Data: []byte("stty size\r")})
	gui.waitFor(t, timeout, func(msg any) bool {
		f, ok := msg.(proto.Frame)
		return ok && f.Pane == shell.ID && f.Grid.Cols == 100 && f.Grid.Rows == 30 &&
			strings.Contains(gridText(f.Grid), "\n30 100\n")
	})

	hook := connect(t, "hook")
	hook.send(t, proto.AgentEvent{Pane: shell.ID, Provider: model.ProviderClaude, Payload: []byte(permission)})
	waitActivity(t, gui, shell.ID, model.StatePendingApproval)
	hook.send(t, proto.AgentEvent{Pane: shell.ID, Provider: model.ProviderClaude, Payload: []byte(stop)})
	waitActivity(t, gui, shell.ID, model.StateCompleted)

	// An exited shell closes its pane.
	gui.send(t, proto.Input{Pane: shell.ID, Data: []byte("exit\r")})
	gui.waitFor(t, timeout, func(msg any) bool {
		s, ok := msg.(proto.StateMsg)
		return ok && !slices.ContainsFunc(s.State.Panes, func(p model.Pane) bool { return p.ID == shell.ID })
	})
	gui.waitFor(t, timeout, func(msg any) bool {
		e, ok := msg.(proto.PaneExited)
		return ok && e.Pane == shell.ID && e.ExitCode == 0
	})
}

// TestSearchScrollback finds a line that scrolled into history and scrolls
// to it: a frame's row y shows line ScrollPushed-ScrollOffset+y, the
// numbering a SearchResult uses.
func TestSearchScrollback(t *testing.T) {
	isolate(t)
	startDaemon(t)
	gui := connect(t, "gui")
	w := newWorkspace(t, gui)
	p := openPane(t, gui, w.ID, []string{"sh", "-c", "i=0; while [ $i -lt 100 ]; do echo row$i; i=$((i+1)); done; echo done; exec sleep 30"})
	gui.waitFor(t, timeout, frameContains(p.ID, "done"))
	gui.send(t, proto.Search{Pane: p.ID, Query: "ROW7"}) // a capital: no row7 matches
	if r := gui.waitFor(t, timeout, func(msg any) bool { _, ok := msg.(proto.SearchResult); return ok }).(proto.SearchResult); len(r.Matches) != 0 {
		t.Fatalf("ROW7: %+v", r.Matches)
	}
	gui.send(t, proto.Search{Pane: p.ID, Query: "row7"})
	r := gui.waitFor(t, timeout, func(msg any) bool { _, ok := msg.(proto.SearchResult); return ok }).(proto.SearchResult)
	if len(r.Matches) != 11 { // row7 and row70-row79
		t.Fatalf("row7: %d matches: %+v", len(r.Matches), r.Matches)
	}
	m := r.Matches[0]
	gui.send(t, proto.Scroll{Pane: p.ID, Lines: 1000})
	f := gui.waitFor(t, timeout, func(msg any) bool {
		f, ok := msg.(proto.Frame)
		return ok && f.Pane == p.ID && f.ScrollOffset > 0 && f.ScrollOffset == f.ScrollMax
	}).(proto.Frame)
	y := int(m.Line - (f.ScrollPushed - uint64(f.ScrollOffset)))
	if y < 0 || y >= f.Grid.Rows {
		t.Fatalf("match line %d is off the view: row %d", m.Line, y)
	}
	if row := strings.Split(gridText(f.Grid), "\n")[y]; row[m.Col:m.Col+m.Cols] != "row7" || row != "row7" {
		t.Fatalf("row %d is %q", y, row)
	}
}

// TestResizeQuietPane: a Resize that reaches a pane after its program's last
// output, as one queued behind an input flood does, still sends the window a
// frame at the new size. The program here never redraws; before the pane
// signalled a resize, the window kept its 80x24 frame until the next output.
func TestResizeQuietPane(t *testing.T) {
	isolate(t)
	startDaemon(t)
	gui := connect(t, "gui")
	w := newWorkspace(t, gui)
	p := openPane(t, gui, w.ID, []string{"sh", "-c", "printf ready; exec sleep 30"})
	gui.waitFor(t, timeout, frameContains(p.ID, "ready"))
	gui.send(t, proto.Resize{Pane: p.ID, Cols: 100, Rows: 30})
	gui.waitFor(t, timeout, func(msg any) bool {
		f, ok := msg.(proto.Frame)
		return ok && f.Pane == p.ID && f.Grid.Cols == 100 && f.Grid.Rows == 30
	})
}

// A tab opened from a shell that cd'd starts there and is titled after that
// directory, as is the shell's own tab; a shell's exit closes its tab.
func TestNewTabFollowsCwd(t *testing.T) {
	isolate(t)
	startDaemon(t)
	gui := connect(t, "gui")
	s := waitState(t, gui, func(s model.State) bool { return len(s.Workspaces) == 1 && len(s.Panes) == 1 })
	w, shell := s.Workspaces[0], s.Panes[0].ID
	other := filepath.Join(t.TempDir(), "other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	gui.send(t, proto.Input{Pane: shell, Data: []byte("cd " + other + "\r")})
	waitState(t, gui, func(s model.State) bool { return s.Workspaces[0].Label == "other" })

	gui.send(t, proto.NewTab{WorkspaceID: w.ID, FromPane: shell})
	s = waitState(t, gui, func(s model.State) bool { return len(s.Workspaces) == 2 && len(s.Panes) == 2 })
	if nw := s.Workspaces[1]; nw.Label != "other" || filepath.Base(nw.Path) != "other" || nw.ID == w.ID || nw.ProjectID != w.ProjectID {
		t.Fatalf("new tab %+v", nw)
	}
	second := s.Panes[1].ID

	gui.send(t, proto.Input{Pane: second, Data: []byte("exit\r")})
	s = waitState(t, gui, func(s model.State) bool { return len(s.Panes) == 1 })
	if len(s.Workspaces) != 1 || s.Workspaces[0].ID != w.ID {
		t.Fatalf("exit left %+v", s.Workspaces)
	}
	gui.send(t, proto.Input{Pane: shell, Data: []byte("exit\r")})
	waitState(t, gui, func(s model.State) bool { return len(s.Workspaces) == 0 && len(s.Panes) == 0 })
}

// A CLI client gets no pushes; Sync acknowledges its request with state.
func TestCLISync(t *testing.T) {
	isolate(t)
	startDaemon(t)
	cli := connect(t, "cli")
	cli.send(t, proto.NewSession{Cwd: t.TempDir()})
	cli.send(t, proto.Sync{})
	s := cli.waitFor(t, timeout, func(any) bool { return true })
	st, ok := s.(proto.StateMsg)
	if !ok || len(st.State.Workspaces) != 1 || st.State.Workspaces[0].Label == "" || len(st.State.Workspaces[0].Tabs) != 1 {
		t.Fatalf("Sync reply %#v", s)
	}
}

func waitState(t *testing.T, c *client, ok func(model.State) bool) model.State {
	t.Helper()
	return c.waitFor(t, timeout, func(msg any) bool {
		s, is := msg.(proto.StateMsg)
		return is && ok(s.State)
	}).(proto.StateMsg).State
}

func TestPersistenceAndRestore(t *testing.T) {
	isolate(t)
	shutdown := startDaemon(t)
	gui := connect(t, "gui")
	w := newWorkspace(t, gui)
	p := openPane(t, gui, w.ID, []string{"sh", "-c", "printf hello; sleep 30"})
	gui.waitFor(t, timeout, frameContains(p.ID, "hello"))
	before := snapshot(t)
	shutdown()
	if _, err := os.Stat(store.Path()); err != nil {
		t.Fatalf("persisted state: %v", err)
	}

	startDaemon(t)
	before = utc(before)
	restored := connect(t, "gui")
	s := restored.waitFor(t, timeout, func(msg any) bool {
		_, ok := msg.(proto.StateMsg)
		return ok
	}).(proto.StateMsg).State
	s = utc(s)
	if !reflect.DeepEqual(s.Projects, before.Projects) || !reflect.DeepEqual(s.Workspaces, before.Workspaces) {
		t.Fatalf("restored %s\nwant %s", dump(s), dump(before))
	}
	if !reflect.DeepEqual(s.Panes, before.Panes) {
		t.Fatalf("restored pane: %+v, want %+v", s.Panes, p)
	}
	restored.waitFor(t, timeout, frameContains(p.ID, "hello"))
}

// A GUI connecting to an empty daemon lands in a live shell; sessions group
// after the fact and a restart brings both back.
func TestSessions(t *testing.T) {
	isolate(t)
	shutdown := startDaemon(t)
	cwd := t.TempDir()
	gui := connectIn(t, "gui", cwd)
	s := gui.waitFor(t, timeout, func(msg any) bool {
		_, ok := msg.(proto.StateMsg)
		return ok
	}).(proto.StateMsg).State
	if len(s.Workspaces) != 1 || len(s.Panes) != 1 || s.Workspaces[0].Path != cwd || s.Workspaces[0].ProjectID != "" {
		t.Fatalf("first state: %+v", s)
	}
	first, shell := s.Workspaces[0].ID, s.Panes[0].ID
	gui.send(t, proto.Input{Pane: shell, Data: []byte("echo hi\r")})
	gui.waitFor(t, timeout, frameContains(shell, "\nhi\n"))

	gui.send(t, proto.NewSession{Cwd: t.TempDir()})
	s = gui.waitFor(t, timeout, func(msg any) bool {
		s, ok := msg.(proto.StateMsg)
		return ok && len(s.State.Workspaces) == 2
	}).(proto.StateMsg).State
	second := s.Workspaces[1].ID
	gui.send(t, proto.NewGroup{Name: "agents", WorkspaceIDs: []string{first, second}})
	gui.waitFor(t, timeout, func(msg any) bool {
		s, ok := msg.(proto.StateMsg)
		return ok && len(s.State.Projects) == 1 && !slices.ContainsFunc(s.State.Workspaces, func(w model.Workspace) bool {
			return w.ProjectID != s.State.Projects[0].ID
		})
	})
	before := snapshot(t)
	shutdown()

	startDaemon(t)
	after, before := utc(snapshot(t)), utc(before)
	if !reflect.DeepEqual(after.Projects, before.Projects) || !reflect.DeepEqual(after.Workspaces, before.Workspaces) || len(after.Panes) != 2 {
		t.Fatalf("restored %s\nwant %s", dump(after), dump(before))
	}
	if g := after.Projects[0]; g.Name != "agents" || g.Kind != model.ProjectGroup {
		t.Fatalf("group: %+v", g)
	}
}

// Codex titles its pane after the folder, which says nothing; its first
// prompt names the tab and the session instead.
func TestPromptNamesSession(t *testing.T) {
	isolate(t)
	startDaemon(t)
	cwd := filepath.Join(t.TempDir(), "cap")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	gui := connectIn(t, "gui", cwd)
	s := waitState(t, gui, func(s model.State) bool { return len(s.Panes) == 1 })
	shell := s.Panes[0].ID
	if w := s.Workspaces[0]; w.NameSet || w.Label != "cap" {
		t.Fatalf("fresh session %+v", w)
	}
	gui.send(t, proto.Input{Pane: shell, Data: []byte("printf '\\033]0;\\342\\240\\213 cap\\007'\r")})
	waitState(t, gui, func(s model.State) bool { return s.Panes[0].Title == "cap" && s.Workspaces[0].Tabs[0].Title == "cap" })

	hook := connect(t, "hook")
	payload := `{"session_id":"s1","transcript_path":"/t.jsonl","hook_event_name":"UserPromptSubmit","prompt":"rename foo to bar\nthen run the tests"}`
	hook.send(t, proto.AgentEvent{Pane: shell, Provider: model.ProviderCodex, Payload: []byte(payload)})
	waitState(t, gui, func(s model.State) bool {
		w := s.Workspaces[0]
		return w.Tabs[0].Title == "rename foo to bar" && w.Label == "rename foo to bar" && !w.NameSet
	})
}

func TestBinaryHook(t *testing.T) {
	isolate(t)
	bin := filepath.Join(t.TempDir(), "pitwall")
	run(t, time.Minute, filepath.Join("..", ".."), "go", "build", "-o", bin, "./cmd/pitwall")
	startDaemon(t)
	gui := connect(t, "gui")
	w := newWorkspace(t, gui)
	p := openPane(t, gui, w.ID, []string{"sh"})
	runHook := func(pane, payload string, limit time.Duration) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, "hook", "claude")
		cmd.Env = slices.DeleteFunc(cmd.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "PITWALL_PANE=") })
		if pane != "" {
			cmd.Env = append(cmd.Env, "PITWALL_PANE="+pane)
		}
		cmd.Stdin = strings.NewReader(payload)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("hook: %v: %s", err, out)
		}
	}
	runHook(p.ID, permission, timeout)
	waitActivity(t, gui, p.ID, model.StatePendingApproval)
	before := snapshot(t)
	runHook("", stop, time.Second)
	after := snapshot(t)
	// Git stats can independently update the state version.
	before.Stats, after.Stats = nil, nil
	before.Version, after.Version = 0, 0
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("hook without PITWALL_PANE changed state: before %+v, after %+v", before, after)
	}
}

func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // defaults, whatever the user's config.toml says
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("SHELL", "/bin/sh") // the first session's shell, without the user's rc files
	// why: run inside a pitwall pane, these point hooks and the CLI at the user's own daemon.
	t.Setenv("PITWALL_SOCKET", "")
	t.Setenv("PITWALL_PANE", "")
}

func startDaemon(t *testing.T) func() {
	t.Helper()
	path, err := proto.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := daemon.New()
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx, ln) }()
	var once sync.Once
	shutdown := func() {
		t.Helper()
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Serve: %v", err)
				}
			case <-time.After(timeout):
				t.Error("daemon shutdown timed out")
			}
		})
	}
	t.Cleanup(shutdown)
	return shutdown
}

type client struct {
	conn *proto.Conn
	msgs chan any
}

func connect(t *testing.T, kind string) *client {
	t.Helper()
	return connectIn(t, kind, t.TempDir())
}

// connectIn says Hello as if launched in cwd.
func connectIn(t *testing.T, kind, cwd string) *client {
	t.Helper()
	path, err := proto.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := proto.Dial(path)
	if err != nil {
		t.Fatal(err)
	}
	c := &client{conn: conn, msgs: make(chan any, 64)}
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		defer close(c.msgs)
		for {
			msg, err := conn.Recv()
			if err != nil {
				msg = err
			}
			select {
			case c.msgs <- msg:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(done)
		conn.Close()
		select {
		case <-exited:
		case <-time.After(timeout):
			t.Error("client receive loop did not stop")
		}
	})
	c.send(t, proto.Hello{Version: proto.Version, Kind: kind, Cwd: cwd})
	return c
}

func (c *client) send(t *testing.T, msg any) {
	t.Helper()
	if err := c.conn.Send(msg); err != nil {
		t.Fatalf("send %T: %v", msg, err)
	}
}

func (c *client) waitFor(t *testing.T, limit time.Duration, match func(any) bool) any {
	t.Helper()
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-c.msgs:
			if !ok {
				t.Fatal("daemon connection closed")
			}
			switch m := msg.(type) {
			case error:
				t.Fatalf("receive: %v", m)
			case proto.Error:
				t.Fatalf("daemon: %s", m.Message)
			}
			if match(msg) {
				return msg
			}
		case <-timer.C:
			t.Fatalf("no matching daemon message within %s", limit)
		}
	}
}

// gitRepo is a new repository on main with one commit.
func gitRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	run(t, timeout, repo, "git", "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("e2e\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(t, timeout, repo, "git", "add", "README")
	run(t, timeout, repo, "git", "-c", "user.name=Pitwall Test", "-c", "user.email=pitwall@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Initialize test repository")
	return repo
}

// A worktree tab's shell has the tab's ports, and the repo's
// .pitwall/worktree.toml copies .env from the main checkout and types its
// setup into that shell, where it runs only once the user presses Enter.
func TestWorktreeSetup(t *testing.T) {
	isolate(t)
	startDaemon(t)
	gui := connect(t, "gui")
	repo := gitRepo(t)
	for name, body := range map[string]string{
		".env":                   "SECRET=1\n",
		".pitwall/worktree.toml": "copy = [\".env\"]\nsetup = 'echo \"$PORT $PITWALL_PORT_BASE $PITWALL_PORTS\" > port.txt'\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o700)
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gui.send(t, proto.AddProject{Path: repo})
	p := waitState(t, gui, func(s model.State) bool { return len(s.Projects) == 1 }).Projects[0]
	gui.send(t, proto.NewWorkspace{ProjectID: p.ID})
	s := waitState(t, gui, func(s model.State) bool {
		return slices.ContainsFunc(s.Workspaces, func(w model.Workspace) bool { return w.ProjectID == p.ID && len(w.Tabs) == 1 })
	})
	w := s.Workspaces[slices.IndexFunc(s.Workspaces, func(w model.Workspace) bool { return w.ProjectID == p.ID })]
	if w.Ports.String() != "3010-3019" {
		t.Fatalf("ports %v, want 3010-3019", w.Ports)
	}
	shell := s.Panes[slices.IndexFunc(s.Panes, func(sp model.Pane) bool { return sp.WorkspaceID == w.ID })].ID
	gui.waitFor(t, timeout, frameContains(shell, "> port.txt"))
	time.Sleep(500 * time.Millisecond) // the time a line with Enter would take to run
	if _, err := os.Stat(filepath.Join(w.Path, "port.txt")); err == nil {
		t.Fatal("the repo's setup ran without Enter")
	}
	gui.send(t, proto.Input{Pane: shell, Data: []byte("\r")})
	var got []byte
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline) && len(got) == 0; time.Sleep(20 * time.Millisecond) {
		got, _ = os.ReadFile(filepath.Join(w.Path, "port.txt"))
	}
	if string(got) != "3010 3010 3010-3019\n" {
		t.Errorf("setup wrote %q", got)
	}
	if env, err := os.ReadFile(filepath.Join(w.Path, ".env")); string(env) != "SECRET=1\n" {
		t.Errorf("worktree .env %q %v", env, err)
	}
}

func newWorkspace(t *testing.T, gui *client) model.Workspace {
	t.Helper()
	repo := gitRepo(t)
	gui.send(t, proto.AddProject{Path: repo})
	s := gui.waitFor(t, timeout, func(msg any) bool {
		s, ok := msg.(proto.StateMsg)
		return ok && len(s.State.Projects) == 1
	}).(proto.StateMsg).State
	p := s.Projects[0]
	if p.Root != repo || p.Kind != model.ProjectGit {
		t.Fatalf("project: %+v", p)
	}
	gui.send(t, proto.NewWorkspace{ProjectID: p.ID})
	inProject := func(w model.Workspace) bool { return w.ProjectID == p.ID }
	s = gui.waitFor(t, timeout, func(msg any) bool {
		s, ok := msg.(proto.StateMsg)
		return ok && slices.ContainsFunc(s.State.Workspaces, inProject)
	}).(proto.StateMsg).State
	w := s.Workspaces[slices.IndexFunc(s.Workspaces, inProject)]
	if w.Name != "workspace-1" || w.Branch != "workspace-1" || w.ProjectID != p.ID || w.Path == repo {
		t.Fatalf("workspace: %+v", w)
	}
	if _, err := os.Stat(filepath.Join(w.Path, ".git")); err != nil {
		t.Fatalf("worktree on disk: %v", err)
	}
	if got := strings.TrimSpace(run(t, timeout, w.Path, "git", "rev-parse", "--show-toplevel")); got != w.Path {
		t.Fatalf("worktree root: %q, want %q", got, w.Path)
	}
	return w
}

func openPane(t *testing.T, gui *client, workspace string, cmd []string) model.Pane {
	t.Helper()
	gui.send(t, proto.OpenPane{WorkspaceID: workspace, Cmd: cmd})
	s := gui.waitFor(t, timeout, func(msg any) bool {
		s, ok := msg.(proto.StateMsg)
		return ok && slices.ContainsFunc(s.State.Panes, func(p model.Pane) bool {
			return p.WorkspaceID == workspace && slices.Equal(p.Cmd, cmd)
		})
	}).(proto.StateMsg).State
	return s.Panes[slices.IndexFunc(s.Panes, func(p model.Pane) bool { return slices.Equal(p.Cmd, cmd) })]
}

func waitActivity(t *testing.T, gui *client, pane string, state model.AgentState) {
	t.Helper()
	gui.waitFor(t, timeout, func(msg any) bool {
		s, ok := msg.(proto.StateMsg)
		return ok && slices.ContainsFunc(s.State.Activities, func(a model.Activity) bool {
			return a.PaneID == pane && a.Provider == model.ProviderClaude && a.State == state
		})
	})
}

func frameContains(pane, text string) func(any) bool {
	return func(msg any) bool {
		f, ok := msg.(proto.Frame)
		return ok && f.Pane == pane && strings.Contains(gridText(f.Grid), text)
	}
}

func gridText(g vt.Grid) string {
	var text strings.Builder
	for y := 0; y < g.Rows; y++ {
		var row strings.Builder
		for x := 0; x < g.Cols; x++ {
			row.WriteString(g.At(x, y).Content)
		}
		text.WriteString(strings.TrimRight(row.String(), " "))
		text.WriteByte('\n')
	}
	return text.String()
}

// utc puts s's workspace times in UTC: gob decodes a time whose offset
// matches the local zone as Local, so on a UTC machine a saved time and a
// live one differ only in their *Location.
func utc(s model.State) model.State {
	s.Workspaces = slices.Clone(s.Workspaces)
	for i := range s.Workspaces {
		s.Workspaces[i].UpdatedAt = s.Workspaces[i].UpdatedAt.UTC()
	}
	return s
}

// dump renders s's groups, tabs with their layouts, and panes for a failure.
func dump(s model.State) string {
	b, _ := json.Marshal(struct {
		Projects   []model.Project
		Workspaces []model.Workspace
		Panes      []model.Pane
	}{s.Projects, s.Workspaces, s.Panes})
	return string(b)
}

func snapshot(t *testing.T) model.State {
	t.Helper()
	gui := connect(t, "gui")
	return gui.waitFor(t, timeout, func(msg any) bool {
		_, ok := msg.(proto.StateMsg)
		return ok
	}).(proto.StateMsg).State
}

func run(t *testing.T, limit time.Duration, dir, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, out)
	}
	return string(out)
}

// TestDriveTabs drives a real daemon the way an agent or a script does,
// through the pitwall binary: new -- cmd, wait and ls --json.
func TestDriveTabs(t *testing.T) {
	isolate(t)
	bin := filepath.Join(t.TempDir(), "pitwall")
	run(t, time.Minute, filepath.Join("..", ".."), "go", "build", "-o", bin, "./cmd/pitwall")
	startDaemon(t)
	pitwall := func(args ...string) (int, string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		var out, errOut strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &errOut
		err := cmd.Run()
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			t.Fatalf("pitwall %v: %v", args, err)
		}
		return cmd.ProcessState.ExitCode(), out.String(), errOut.String()
	}

	// A tab running a command keeps its pane, and its exit code, after exiting.
	if code, out, stderr := pitwall("new", "-n", "echo", "--", "sh", "-c", `echo done; exit 4`); code != 0 || out != "#1\n" {
		t.Fatalf("new: %d %q %s", code, out, stderr)
	}
	if code, out, stderr := pitwall("wait", "echo", "--until", "exit", "--timeout", "10s"); code != 4 || out != "exit 4\n" {
		t.Fatalf("wait: %d %q %s", code, out, stderr)
	}
	var tabs []map[string]any
	_, out, _ := pitwall("ls", "--json")
	if err := json.Unmarshal([]byte(out), &tabs); err != nil || len(tabs) != 1 || tabs[0]["state"] != "exited" || tabs[0]["exit_code"] != 4.0 {
		t.Fatalf("ls --json: %v %s", err, out)
	}

	// wait reports an agent blocked on a permission prompt, then its finished turn.
	if code, _, stderr := pitwall("new", "-n", "agent", "--", "sh"); code != 0 {
		t.Fatalf("new: %d %s", code, stderr)
	}
	s := snapshot(t)
	agent := s.Panes[slices.IndexFunc(s.Panes, func(p model.Pane) bool { return len(p.Cmd) == 1 })].ID
	gui := connect(t, "gui")
	hook := connect(t, "hook")
	hook.send(t, proto.AgentEvent{Pane: agent, Provider: model.ProviderClaude, Payload: []byte(permission)})
	waitActivity(t, gui, agent, model.StatePendingApproval)
	if code, out, _ := pitwall("wait", "agent", "--until", "done"); code != 2 || !strings.HasPrefix(out, "blocked") {
		t.Fatalf("wait on a blocked agent: %d %q", code, out)
	}
	hook.send(t, proto.AgentEvent{Pane: agent, Provider: model.ProviderClaude, Payload: []byte(stop)})
	waitActivity(t, gui, agent, model.StateCompleted)
	if code, out, stderr := pitwall("wait", "agent", "--until", "done", "--timeout", "10s"); code != 0 || out != "done\n" {
		t.Fatalf("wait on a finished agent: %d %q %s", code, out, stderr)
	}
}
