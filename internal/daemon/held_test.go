package daemon

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// request sends m and a Sync, and returns the daemon's error for m, if any.
func (c *testClient) request(m any) string {
	c.t.Helper()
	c.send(m)
	c.send(proto.Sync{})
	var errs []string
	for {
		select {
		case got, open := <-c.in:
			if !open {
				c.t.Fatal("connection closed")
			}
			switch got := got.(type) {
			case proto.Error:
				errs = append(errs, got.Message)
			case proto.StateMsg:
				return strings.Join(errs, "; ")
			}
		case <-time.After(5 * time.Second):
			c.t.Fatal("timed out waiting for Sync")
		}
	}
}

// A tab opened with a command runs it, and its pane stays after the command
// exits, with the exit code, until it is closed. A watch client sees it all.
func TestHeldCommand(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	cli, watch := dial(t, sock, "cli"), dial(t, sock, "watch")

	if e := cli.request(proto.NewSession{Cwd: t.TempDir(), Cmd: []string{"claude", "fix it"}}); e != "" {
		t.Fatal(e)
	}
	st := watch.waitState("the command's tab", func(s model.State) bool { return len(s.Panes) == 1 })
	id, fp := st.Panes[0].ID, f.pane(0)
	if !slices.Equal(fp.cfg.Cmd, []string{"claude", "fix it"}) || !slices.Equal(st.Panes[0].Cmd, fp.cfg.Cmd) {
		t.Fatalf("pane runs %q, state says %q", fp.cfg.Cmd, st.Panes[0].Cmd)
	}

	fp.code = 3
	fp.Close()
	st = watch.waitState("the pane exited", func(s model.State) bool { return len(s.Panes) == 1 && s.Panes[0].Exited })
	if st.Panes[0].ExitCode != 3 || len(st.Workspaces) != 1 || len(st.Activities) != 0 {
		t.Fatalf("after exit: %+v", st)
	}
	watch.waitFor("PaneExited", func(m any) bool { return m == proto.PaneExited{Pane: id, ExitCode: 3} })

	// It still resizes, and a window gets the frame at the new size.
	gui := dial(t, sock, "gui")
	gui.waitFor("the first frames", func(m any) bool { f, ok := m.(proto.Frame); return ok && f.Pane == id })
	fp.mu.Lock()
	fp.title = "resized" // only a frame built after the resize has it
	fp.mu.Unlock()
	if e := cli.request(proto.Resize{Pane: id, Cols: 100, Rows: 30}); e != "" {
		t.Fatal(e)
	}
	gui.waitFor("a frame after the resize", func(m any) bool {
		f, ok := m.(proto.Frame)
		return ok && f.Pane == id && f.Grid.Title == "resized"
	})
	fp.mu.Lock()
	size := fp.size
	fp.mu.Unlock()
	if size != [2]int{100, 30} {
		t.Fatalf("pane size %v", size)
	}
	if e := cli.request(proto.ClosePane{Pane: id}); e != "" {
		t.Fatal(e)
	}
	watch.waitState("the tab closed", func(s model.State) bool { return len(s.Workspaces) == 0 })
}

// screen is a frame's text, rows joined.
func screen(g vt.Grid) string {
	var b strings.Builder
	for _, c := range g.Cells {
		b.WriteString(c.Content)
	}
	return b.String()
}

// Held panes survive a restart: an exited one keeps its exit code, a resumable
// agent resumes and stays held, and any other running command is not run
// again and comes back exited with its code unknown.
func TestHeldSurvivesRestart(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	cli, watch := dial(t, sock, "cli"), dial(t, sock, "watch")
	for _, cmd := range [][]string{{"make", "build", "SECRET=1"}, {"sh", "-c", "long job"}, {"claude", "fix it"}} {
		if e := cli.request(proto.NewSession{Cwd: t.TempDir(), Cmd: cmd}); e != "" {
			t.Fatal(e)
		}
	}
	st := watch.waitState("three tabs", func(s model.State) bool { return len(s.Panes) == 3 })
	done, running, agent := st.Panes[0].ID, st.Panes[1].ID, st.Panes[2].ID
	if e := cli.request(proto.AgentEvent{Pane: agent, Provider: model.ProviderClaude, Payload: []byte("working")}); e != "" {
		t.Fatal(e)
	}
	f.pane(0).code = 3
	f.pane(0).Close()
	watch.waitState("make exited", func(s model.State) bool { return s.Panes[0].Exited && s.Panes[2].SessionID != "" })
	stop()

	for restart := range 2 {
		sock, stop = run(t, f)
		st = dial(t, sock, "watch").waitState("restored", func(model.State) bool { return true })
		if n := len(f.panes); n != 4+restart {
			t.Fatalf("restart %d started %d panes; only the agent runs again", restart, n)
		}
		byID := map[string]model.Pane{}
		for _, p := range st.Panes {
			byID[p.ID] = p
		}
		if p := byID[done]; !p.Held || !p.Exited || p.ExitUnknown || p.ExitCode != 3 {
			t.Fatalf("exited pane: %+v", p)
		}
		if p := byID[running]; !p.Held || !p.Exited || !p.ExitUnknown {
			t.Fatalf("running pane: %+v", p)
		}
		if p := byID[agent]; !p.Held || p.Exited || !slices.Equal(f.pane(3+restart).cfg.Cmd, []string{"claude", "--resume", "sess-1"}) {
			t.Fatalf("agent pane %+v runs %q", p, f.pane(3+restart).cfg.Cmd)
		}
		want := map[string]string{
			done:    "make exited with code 3 before pitwall restarted",
			running: "pitwall restarted while sh ran, so it was not run again",
		}
		gui := dial(t, sock, "gui")
		gui.waitFor("both notices", func(m any) bool {
			f, ok := m.(proto.Frame)
			if !ok || want[f.Pane] == "" {
				return false
			}
			if s := screen(f.Grid); !strings.Contains(s, want[f.Pane]) || strings.Contains(s, "long job") || strings.Contains(s, "SECRET") {
				t.Fatalf("pane %s shows %q, want %q and no arguments", f.Pane, s, want[f.Pane])
			}
			delete(want, f.Pane)
			return len(want) == 0
		})
		if e := dial(t, sock, "cli").request(proto.Input{Pane: running, Data: []byte("x")}); e == "" {
			t.Fatal("input to a restored exited pane did not fail")
		}
		if restart == 0 {
			stop()
		}
	}

	// The resumed agent stays held when it exits.
	watch = dial(t, sock, "watch")
	f.pane(4).code = 1
	f.pane(4).Close()
	st = watch.waitState("the agent exited", func(s model.State) bool {
		return slices.ContainsFunc(s.Panes, func(p model.Pane) bool { return p.ID == agent && p.Exited })
	})
	if i := slices.IndexFunc(st.Panes, func(p model.Pane) bool { return p.ID == agent }); st.Panes[i].ExitCode != 1 || len(st.Panes) != 3 {
		t.Fatalf("after the agent exited: %+v", st.Panes)
	}
	stop()
}
