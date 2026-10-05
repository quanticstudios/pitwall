package daemon

import (
	"context"
	"errors"
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

	// A working agent never takes it: the turn in flight would end after the send.
	state("working")
	if e := cli.request(proto.Send{Pane: id, Text: "x", Enter: true}); !strings.Contains(e, "working") {
		t.Fatalf("send to a working agent: %q", e)
	}
	typed("a\nb\rc")

	// A blocked agent never does: Enter would answer its prompt.
	for _, s := range []string{"pending-approval", "awaiting-input", "plan-ready"} {
		state(s)
		if e := cli.request(proto.Send{Pane: id, Text: "y", Enter: true}); !strings.Contains(e, "answer it in the tab") {
			t.Fatalf("send to a %s agent: %q", s, e)
		}
	}
	typed("a\nb\rc")

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

// sendPane opens a tab running a command over the protocol and returns
// its pane id and fake.
func sendPane(t *testing.T, f *fakes, cli, watch *testClient) (string, *fakePane) {
	t.Helper()
	if e := cli.request(proto.NewSession{Cwd: t.TempDir(), Cmd: []string{"tool"}}); e != "" {
		t.Fatal(e)
	}
	st := watch.waitState("the tab", func(s model.State) bool { return len(s.Panes) == 1 })
	return st.Panes[0].ID, f.pane(0)
}

// A prompt on screen stops send in a pane no hook or detection has named
// an agent yet, as a freshly started agent's trust prompt is.
func TestSendReadsTheScreenOfEveryPane(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	cli, watch := dial(t, sock, "cli"), dial(t, sock, "watch")
	id, fp := sendPane(t, f, cli, watch)
	fp.mu.Lock()
	fp.screen = "Do you want to proceed?"
	fp.mu.Unlock()
	if e := cli.request(proto.Send{Pane: id, Text: "1", Enter: true}); !strings.Contains(e, "answer it in the tab") {
		t.Fatalf("send: %q", e)
	}
	if got := fp.got(); got != "" {
		t.Fatalf("pane got %q", got)
	}
}

// Two sends into one pane never merge: each paste gets its own Enter.
func TestSendsDoNotInterleave(t *testing.T) {
	sendPause = 50 * time.Millisecond
	t.Cleanup(func() { sendPause = 300 * time.Millisecond })
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	cli, watch := dial(t, sock, "cli"), dial(t, sock, "watch")
	id, fp := sendPane(t, f, cli, watch)
	errs := make(chan string, 2)
	for _, text := range []string{"one", "two"} {
		c := dial(t, sock, "cli")
		go func() { errs <- c.request(proto.Send{Pane: id, Text: text, Enter: true}) }()
	}
	for range 2 {
		if e := <-errs; e != "" {
			t.Fatal(e)
		}
	}
	if got := fp.got(); got != "one\rtwo\r" && got != "two\rone\r" {
		t.Fatalf("pane got %q", got)
	}
}

// An OSC notification asking for input blocks send like a hook's question.
func TestSendRefusesAnOSCNotice(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir(), Cmd: []string{"tool"}}))
	d.mu.Lock()
	id := d.st.Panes[0].ID
	d.mu.Unlock()
	d.notice(id, vt.Notification{Title: "Codex", Body: "approve?"})
	if err := d.handle(ctx, proto.Send{Pane: id, Text: "y", Enter: true}); !errors.Is(err, errSendBlocked) {
		t.Fatalf("send with a notice up: %v", err)
	}
	if got := f.pane(0).got(); got != "" {
		t.Fatalf("pane got %q", got)
	}
}

// The daemon stops while a pane's writer is stuck on a write the program
// never reads, after a send queued into it and a hook for the pane.
func TestShutdownWithSendStuck(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	cli, hook, watch := dial(t, sock, "cli"), dial(t, sock, "hook"), dial(t, sock, "watch")
	id, fp := sendPane(t, f, cli, watch)
	fp.mu.Lock()
	fp.block = make(chan struct{}) // never closed
	fp.mu.Unlock()
	cli.send(proto.Send{Pane: id, Text: "stuck", Enter: true})
	time.Sleep(50 * time.Millisecond)
	hook.send(proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte("completed")})
	time.Sleep(50 * time.Millisecond)
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown hangs behind a stuck writer")
	}
}

// After a send the pane's agent shows working, so a wait for done waits
// for the reply; the agent's next done ends it.
func TestSendShowsWorking(t *testing.T) {
	sendPause = time.Millisecond
	t.Cleanup(func() { sendPause = 300 * time.Millisecond })
	f := &fakes{statsCalls: map[string]int{}}
	sock, stop := run(t, f)
	defer stop()
	cli, hook, watch := dial(t, sock, "cli"), dial(t, sock, "hook"), dial(t, sock, "watch")
	id, _ := sendPane(t, f, cli, watch)
	activity := func(what string, ok func(model.Activity) bool) {
		t.Helper()
		watch.waitState(what, func(s model.State) bool { return len(s.Activities) == 1 && ok(s.Activities[0]) })
	}
	hook.send(proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte("completed")})
	activity("the last turn done", func(a model.Activity) bool { return a.State == model.StateCompleted })
	if e := cli.request(proto.Send{Pane: id, Text: "next", Enter: true}); e != "" {
		t.Fatal(e)
	}
	activity("prompt sent", func(a model.Activity) bool {
		return a.State == model.StateWorking && a.Detail == "prompt sent" && a.Provider == model.ProviderClaude
	})
	hook.send(proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte("completed")})
	activity("the reply done", func(a model.Activity) bool { return a.State == model.StateCompleted })
}

// A pane whose input channel is full gets nothing, send says the pane is
// not reading, and the pane's send lock is free again.
func TestSendToAPaneNotReading(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	must(t, d.handle(context.Background(), proto.NewSession{Cwd: t.TempDir(), Cmd: []string{"tool"}}))
	d.mu.Lock()
	id, in := d.st.Panes[0].ID, d.inputs[d.st.Panes[0].ID]
	d.mu.Unlock()
	fp := f.pane(0)
	fp.mu.Lock()
	fp.block = make(chan struct{}) // the writer sits in its first Write
	fp.mu.Unlock()
	t.Cleanup(func() { fp.Close() })
	for len(in) < cap(in) {
		d.input(id, []byte("k"))
	}
	if err := d.handle(context.Background(), proto.Send{Pane: id, Text: "hi", Enter: true}); err == nil || !strings.Contains(err.Error(), "not reading") {
		t.Fatalf("send: %v", err)
	}
	if l := d.sendLock(id); !l.TryLock() {
		t.Fatal("the pane's send lock is still held")
	}
}
