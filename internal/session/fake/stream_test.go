package fake

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

func TestExplicitEnvironmentSelection(t *testing.T) {
	real := map[model.Provider]session.Provider{model.ProviderClaude: New(Options{FailStart: true}), model.ProviderPi: New(Options{})}
	for _, value := range []string{"", "0", "true", "fail-start"} {
		t.Setenv("PITWALL_FAKE_PROVIDERS", value)
		got := ProvidersFromEnv(real, func(string) { t.Error("warning without opt-in") })
		if got[model.ProviderClaude] != real[model.ProviderClaude] || got[model.ProviderCodex] != nil {
			t.Fatal("changed real registry")
		}
	}
	t.Setenv("PITWALL_FAKE_PROVIDERS", "1")
	warnings := 0
	got := ProvidersFromEnv(real, func(s string) {
		warnings++
		if !strings.Contains(s, "WARNING") || !strings.Contains(s, "PITWALL_FAKE_PROVIDERS=1") {
			t.Error(s)
		}
	})
	if warnings != 1 || got[model.ProviderPi] != real[model.ProviderPi] || len(real) != 2 {
		t.Fatal("changed unrelated providers or original map")
	}
	for _, kind := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		if got[kind] == nil || got[kind] == real[kind] {
			t.Fatal("did not swap managed driver")
		}
		// Override is sampled for each Start, rather than once for the registry.
		t.Setenv("PITWALL_FAKE_SCRIPT", "fail-start")
		_, err := got[kind].Start(context.Background(), session.Config{Provider: kind}, session.Origin{}, func(session.Event) {})
		var pe *session.ProviderError
		if !errors.As(err, &pe) || pe.Op != "start" {
			t.Fatalf("start error %v", err)
		}
		t.Setenv("PITWALL_FAKE_SCRIPT", "approval")
		ch := make(chan session.Event, 16)
		d, err := got[kind].Start(context.Background(), session.Config{Provider: kind}, session.Origin{}, func(e session.Event) { ch <- e })
		if err != nil {
			t.Fatal(err)
		}
		receive(t, ch)
		receive(t, ch)
		if _, err := d.Send(context.Background(), session.Message{Text: "a different prompt"}); err != nil {
			t.Fatal(err)
		}
		receive(t, ch)
		if e := receive(t, ch); e.Type != session.EventRequestOpened {
			t.Fatal(e)
		}
		if err := d.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := ProvidersFromEnv(nil, func(string) {}); len(got) != 2 {
		t.Fatal("missing managed defaults")
	}
}

func TestPerformanceStreamKnobs(t *testing.T) {
	t.Setenv("AIDE_FAKE_STREAM_TOKENS_PER_SECOND", "20")
	t.Setenv("AIDE_FAKE_STREAM_SECONDS", "0.5")
	t.Setenv("AIDE_FAKE_STREAM_TOOL_EVERY", "2")
	t.Setenv("AIDE_FAKE_STREAM_TOOL_MIX", " Bash, Edit, Read ")
	s, err := streamFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	script := perfScript(s)
	var textEvents int
	var tools []string
	var totalMS int
	for _, step := range script.RuntimeEvents {
		totalMS += step.DelayMS
		if step.Kind == session.KindAssistantText {
			textEvents++
		}
		if step.Tool != nil && step.Tool.Status == "running" {
			tools = append(tools, step.Tool.ToolName)
		}
	}
	if textEvents != 10 || totalMS != 500 || !reflect.DeepEqual(tools, []string{"Bash", "Edit", "Read", "Bash", "Edit"}) {
		t.Fatalf("text %d duration %d tools %v", textEvents, totalMS, tools)
	}
	for _, name := range []string{"AIDE_FAKE_STREAM_TOKENS_PER_SECOND", "AIDE_FAKE_STREAM_SECONDS", "AIDE_FAKE_STREAM_TOOL_EVERY"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "invalid")
			if _, err := streamFromEnv(); err == nil {
				t.Fatal("accepted invalid knob")
			}
		})
	}
	for _, value := range []string{"NaN", "+Inf", "-1"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AIDE_FAKE_STREAM_SECONDS", value)
			if _, err := streamFromEnv(); err == nil {
				t.Fatal("accepted invalid rate")
			}
		})
	}
	t.Setenv("AIDE_FAKE_STREAM_TOOL_EVERY", "0")
	t.Setenv("AIDE_FAKE_STREAM_TOOL_MIX", "")
	s, err = streamFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(perfScript(s).RuntimeEvents) != 11 {
		t.Fatal("did not disable tools")
	}
}

func TestScriptJSONAndValidation(t *testing.T) {
	var s Script
	data := `{"mode":"codex","prompt":"custom","runtimeEvents":[{"type":"request.opened","requestId":"r","delayMs":25,"answers":{"allow":true}},{"type":"turn.completed","state":"completed"}]}`
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var again Script
	if err := json.Unmarshal(b, &again); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s, again) || again.RuntimeEvents[0].DelayMS != 25 || again.RuntimeEvents[0].Answers.Allow == nil {
		t.Fatalf("lost script metadata %s", b)
	}
	for _, steps := range [][]Step{{{DelayMS: -1}}, {{Repeat: -1}}, {{Event: session.Event{Type: session.EventContent}, Answers: &Answers{}}}} {
		if _, err := expand(steps); err == nil {
			t.Fatal("accepted invalid script")
		}
	}
	if _, err := Load("../not-a-script"); err == nil {
		t.Fatal("accepted missing script")
	}
	o := testOptions()
	o.Script = &s
	o.ScriptName = "no-such-script" // explicit fixtures take priority
	d, ch := start(t, session.Config{Provider: model.ProviderCodex}, o)
	receive(t, ch)
	receive(t, ch)
	s.RuntimeEvents[0].DelayMS = 999
	s.RuntimeEvents[0].Answers.Allow = nil
	if _, err := d.Send(context.Background(), session.Message{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	receive(t, ch)
	e := receive(t, ch)
	if e.CreatedAt != "2030-01-01T00:00:00.025Z" {
		t.Fatal(e.CreatedAt)
	}
	if err := d.Approve(context.Background(), e.RequestID, session.Decision{}); err == nil {
		t.Fatal("fixture mutated after Start")
	}
}

// A manual clock proves a long-running stream can be steered before its next
// tick. Pending timers never cause output after Close.
type manualClock struct {
	now       time.Time
	scheduled chan time.Duration
	tick      chan time.Time
}

func (c *manualClock) Now() time.Time                         { return c.now }
func (c *manualClock) After(d time.Duration) <-chan time.Time { c.scheduled <- d; return c.tick }
func TestSteerBeforeStreamTick(t *testing.T) {
	c := &manualClock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), scheduled: make(chan time.Duration, 8), tick: make(chan time.Time, 8)}
	o := testOptions()
	o.Clock = c
	d, ch := start(t, session.Config{Provider: model.ProviderCodex}, o)
	receive(t, ch)
	receive(t, ch)
	if _, err := d.Send(context.Background(), session.Message{Text: "stream-lines"}); err != nil {
		t.Fatal(err)
	}
	receive(t, ch)
	if delay := <-c.scheduled; delay != 15*time.Millisecond {
		t.Fatal(delay)
	}
	if err := d.Steer(context.Background(), session.Message{Text: "stop streaming"}); err != nil {
		t.Fatal(err)
	}
	if e := receive(t, ch); e.Text != "Steered: stop streaming" {
		t.Fatal(e)
	}
	receive(t, ch)
	c.tick <- c.now
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e := receive(t, ch); e.Type != session.EventExited {
		t.Fatal(e)
	}
	select {
	case e := <-ch:
		t.Fatalf("late event %+v", e)
	default:
	}
}

func TestAideRuntimeFixturesReplayWholeLifecycle(t *testing.T) {
	paths, err := filepath.Glob("../testdata/session-deltas/*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var script Script
			if err := json.Unmarshal(b, &script); err != nil {
				t.Fatal(err)
			}
			o := testOptions()
			o.Script = &script
			d, ch := start(t, session.Config{Provider: script.Mode}, o)
			receive(t, ch)
			receive(t, ch)
			turn, err := d.Send(context.Background(), session.Message{Text: script.Prompt})
			if err != nil {
				t.Fatal(err)
			}
			starts := 0
			for _, step := range script.RuntimeEvents {
				e := receive(t, ch)
				if e.Type != step.Type || e.CreatedAt != step.CreatedAt || e.Text != step.Text {
					t.Fatalf("got %+v, want %+v", e, step.Event)
				}
				if e.Type == session.EventTurnStarted {
					starts++
					if e.TurnID != turn {
						t.Fatal("wrong turn id")
					}
				}
			}
			if starts != 1 {
				t.Fatalf("%d turn starts", starts)
			}
			// The original tail has finished, including post-turn ready/usage events.
			if _, err := d.Send(context.Background(), session.Message{Text: script.Prompt}); err != nil {
				t.Fatal(err)
			}
			if err := d.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSendWaitsForPreviousTurnTail(t *testing.T) {
	c := &manualClock{now: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), scheduled: make(chan time.Duration, 8), tick: make(chan time.Time, 8)}
	script := Script{RuntimeEvents: []Step{
		{Event: session.Event{Type: session.EventTurnCompleted, State: session.TurnCompleted}},
		{DelayMS: 50, Event: session.Event{Type: session.EventUsage, InputTokens: 42}},
	}}
	o := testOptions()
	o.Clock = c
	o.Script = &script
	d, ch := start(t, session.Config{Provider: model.ProviderCodex}, o)
	receive(t, ch)
	receive(t, ch)
	if _, err := d.Send(context.Background(), session.Message{Text: "first"}); err != nil {
		t.Fatal(err)
	}
	receive(t, ch)
	<-c.scheduled
	c.tick <- c.now
	if e := receive(t, ch); e.Type != session.EventTurnCompleted {
		t.Fatal(e)
	}
	if delay := <-c.scheduled; delay != 50*time.Millisecond {
		t.Fatal(delay)
	}
	sent := make(chan error, 1)
	go func() { _, err := d.Send(context.Background(), session.Message{Text: "second"}); sent <- err }()
	c.tick <- c.now
	if e := receive(t, ch); e.Type != session.EventUsage {
		t.Fatalf("dropped tail: %+v", e)
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if e := receive(t, ch); e.Type != session.EventTurnStarted {
		t.Fatalf("no new turn: %+v", e)
	}
	if err := d.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseWithCanceledDeadlineStillJoins(t *testing.T) {
	d, ch := start(t, session.Config{Provider: model.ProviderCodex}, testOptions())
	receive(t, ch)
	receive(t, ch)
	if _, err := d.Send(context.Background(), session.Message{Text: "wait-codex"}); err != nil {
		t.Fatal(err)
	}
	receive(t, ch)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if e := receive(t, ch); e.State != session.TurnInterrupted {
		t.Fatal(e)
	}
	if e := receive(t, ch); e.Type != session.EventExited {
		t.Fatal(e)
	}
	if _, err := d.Send(context.Background(), session.Message{}); !errors.Is(err, session.ErrClosed) {
		t.Fatal(err)
	}
}

func TestDefaultPerformanceStream(t *testing.T) {
	script := perfScript(nil)
	text, tools, elapsed := 0, 0, 0
	for _, step := range script.RuntimeEvents {
		elapsed += step.DelayMS
		if step.Kind == session.KindAssistantText {
			text++
		}
		if step.Tool != nil {
			tools++
		}
	}
	if text != 7500 || tools != 100 || elapsed != 150000 {
		t.Fatalf("text %d tool events %d milliseconds %d", text, tools, elapsed)
	}
}
