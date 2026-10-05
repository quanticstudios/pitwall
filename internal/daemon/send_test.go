package daemon

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
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

func TestSendAndHeldCommand(t *testing.T) {
	sendPause = time.Millisecond
	t.Cleanup(func() { sendPause = 300 * time.Millisecond })
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	cli, hook, watch := dial(t, sock, "cli"), dial(t, sock, "hook"), dial(t, sock, "watch")

	if e := cli.request(proto.NewSession{Cwd: t.TempDir(), Cmd: []string{"claude", "fix it"}}); e != "" {
		t.Fatal(e)
	}
	st := watch.waitState("the command's tab", func(s model.State) bool { return len(s.Panes) == 1 })
	id, fp := st.Panes[0].ID, f.pane(0)
	if !slices.Equal(fp.cfg.Cmd, []string{"claude", "fix it"}) || !slices.Equal(st.Panes[0].Cmd, fp.cfg.Cmd) {
		t.Fatalf("pane runs %q, state says %q", fp.cfg.Cmd, st.Panes[0].Cmd)
	}
	state := func(s string) {
		t.Helper()
		hook.send(proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte(s)})
		watch.waitState(s, func(st model.State) bool {
			return slices.ContainsFunc(st.Activities, func(a model.Activity) bool { return a.PaneID == id && string(a.State) == s })
		})
	}
	typed := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); fp.got() != want && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		if got := fp.got(); got != want {
			t.Fatalf("pane got %q, want %q", got, want)
		}
	}

	// An idle agent, and a shell, take the text and a separate Enter.
	if e := cli.request(proto.Send{Pane: id, Text: "a\nb", Enter: true}); e != "" {
		t.Fatal(e)
	}
	typed("a\nb\r")
	if e := cli.request(proto.Send{Pane: id, Text: "c"}); e != "" {
		t.Fatal(e)
	}
	typed("a\nb\rc")

	// A working agent takes it only with Force.
	state("working")
	if e := cli.request(proto.Send{Pane: id, Text: "x", Enter: true}); !strings.Contains(e, "working") {
		t.Fatalf("send to a working agent: %q", e)
	}
	if e := cli.request(proto.Send{Pane: id, Text: "d", Force: true}); e != "" {
		t.Fatal(e)
	}
	typed("a\nb\rcd")

	// A blocked agent never does: Enter would answer its prompt.
	for _, s := range []string{"pending-approval", "awaiting-input", "plan-ready"} {
		state(s)
		if e := cli.request(proto.Send{Pane: id, Text: "y", Enter: true, Force: true}); !strings.Contains(e, "answer it in the tab") {
			t.Fatalf("send to a %s agent: %q", s, e)
		}
	}
	typed("a\nb\rcd")

	// The command's pane stays after it exits, with its exit code.
	fp.code = 3
	fp.Close()
	st = watch.waitState("the pane exited", func(s model.State) bool { return len(s.Panes) == 1 && s.Panes[0].Exited })
	if st.Panes[0].ExitCode != 3 || len(st.Workspaces) != 1 || len(st.Activities) != 0 {
		t.Fatalf("after exit: %+v", st)
	}
	watch.waitFor("PaneExited", func(m any) bool { return m == proto.PaneExited{Pane: id, ExitCode: 3} })
	if e := cli.request(proto.Send{Pane: id, Text: "z"}); !strings.Contains(e, "exited") {
		t.Fatalf("send to an exited pane: %q", e)
	}
	if e := cli.request(proto.ClosePane{Pane: id}); e != "" {
		t.Fatal(e)
	}
	watch.waitState("the tab closed", func(s model.State) bool { return len(s.Workspaces) == 0 })
}
