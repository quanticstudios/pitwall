package fake

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

// Auto-advance only the injected clock. No test waits for elapsed wall time.
type instantClock struct{ now time.Time }

func (c *instantClock) Now() time.Time { return c.now }
func (c *instantClock) After(d time.Duration) <-chan time.Time {
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}
func testOptions() Options {
	return Options{Clock: &instantClock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}, NewID: func() string { return "test" }}
}
func receive(t *testing.T, ch <-chan session.Event) session.Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(10 * time.Second):
		t.Fatal("fake driver did not emit expected event")
		return session.Event{}
	}
}
func start(t *testing.T, cfg session.Config, o Options) (session.Driver, chan session.Event) {
	t.Helper()
	ch := make(chan session.Event, 20000)
	d, err := New(o).Start(context.Background(), cfg, session.Origin{}, func(e session.Event) { ch <- e })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return d, ch
}

// Keep this explicit: adding a scenario to aide requires adding it here too.
var aideScenarios = []string{"approval", "edit approval", "plan approval", "input", "tools", "long transcript", "long transcript wait", "todos", "subagent", "perf-stream", "wait-claude", "wait-codex", "fail-claude", "fail-codex", "edit readme", "stream-lines", "stream-codex", "fail-start", "fleet plan", "fleet consult", "three approvals", "empty reply", "think-stream", "tool kinds", "auth error"}

func TestAideScenarioNames(t *testing.T) {
	for _, name := range aideScenarios {
		if !slices.Contains(Names(), name) {
			t.Errorf("missing aide scenario %q", name)
		}
	}
}

func replay(t *testing.T, name string, provider model.Provider) []session.Event {
	t.Helper()
	o := testOptions()
	o.ScriptName = name
	return replayWith(t, name, provider, o)
}

// replayWith sends name once on a driver from o, allows every approval,
// answers every input, and closes the driver once the turn ends.
func replayWith(t *testing.T, name string, provider model.Provider, o Options) []session.Event {
	t.Helper()
	cfg := session.Config{ID: "chat", Provider: provider, Cwd: t.TempDir()}
	if name == "fail-start" {
		_, err := New(o).Start(context.Background(), cfg, session.Origin{}, func(session.Event) { t.Error("emission from failed start") })
		var pe *session.ProviderError
		if !errors.As(err, &pe) || pe.Message != "Fake managed provider failed to start" {
			t.Fatalf("start error %v", err)
		}
		return nil
	}
	if err := os.WriteFile(filepath.Join(cfg.Cwd, "README.md"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	d, ch := start(t, cfg, o)
	events := []session.Event{receive(t, ch), receive(t, ch)}
	turn, err := d.Send(context.Background(), session.Message{ID: "prompt", Text: name})
	if err != nil {
		t.Fatal(err)
	}
	for {
		e := receive(t, ch)
		events = append(events, e)
		if e.Type == session.EventTurnStarted && e.TurnID != turn {
			t.Fatalf("turn %s != %s", e.TurnID, turn)
		}
		switch e.Type {
		case session.EventRequestOpened:
			if err := d.Approve(context.Background(), e.RequestID, session.Decision{Allow: true}); err != nil {
				t.Fatal(err)
			}
		case session.EventInputRequested:
			if err := d.Answer(context.Background(), e.RequestID, map[string][]string{"prompt": {"continue"}}); err != nil {
				t.Fatal(err)
			}
		}
		// The wait variants intentionally have no ending event in the script.
		if name == "wait-"+string(provider) && e.Type == session.EventTurnStarted || name == "long transcript wait" && e.ItemID == turn+"/long-text-999" {
			if _, err := d.Send(context.Background(), session.Message{Text: "queued"}); !errors.Is(err, session.ErrBusy) {
				t.Fatalf("busy error %v", err)
			}
			if err := d.Interrupt(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		if e.Type == session.EventTurnCompleted {
			break
		}
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	events = append(events, receive(t, ch))
	select {
	case e := <-ch:
		t.Fatalf("extra event after Close: %+v", e)
	default:
	}
	if name == "edit readme" {
		b, err := os.ReadFile(filepath.Join(cfg.Cwd, "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "before\nedited by the turn\n" {
			t.Fatalf("README: %q", b)
		}
	}
	return events
}

func TestEveryScenarioReplaysDeterministically(t *testing.T) {
	for _, name := range Names() {
		for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
			t.Run(string(provider)+"/"+name, func(t *testing.T) {
				a, b := replay(t, name, provider), replay(t, name, provider)
				if !reflect.DeepEqual(a, b) {
					t.Fatal("replay changed events or timestamps")
				}
				for _, e := range a {
					if e.CreatedAt == "" {
						t.Fatal("missing timestamp")
					}
				}
			})
		}
	}
}

func TestReplayContentsAndTiming(t *testing.T) {
	for _, tc := range []struct {
		name     string
		count    int
		duration time.Duration
		contains string
	}{
		{"stream-lines", 300, 4500 * time.Millisecond, "line 299\n\n"},
		{"stream-codex", 121, 1200 * time.Millisecond, "119 complete"},
		{"reasoning", 3, 60 * time.Millisecond, "Checking the request. Ready.Done."},
		{"long transcript", 2000, 0, "Step 999 line 3: checked the next file."},
		{"subagent", 5, 400 * time.Millisecond, "The subagent finished exploring."},
		{"tools", 7, 400 * time.Millisecond, "Ran three tools."},
		{"tool kinds", 13, 3600 * time.Millisecond, "Ran every tool kind."},
		{"think-stream", 31, 3100 * time.Millisecond, "step 14 \n\nChecking the latest idea. step 16"},
		{"empty reply", 0, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := replay(t, tc.name, model.ProviderCodex)
			var content []session.Event
			var text strings.Builder
			for _, e := range events {
				if e.Type == session.EventContent {
					content = append(content, e)
					text.WriteString(e.Text)
				}
			}
			if len(content) != tc.count || !strings.Contains(text.String(), tc.contains) {
				t.Fatalf("%d events, contents missing %q", len(content), tc.contains)
			}
			first, _ := time.Parse(time.RFC3339Nano, events[0].CreatedAt)
			last, _ := time.Parse(time.RFC3339Nano, events[len(events)-1].CreatedAt)
			if last.Sub(first) != tc.duration {
				t.Fatalf("duration %s want %s", last.Sub(first), tc.duration)
			}
		})
	}
}

func TestFakeEventsReduceAndRoundTripDeltas(t *testing.T) {
	// Exercise the same exported provider that eventlog and socket tests can use.
	// JSON round-trips stand in for their persistence/wire boundaries.
	for _, name := range []string{"approval", "edit approval", "plan approval", "input", "tools", "todos", "subagent", "reasoning", "limit", "error"} {
		t.Run(name, func(t *testing.T) {
			events := replay(t, name, model.ProviderCodex)
			snapshot := session.Snapshot{SessionID: "chat", Provider: model.ProviderCodex, SessionState: session.StateConnecting}
			var seq int64
			kinds := map[session.DeltaType]bool{}
			for _, e := range events {
				ds := session.Reduce(snapshot, e)
				revision := snapshot.Revision + 1
				for _, delta := range ds {
					seq++
					delta.Version = session.DeltaVersion
					delta.SessionID = "chat"
					delta.Seq = seq
					delta.Revision = revision
					delta.UpdatedAt = e.CreatedAt
					if delta.Snapshot != nil {
						delta.Snapshot.Revision = revision
						delta.Snapshot.UpdatedAt = e.CreatedAt
					}
					b, err := json.Marshal(delta)
					if err != nil {
						t.Fatal(err)
					}
					var wire session.Delta
					if err := json.Unmarshal(b, &wire); err != nil {
						t.Fatal(err)
					}
					snapshot, err = session.Apply(&snapshot, seq-1, wire)
					if err != nil {
						t.Fatal(err)
					}
					kinds[delta.Type] = true
				}
			}
			if len(snapshot.Turns) != 1 || len(snapshot.PendingApprovals) != 0 || len(snapshot.PendingUserInputs) != 0 {
				t.Fatalf("unfinished snapshot %+v", snapshot)
			}
			required := map[string][]session.DeltaType{
				"tools":     {session.DeltaToolStart, session.DeltaToolEnd},
				"todos":     {session.DeltaPlanUpdate},
				"approval":  {session.DeltaApprovalAdd, session.DeltaApprovalResolve},
				"input":     {session.DeltaInputAdd, session.DeltaInputResolve},
				"reasoning": {session.DeltaReasoningAppend},
				"error":     {session.DeltaError},
			}
			for _, kind := range required[name] {
				if !kinds[kind] {
					t.Errorf("missing delta %s", kind)
				}
			}
		})
	}
}

func TestRequestsValidateResponsesAndResolveOnce(t *testing.T) {
	for _, input := range []bool{false, true} {
		t.Run(map[bool]string{false: "approval", true: "input"}[input], func(t *testing.T) {
			allow := true
			e := session.Event{Type: session.EventRequestOpened, RequestID: "r", RequestType: session.RequestCommand}
			expected := &Answers{Allow: &allow}
			if input {
				e.Type = session.EventInputRequested
				expected = &Answers{Input: map[string][]string{"q": {"yes"}}}
			}
			o := testOptions()
			o.Script = &Script{RuntimeEvents: []Step{{Event: e, Answers: expected}, {Event: session.Event{Type: session.EventTurnCompleted, State: session.TurnCompleted}}}}
			d, ch := start(t, session.Config{Provider: model.ProviderCodex}, o)
			receive(t, ch)
			receive(t, ch)
			if _, err := d.Send(context.Background(), session.Message{Text: "anything"}); err != nil {
				t.Fatal(err)
			}
			receive(t, ch)
			request := receive(t, ch)
			if err := d.Approve(context.Background(), "missing", session.Decision{}); err == nil {
				t.Fatal("accepted missing request")
			}
			if input {
				if err := d.Approve(context.Background(), request.RequestID, session.Decision{}); err == nil {
					t.Fatal("accepted wrong response kind")
				}
				if err := d.Answer(context.Background(), request.RequestID, map[string][]string{"q": {"no"}}); err == nil {
					t.Fatal("accepted wrong answer")
				}
				if err := d.Answer(context.Background(), request.RequestID, map[string][]string{"q": {"yes"}}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := d.Answer(context.Background(), request.RequestID, nil); err == nil {
					t.Fatal("accepted wrong response kind")
				}
				if err := d.Approve(context.Background(), request.RequestID, session.Decision{}); err == nil {
					t.Fatal("accepted wrong decision")
				}
				if err := d.Approve(context.Background(), request.RequestID, session.Decision{Allow: true}); err != nil {
					t.Fatal(err)
				}
			}
			receive(t, ch)
			receive(t, ch)
			if err := d.Approve(context.Background(), request.RequestID, session.Decision{Allow: true}); err == nil {
				t.Fatal("accepted second response")
			}
		})
	}
}

func TestInterruptSteerSettingsAndTurnIDs(t *testing.T) {
	d, ch := start(t, session.Config{Provider: model.ProviderClaude}, testOptions())
	receive(t, ch)
	receive(t, ch)
	if err := d.Steer(context.Background(), session.Message{}); !errors.Is(err, session.ErrIdle) {
		t.Fatal(err)
	}
	if err := d.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := d.Set(context.Background(), session.Settings{Model: "fake-model", Effort: "high", Mode: "plan"}); err != nil {
		t.Fatal(err)
	}
	a, err := d.Send(context.Background(), session.Message{Text: "wait-claude"})
	if err != nil {
		t.Fatal(err)
	}
	receive(t, ch)
	if err := d.Steer(context.Background(), session.Message{Text: "new direction"}); err != nil {
		t.Fatal(err)
	}
	e := receive(t, ch)
	if e.Text != "Steered: new direction" || e.Model != "fake-model" {
		t.Fatalf("steer %+v", e)
	}
	receive(t, ch)
	b, err := d.Send(context.Background(), session.Message{Text: "approval"})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("reused turn id")
	}
	receive(t, ch)
	request := receive(t, ch)
	if err := d.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	e = receive(t, ch)
	if e.Type != session.EventRequestResolved || e.RequestID != request.RequestID {
		t.Fatalf("unresolved approval %+v", e)
	}
	e = receive(t, ch)
	if e.State != session.TurnInterrupted {
		t.Fatalf("completion %+v", e)
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	receive(t, ch)
	if _, err := d.Send(context.Background(), session.Message{}); !errors.Is(err, session.ErrClosed) {
		t.Fatalf("closed: %v", err)
	}
}

func TestConcurrentCommands(t *testing.T) {
	d, ch := start(t, session.Config{Provider: model.ProviderCodex}, testOptions())
	receive(t, ch)
	receive(t, ch)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			_, err := d.Send(context.Background(), session.Message{Text: "wait-codex"})
			if err != nil && !errors.Is(err, session.ErrBusy) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	receive(t, ch)
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e := receive(t, ch); e.State != session.TurnInterrupted {
		t.Fatalf("completion %+v", e)
	}
	if e := receive(t, ch); e.Type != session.EventExited {
		t.Fatalf("exit %+v", e)
	}
}

func TestScriptSelectionAndCancellation(t *testing.T) {
	d, ch := start(t, session.Config{Provider: model.ProviderCodex}, testOptions())
	receive(t, ch)
	receive(t, ch)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Send(ctx, session.Message{Text: "wait-codex"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, tc := range []struct{ prompt, reply string }{{"hello \"world\"\nwith newline", "Handled: hello \"world\"\nwith newline"}, {"tools extra context", "Ran three tools."}, {"todos\n\nWorkspace evidence. blah", "Tracking three tasks."}} {
		if _, err := d.Send(context.Background(), session.Message{Text: tc.prompt}); err != nil {
			t.Fatal(err)
		}
		found := false
		for {
			e := receive(t, ch)
			if e.Text == tc.reply {
				found = true
			}
			if e.Type == session.EventTurnCompleted {
				break
			}
		}
		if !found {
			t.Errorf("missing reply %q", tc.reply)
		}
	}
}

func TestStartOriginAndIDUniqueness(t *testing.T) {
	var previous string
	for _, from := range []session.Origin{{}, {Resume: "existing"}, {Resume: "existing", Fork: true}} {
		ch := make(chan session.Event, 8)
		d, err := New(Options{}).Start(context.Background(), session.Config{Provider: model.ProviderCodex}, from, func(e session.Event) { ch <- e })
		if err != nil {
			t.Fatal(err)
		}
		id := receive(t, ch).ResumeID
		if from.Resume != "" && !from.Fork {
			if id != "existing" {
				t.Fatal(id)
			}
		} else if id == "" || id == previous || id == "existing" {
			t.Fatalf("not a fresh resume id %q", id)
		}
		previous = id
		if err := d.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(Options{}).Start(ctx, session.Config{}, session.Origin{}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, cfg := range []session.Config{{}, {Provider: model.ProviderCodex}} {
		if _, err := New(Options{}).Start(context.Background(), cfg, session.Origin{}, nil); err == nil {
			t.Fatal("accepted invalid start")
		}
	}
	if _, err := New(Options{ScriptName: "no-such-script"}).Start(context.Background(), session.Config{Provider: model.ProviderCodex}, session.Origin{}, func(session.Event) {}); err == nil {
		t.Fatal("accepted missing script")
	}
}

func TestQueuedApprovalsDenialReasonAndUsageLimit(t *testing.T) {
	d, ch := start(t, session.Config{ID: "chat", Provider: model.ProviderClaude, Cwd: t.TempDir()}, testOptions())
	receive(t, ch)
	receive(t, ch)
	if _, err := d.Send(context.Background(), session.Message{Text: "three approvals"}); err != nil {
		t.Fatal(err)
	}
	receive(t, ch) // turn.started
	var open []string
	for range 3 {
		e := receive(t, ch)
		if e.Type != session.EventRequestOpened {
			t.Fatalf("got %s, want all three requests open before any response", e.Type)
		}
		open = append(open, e.RequestID)
	}
	if err := d.Approve(context.Background(), open[0], session.Decision{Allow: true}); err != nil {
		t.Fatal(err)
	}
	if e := receive(t, ch); e.Type != session.EventRequestResolved || e.RequestID != open[0] {
		t.Fatalf("got %+v", e)
	}
	if err := d.Approve(context.Background(), open[1], session.Decision{Reason: "Print it with printf instead."}); err != nil {
		t.Fatal(err)
	}
	receive(t, ch)
	if e := receive(t, ch); e.Text != "claude got the denial reason: Print it with printf instead." {
		t.Fatalf("got %+v", e)
	}
	select {
	case e := <-ch:
		t.Fatalf("turn went on with a request still open: %+v", e)
	default:
	}
	if err := d.Approve(context.Background(), open[2], session.Decision{}); err != nil {
		t.Fatal(err)
	}
	receive(t, ch)
	if e := receive(t, ch); e.Type != session.EventTurnCompleted || e.State != session.TurnCompleted {
		t.Fatalf("got %+v", e)
	}

	if _, err := d.Send(context.Background(), session.Message{Text: "usage limit 1893456000"}); err != nil {
		t.Fatal(err)
	}
	receive(t, ch)
	limit := receive(t, ch)
	want := time.Unix(1893456000, 0).UTC()
	if limit.Type != session.EventLimit || !limit.Limit.Rejected || !limit.Limit.Resets.Equal(want) || len(limit.Limit.Windows) != 1 || limit.Limit.Windows[0].Minutes != 300 {
		t.Fatalf("got %+v", limit)
	}
	if e := receive(t, ch); e.Type != session.EventTurnCompleted || e.State != session.TurnFailed || e.ErrorMessage != "You've hit your usage limit." {
		t.Fatalf("got %+v", e)
	}
}

func TestPerStartFailure(t *testing.T) {
	p := New(testOptions())
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		for _, failure := range []string{"missing-cli", "fail-start"} {
			emitted := false
			drv, err := p.Start(context.Background(), session.Config{Provider: provider, FakeStartFailure: failure}, session.Origin{}, func(session.Event) { emitted = true })
			if err == nil || drv != nil || emitted {
				t.Fatalf("failure %q: %v %v", failure, drv, err)
			}
			if failure == "missing-cli" && !strings.Contains(err.Error(), "was not found in PATH") {
				t.Fatal(err)
			}
		}
		drv, err := p.Start(context.Background(), session.Config{Provider: provider}, session.Origin{}, func(session.Event) {})
		if err != nil {
			t.Fatalf("another start inherited failure: %v", err)
		}
		if err := drv.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}
