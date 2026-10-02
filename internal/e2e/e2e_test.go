package e2e_test

import (
	"context"
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

	// Exit and reap the shell before ClosePane removes its handle.
	gui.send(t, proto.Input{Pane: shell.ID, Data: []byte("exit\r")})
	gui.waitFor(t, timeout, func(msg any) bool {
		e, ok := msg.(proto.PaneExited)
		return ok && e.Pane == shell.ID && e.ExitCode == 0
	})
	gui.send(t, proto.ClosePane{Pane: shell.ID})
	gui.waitFor(t, timeout, func(msg any) bool {
		s, ok := msg.(proto.StateMsg)
		return ok && !slices.ContainsFunc(s.State.Panes, func(p model.Pane) bool { return p.ID == shell.ID })
	})
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
	restored := connect(t, "gui")
	s := restored.waitFor(t, timeout, func(msg any) bool {
		_, ok := msg.(proto.StateMsg)
		return ok
	}).(proto.StateMsg).State
	if !reflect.DeepEqual(s.Projects, before.Projects) || !reflect.DeepEqual(s.Workspaces, before.Workspaces) {
		t.Fatalf("restored workspace: %+v", s.Workspaces)
	}
	if !reflect.DeepEqual(s.Panes, before.Panes) {
		t.Fatalf("restored pane: %+v, want %+v", s.Panes, p)
	}
	restored.waitFor(t, timeout, frameContains(p.ID, "hello"))
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
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
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
	c.send(t, proto.Hello{Version: proto.Version, Kind: kind})
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

func newWorkspace(t *testing.T, gui *client) model.Workspace {
	t.Helper()
	repo := t.TempDir()
	run(t, timeout, repo, "git", "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("e2e\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(t, timeout, repo, "git", "add", "README")
	run(t, timeout, repo, "git", "-c", "user.name=Pitwall Test", "-c", "user.email=pitwall@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Initialize test repository")
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
	s = gui.waitFor(t, timeout, func(msg any) bool {
		s, ok := msg.(proto.StateMsg)
		return ok && len(s.State.Workspaces) == 1
	}).(proto.StateMsg).State
	w := s.Workspaces[0]
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
