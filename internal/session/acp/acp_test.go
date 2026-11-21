package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/testenv"
)

type step struct {
	Receive json.RawMessage   `json:"receive"`
	Send    []json.RawMessage `json:"send"`
}

// The test executable is a strict scripted ACP agent, following the Claude
// driver's subprocess tests. No real agent, shell or network is involved.
func TestMain(m *testing.M) {
	if path := os.Getenv("PITWALL_TEST_ACP_SCRIPT"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			os.Exit(2)
		}
		var script []step
		if json.Unmarshal(b, &script) != nil {
			os.Exit(2)
		}
		in := bufio.NewReader(os.Stdin)
		for _, s := range script {
			line, err := in.ReadBytes('\n')
			if err != nil {
				os.Exit(3)
			}
			var got, want any
			if json.Unmarshal(line, &got) != nil || json.Unmarshal(s.Receive, &want) != nil || !reflect.DeepEqual(got, want) {
				fmt.Fprintf(os.Stderr, "unexpected ACP input: %s; expected %s", line, s.Receive)
				os.Exit(4)
			}
			for _, out := range s.Send {
				fmt.Fprintln(os.Stdout, string(out))
			}
		}
		if os.Getenv("PITWALL_TEST_ACP_CRASH") == "1" {
			fmt.Fprint(os.Stderr, "scripted crash")
			os.Exit(7)
		}
		if os.Getenv("PITWALL_TEST_ACP_HANG") == "1" {
			for {
				time.Sleep(time.Second)
			}
		}
		if _, err := in.ReadBytes('\n'); err != io.EOF {
			fmt.Fprint(os.Stderr, "unexpected trailing input")
			os.Exit(5)
		}
		return
	}
	testenv.Main(m)
}

func script(t *testing.T) []step {
	t.Helper()
	b, err := os.ReadFile("testdata/approval.json")
	if err != nil {
		t.Fatal(err)
	}
	var s []step
	if err = json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
func rpc(id any, method string, params any) json.RawMessage {
	return raw(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
}
func result(id any, v any) json.RawMessage {
	return raw(map[string]any{"jsonrpc": "2.0", "id": id, "result": v})
}
func notification(method string, p any) json.RawMessage {
	return raw(map[string]any{"jsonrpc": "2.0", "method": method, "params": p})
}

type harness struct {
	d      session.Driver
	events chan session.Event
	all    []session.Event
}

func start(t *testing.T, steps []step, from session.Origin, extra ...string) (*harness, error) {
	t.Helper()
	return startWith(t, steps, session.Config{}, from, extra...)
}

func startWith(t *testing.T, steps []step, cfg session.Config, from session.Origin, extra ...string) (*harness, error) {
	t.Helper()
	dir := t.TempDir()
	cfg.Cwd = dir
	stepsBytes := bytes.ReplaceAll(raw(steps), []byte(`"CWD"`), raw(dir))
	path := filepath.Join(dir, "script.json")
	if err := os.WriteFile(path, stepsBytes, 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{events: make(chan session.Event, 128)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.d, err = (Provider{Command: exe, Args: []string{"--scripted-acp"}, Env: append([]string{"PITWALL_TEST_ACP_SCRIPT=" + path}, extra...)}).Start(ctx, cfg, from, func(e session.Event) { h.events <- e })
	if err == nil {
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			h.d.Close(ctx)
		})
	}
	return h, err
}
func (h *harness) wait(t *testing.T, typ session.EventType) session.Event {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case e := <-h.events:
			h.all = append(h.all, e)
			if e.Type == typ {
				return e
			}
			if e.Type == session.EventExited {
				t.Fatalf("agent exited before %s: %+v", typ, h.all)
			}
		case <-deadline.C:
			t.Fatalf("timeout waiting for %s; events: %+v", typ, h.all)
		}
	}
}

func TestScriptedSession(t *testing.T) {
	h, err := start(t, script(t), session.Origin{})
	if err != nil {
		t.Fatal(err)
	}
	e := h.wait(t, session.EventResumeID)
	if e.ResumeID != "s1" || e.Version != "test" || !reflect.DeepEqual(e.Capabilities, []string{"session/load"}) {
		t.Fatalf("resume event: %+v", e)
	}
	if _, err = h.d.Send(context.Background(), session.Message{ID: "turn-1", Text: "fix it"}); err != nil {
		t.Fatal(err)
	}
	e = h.wait(t, session.EventRequestOpened)
	if e.RequestType != session.RequestFileRead || e.Request.ToolName != "Read file" || string(e.Request.Input) != `{"path":"/test/file"}` {
		t.Fatalf("approval: %+v", e)
	}
	if _, err = h.d.Send(context.Background(), session.Message{Text: "busy"}); !errors.Is(err, session.ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	if err = h.d.Steer(context.Background(), session.Message{Text: "steer"}); !errors.Is(err, session.ErrUnsupported) {
		t.Fatal(err)
	}
	for _, d := range []session.Decision{{Allow: true, ForSession: true}, {Allow: true, Input: raw(map[string]string{"path": "edited"})}, {Reason: "no"}} {
		if err = h.d.Approve(context.Background(), e.RequestID, d); !errors.Is(err, session.ErrUnsupported) {
			t.Fatalf("unsupported decision: %v", err)
		}
	}
	if err = h.d.Approve(context.Background(), e.RequestID, session.Decision{Allow: true}); err != nil {
		t.Fatal(err)
	}
	if err = h.d.Approve(context.Background(), e.RequestID, session.Decision{Allow: true}); err == nil {
		t.Fatal("accepted duplicate approval")
	}
	e = h.wait(t, session.EventTurnCompleted)
	if e.State != session.TurnCompleted || e.TurnID != "turn-1" {
		t.Fatalf("turn: %+v", e)
	}
	var snap session.Snapshot
	var seq int64
	for _, e := range h.all {
		if e.CreatedAt == "" {
			t.Fatal("unstamped event")
		}
		for _, d := range session.Reduce(snap, e) {
			seq++
			d.Version = session.DeltaVersion
			d.Seq = seq
			d.Revision = seq
			d.UpdatedAt = e.CreatedAt
			snap, err = session.Apply(&snap, seq-1, d)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	var texts []string
	var tool *session.ToolCall
	for _, i := range snap.Content {
		if i.Kind == session.KindAssistantText {
			texts = append(texts, i.Text)
		}
		if i.Tool != nil {
			tool = i.Tool
		}
	}
	if !reflect.DeepEqual(snap.Commands, []session.Command{{Name: "review", Description: "Review the diff", Hint: "[target]"}}) {
		t.Fatalf("commands: %+v", snap.Commands)
	}
	if !reflect.DeepEqual(texts, []string{"I will read it.", "Done."}) || tool == nil || tool.Status != "completed" || tool.Output != "file text" || string(tool.Input) != `{"path":"/test/file"}` || len(snap.PendingApprovals) != 0 {
		t.Fatalf("snapshot: %+v; tool: %+v", snap, tool)
	}
	// An earlier event's tool must stay pending after the later update.
	for _, e := range h.all {
		if e.Tool != nil {
			if e.Tool.Status != "running" {
				t.Fatal("earlier tool mutated")
			}
			break
		}
	}
}

func TestResumeAndCapabilities(t *testing.T) {
	for _, tc := range []string{"load", "no-load", "version", "rpc-error", "missing-id"} {
		t.Run(tc, func(t *testing.T) {
			steps := script(t)[:2]
			from := session.Origin{}
			wantUnsupported := false
			switch tc {
			case "load":
				from.Resume = "s1"
				steps[1].Receive = rpc(2, "session/load", map[string]any{"sessionId": "s1", "cwd": "CWD", "mcpServers": []any{}})
				// Replayed history during load must not duplicate the saved transcript.
				steps[1].Send = []json.RawMessage{notification("session/update", map[string]any{"sessionId": "s1", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "old"}}}), result(2, map[string]any{})}
			case "no-load":
				from.Resume = "s1"
				steps = steps[:1]
				steps[0].Send = []json.RawMessage{result(1, map[string]any{"protocolVersion": 1})}
				wantUnsupported = true
			case "version":
				steps = steps[:1]
				steps[0].Send = []json.RawMessage{result(1, map[string]any{"protocolVersion": 2})}
				wantUnsupported = true
			case "rpc-error":
				steps[1].Send = []json.RawMessage{raw(map[string]any{"jsonrpc": "2.0", "id": 2, "error": map[string]any{"code": -32000, "message": "authentication required"}})}
			case "missing-id":
				steps[1].Send = []json.RawMessage{result(2, map[string]any{})}
			}
			h, err := start(t, steps, from)
			if tc == "load" {
				if err != nil {
					t.Fatal(err)
				}
				h.wait(t, session.EventState)
				for _, e := range h.all {
					if e.Type == session.EventContent {
						t.Fatalf("replayed: %+v", e)
					}
				}
				return
			}
			if err == nil || wantUnsupported && !errors.Is(err, session.ErrUnsupported) {
				t.Fatalf("start: %v", err)
			}
			if tc == "rpc-error" {
				var pe *session.ProviderError
				if !errors.As(err, &pe) || pe.Message != "authentication required" {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestNonResumableAndUnsupported(t *testing.T) {
	steps := script(t)[:2]
	steps[0].Send = []json.RawMessage{result(1, map[string]any{"protocolVersion": 1})}
	h, err := start(t, steps, session.Origin{})
	if err != nil {
		t.Fatal(err)
	}
	e := h.wait(t, session.EventResumeID)
	if !reflect.DeepEqual(e.Capabilities, []string{"non-resumable"}) {
		t.Fatal(e)
	}
	if e = h.wait(t, session.EventContent); !strings.Contains(e.Text, "not resumable") {
		t.Fatal(e)
	}
	if _, err = h.d.Send(context.Background(), session.Message{Images: []session.Image{{MimeType: "image/png", Data: []byte{1}}}}); !errors.Is(err, session.ErrUnsupported) {
		t.Fatal(err)
	}
	if err = h.d.Steer(context.Background(), session.Message{}); !errors.Is(err, session.ErrIdle) {
		t.Fatal(err)
	}
	if err = h.d.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	var pe *session.ProviderError
	if err = h.d.Answer(context.Background(), "x", nil); !errors.As(err, &pe) {
		t.Fatal(err)
	}
	if err = h.d.Set(context.Background(), session.Settings{Mode: "auto"}); !errors.Is(err, session.ErrUnsupported) {
		t.Fatal(err)
	}
	if err = h.d.Set(context.Background(), session.Settings{}); err != nil {
		t.Fatal(err)
	}
	for _, from := range []session.Origin{{Fork: true}, {At: "point"}} {
		if _, err = (Provider{}).Start(context.Background(), session.Config{}, from, func(session.Event) {}); !errors.Is(err, session.ErrUnsupported) {
			t.Fatal(err)
		}
	}
}

func TestInterruptCancelsPermission(t *testing.T) {
	steps := script(t)
	steps[3] = step{Receive: notification("session/cancel", map[string]any{"sessionId": "s1"})}
	steps = append(steps, step{Receive: result("approval-1", map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}), Send: []json.RawMessage{result(3, map[string]string{"stopReason": "cancelled"})}})
	h, err := start(t, steps, session.Origin{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.d.Send(context.Background(), session.Message{Text: "fix it"}); err != nil {
		t.Fatal(err)
	}
	h.wait(t, session.EventRequestOpened)
	if err = h.d.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e := h.wait(t, session.EventTurnCompleted); e.State != session.TurnInterrupted {
		t.Fatal(e)
	}
}

func TestCrashAndClose(t *testing.T) {
	t.Run("crash", func(t *testing.T) {
		steps := script(t)[:3]
		steps[2].Send = nil
		h, err := start(t, steps, session.Origin{}, "PITWALL_TEST_ACP_CRASH=1")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = h.d.Send(context.Background(), session.Message{Text: "fix it"}); err != nil {
			t.Fatal(err)
		}
		if e := h.wait(t, session.EventError); !strings.Contains(e.Message, "scripted crash") {
			t.Fatal(e)
		}
		if e := h.wait(t, session.EventExited); e.ExitCode == nil || *e.ExitCode != 7 {
			t.Fatal(e)
		}
		if _, err = h.d.Send(context.Background(), session.Message{}); !errors.Is(err, session.ErrClosed) {
			t.Fatal(err)
		}
	})
	t.Run("kill", func(t *testing.T) {
		h, err := start(t, script(t)[:2], session.Origin{}, "PITWALL_TEST_ACP_HANG=1")
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err = h.d.Close(ctx); err != nil {
			t.Fatal(err)
		}
		count := len(h.events)
		time.Sleep(10 * time.Millisecond)
		if len(h.events) != count {
			t.Fatal("events after Close")
		}
		if err = h.d.Close(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func TestImagesAndRepeatedTurns(t *testing.T) {
	steps := script(t)[:2]
	steps = append(steps, step{Receive: rpc(3, "session/prompt", map[string]any{"sessionId": "s1", "prompt": []any{map[string]string{"type": "text", "text": "look"}, map[string]string{"type": "image", "mimeType": "image/png", "data": "AQI="}}}), Send: []json.RawMessage{result(3, map[string]string{"stopReason": "end_turn"})}}, step{Receive: rpc(4, "session/prompt", map[string]any{"sessionId": "s1", "prompt": []any{map[string]string{"type": "text", "text": "again"}}}), Send: []json.RawMessage{result(4, map[string]string{"stopReason": "refusal"})}})
	h, err := start(t, steps, session.Origin{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := h.d.Send(context.Background(), session.Message{Text: "look", Images: []session.Image{{MimeType: "image/png", Data: []byte{1, 2}}}})
	if err != nil || id == "" {
		t.Fatalf("send: %s %v", id, err)
	}
	if e := h.wait(t, session.EventTurnCompleted); e.State != session.TurnCompleted || e.TurnID != id {
		t.Fatal(e)
	}
	next, err := h.d.Send(context.Background(), session.Message{Text: "again"})
	if err != nil || next == id {
		t.Fatalf("second turn: %s %v", next, err)
	}
	h.wait(t, session.EventTurnCompleted)
}

func TestPermissionOptions(t *testing.T) {
	for _, tc := range []struct {
		kind     string
		decision session.Decision
	}{{"reject_once", session.Decision{}}, {"allow_always", session.Decision{Allow: true, ForSession: true}}, {"reject_always", session.Decision{ForSession: true}}} {
		t.Run(tc.kind, func(t *testing.T) {
			steps := script(t)
			steps[2].Send = []json.RawMessage{rpc(42, "session/request_permission", map[string]any{"sessionId": "s1", "toolCall": map[string]string{"toolCallId": "t1", "title": "Action", "kind": "execute"}, "options": []map[string]string{{"optionId": "chosen", "name": "Choice", "kind": tc.kind}}})}
			steps[3] = step{Receive: result(42, map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": "chosen"}}), Send: []json.RawMessage{result(3, map[string]string{"stopReason": "end_turn"})}}
			h, err := start(t, steps, session.Origin{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = h.d.Send(context.Background(), session.Message{Text: "fix it"}); err != nil {
				t.Fatal(err)
			}
			e := h.wait(t, session.EventRequestOpened)
			if e.RequestType != session.RequestCommand {
				t.Fatal(e)
			}
			if err = h.d.Approve(context.Background(), e.RequestID, tc.decision); err != nil {
				t.Fatal(err)
			}
			h.wait(t, session.EventTurnCompleted)
		})
	}
}

func TestPromptErrorAndLatePermission(t *testing.T) {
	t.Run("error", func(t *testing.T) {
		steps := script(t)[:3]
		steps[2].Send = []json.RawMessage{raw(map[string]any{"jsonrpc": "2.0", "id": 3, "error": map[string]any{"code": -32000, "message": "model unavailable"}})}
		h, err := start(t, steps, session.Origin{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = h.d.Send(context.Background(), session.Message{Text: "fix it"}); err != nil {
			t.Fatal(err)
		}
		if e := h.wait(t, session.EventTurnCompleted); e.State != session.TurnFailed || e.ErrorMessage != "model unavailable" {
			t.Fatal(e)
		}
	})
	t.Run("late permission", func(t *testing.T) {
		steps := script(t)[:3]
		request := steps[2].Send[len(steps[2].Send)-1]
		steps[2].Send = nil
		steps = append(steps, step{Receive: notification("session/cancel", map[string]string{"sessionId": "s1"}), Send: []json.RawMessage{request}}, step{Receive: result("approval-1", map[string]any{"outcome": map[string]string{"outcome": "cancelled"}}), Send: []json.RawMessage{result(3, map[string]string{"stopReason": "cancelled"})}})
		h, err := start(t, steps, session.Origin{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = h.d.Send(context.Background(), session.Message{Text: "fix it"}); err != nil {
			t.Fatal(err)
		}
		if err = h.d.Interrupt(context.Background()); err != nil {
			t.Fatal(err)
		}
		h.wait(t, session.EventTurnCompleted)
		for _, e := range h.all {
			if e.Type == session.EventRequestOpened {
				t.Fatal("late permission opened after cancellation")
			}
		}
	})
}

func TestStartValidationAndContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (Provider{Command: "missing-command"}).Start(ctx, session.Config{Cwd: t.TempDir()}, session.Origin{}, func(session.Event) {})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_, err = (Provider{Command: "missing-command"}).Start(context.Background(), session.Config{Cwd: "relative"}, session.Origin{}, func(session.Event) {})
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatal(err)
	}
}

func TestInitializationDeadline(t *testing.T) {
	dir := t.TempDir()
	steps := script(t)[:1]
	steps[0].Send = nil
	path := filepath.Join(dir, "silent.json")
	if err := os.WriteFile(path, raw(steps), 0600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = (Provider{Command: exe, Args: []string{"--scripted-acp"}, Env: []string{"PITWALL_TEST_ACP_SCRIPT=" + path}}).Start(ctx, session.Config{Cwd: dir}, session.Origin{}, func(session.Event) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("initialization deadline: %v", err)
	}
}

// snapshot reduces and applies events as the chat loop does.
func snapshot(t *testing.T, events []session.Event) session.Snapshot {
	t.Helper()
	snap := session.Snapshot{SessionID: "c1"}
	for _, e := range events {
		for _, d := range session.Reduce(snap, e) {
			d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = session.DeltaVersion, snap.SessionID, snap.Revision+1, snap.Revision+1, e.CreatedAt
			n, err := session.Apply(&snap, snap.Revision, d)
			if err != nil {
				t.Fatal(err)
			}
			snap = n
		}
	}
	return snap
}

func modelOption(current string) map[string]any {
	return map[string]any{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": current,
		"options": []map[string]string{{"value": "acme-small", "name": "Small"}, {"value": "acme-large", "name": "Large"}}}
}

// Commands, modes and the model option: what session/new advertises and
// later updates reach the snapshot, and Set uses the methods the agent
// advertised.
func TestCommandsAndSettings(t *testing.T) {
	update := func(u map[string]any) json.RawMessage {
		return notification("session/update", map[string]any{"sessionId": "s1", "update": u})
	}
	steps := script(t)[:2]
	steps[1].Send = []json.RawMessage{
		update(map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{
			map[string]any{"name": "review", "description": "Review the diff", "input": map[string]string{"hint": "[target]"}},
			map[string]any{"name": "init", "description": "Write AGENTS.md"},
		}}),
		result(2, map[string]any{"sessionId": "s1",
			"modes":         map[string]any{"currentModeId": "default", "availableModes": []map[string]string{{"id": "default", "name": "Default"}, {"id": "plan", "name": "Plan"}}},
			"configOptions": []any{modelOption("acme-small")}}),
	}
	steps = append(steps,
		step{Receive: rpc(3, "session/set_mode", map[string]string{"sessionId": "s1", "modeId": "plan"}),
			Send: []json.RawMessage{result(3, map[string]any{}), update(map[string]any{"sessionUpdate": "current_mode_update", "currentModeId": "plan"})}},
		step{Receive: rpc(4, "session/set_config_option", map[string]string{"sessionId": "s1", "configId": "model", "value": "acme-large"}),
			Send: []json.RawMessage{result(4, map[string]any{"configOptions": []any{modelOption("acme-large")}})}},
	)
	h, err := start(t, steps, session.Origin{})
	if err != nil {
		t.Fatal(err)
	}
	if e := h.wait(t, session.EventSettings); e.Mode != "default" || e.Model != "acme-small" {
		t.Fatalf("advertised settings: %+v", e)
	}
	if err = h.d.Set(context.Background(), session.Settings{Mode: "plan"}); err != nil {
		t.Fatal(err)
	}
	if e := h.wait(t, session.EventSettings); e.Mode != "plan" {
		t.Fatalf("mode update: %+v", e)
	}
	// Already current: nothing is sent.
	if err = h.d.Set(context.Background(), session.Settings{Mode: "plan", Model: "acme-small"}); err != nil {
		t.Fatal(err)
	}
	if err = h.d.Set(context.Background(), session.Settings{Model: "acme-large"}); err != nil {
		t.Fatal(err)
	}
	h.wait(t, session.EventSettings)
	if err = h.d.Set(context.Background(), session.Settings{Effort: "high"}); !errors.Is(err, session.ErrUnsupported) {
		t.Fatalf("effort without a thought_level option: %v", err)
	}
	snap := snapshot(t, h.all)
	want := []session.Command{{Name: "review", Description: "Review the diff", Hint: "[target]"}, {Name: "init", Description: "Write AGENTS.md"}}
	if !reflect.DeepEqual(snap.Commands, want) || snap.PermissionMode != "plan" || snap.InteractionMode != "plan" || snap.Model == nil || *snap.Model != "acme-large" {
		t.Fatalf("snapshot: commands %+v mode %q model %v", snap.Commands, snap.PermissionMode, snap.Model)
	}
}

// A chat that saved settings starts with them where the agent takes them.
// One it rejects or cannot change leaves a notice, and the chat still
// starts with the agent's real model and mode.
func TestStartSettings(t *testing.T) {
	modeOption := map[string]any{"id": "mode", "name": "Mode", "category": "mode", "type": "select", "currentValue": "ask", "options": []any{}}
	steps := script(t)[:2]
	steps[1].Send = []json.RawMessage{result(2, map[string]any{"sessionId": "s1", "configOptions": []any{modeOption, modelOption("acme-small")}})}
	steps = append(steps,
		step{Receive: rpc(3, "session/set_config_option", map[string]string{"sessionId": "s1", "configId": "model", "value": "acme-gone"}),
			Send: []json.RawMessage{raw(map[string]any{"jsonrpc": "2.0", "id": 3, "error": map[string]any{"code": -32602, "message": "Invalid value for config option model: acme-gone"}})}},
		step{Receive: rpc(4, "session/set_config_option", map[string]string{"sessionId": "s1", "configId": "mode", "value": "code"}),
			Send: []json.RawMessage{result(4, map[string]any{})}})
	h, err := startWith(t, steps, session.Config{Model: "acme-gone", Effort: "high", Mode: "code"}, session.Origin{})
	if err != nil {
		t.Fatal(err)
	}
	h.wait(t, session.EventState)
	h.wait(t, session.EventSettings) // the real settings, after the notices
	snap := snapshot(t, h.all)
	var notices []string
	for _, it := range snap.Content {
		if it.Kind == session.KindSystem {
			notices = append(notices, it.Text)
		}
	}
	if snap.Model == nil || *snap.Model != "acme-small" || len(notices) != 2 ||
		!strings.Contains(notices[0], "model acme-gone") || !strings.Contains(notices[0], "Invalid value") || !strings.Contains(notices[1], "effort high") {
		t.Fatalf("model %v, notices %q", snap.Model, notices)
	}
}

// claude-agent-acp's AskUserQuestion: a form elicitation becomes a
// question with options, an array field a multiSelect one, a question's
// free-text field its typed row, and the card's labels go back as the
// schema's values in the accepted content.
func TestElicitation(t *testing.T) {
	answerElicitation(t, `{"type":"object","properties":{"question_0":{"type":"string","title":"Database","oneOf":[{"const":"Postgres","title":"Postgres","description":"Relational"},{"const":"Redis","title":"Redis","description":"Key-value"}]},"question_0_custom":{"type":"string","title":"Other","description":"Type your own answer, or add a note to the option you chose above (optional)."},"question_1":{"type":"array","title":"Extras","description":"Which extras?","items":{"anyOf":[{"const":"cache","title":"Cache"},{"const":"queue","title":"Queue"}]}},"question_2":{"type":"boolean","title":"Migrate now?"}}}`,
		"Which database?", func(qs []session.Question) bool {
			return len(qs) == 3 && qs[0].ID == "question_0" && qs[0].Prompt == "Which database?" && qs[0].Header == "Database" && !qs[0].Multi &&
				reflect.DeepEqual(qs[0].Options, []session.PromptOption{{ID: "Postgres", Label: "Postgres", Description: "Relational"}, {ID: "Redis", Label: "Redis", Description: "Key-value"}}) &&
				qs[1].ID == "question_1" && qs[1].Prompt == "Which extras?" && qs[1].Multi && len(qs[1].Options) == 2 && qs[2].ID == "question_2"
		},
		map[string][]string{"question_0": {"MariaDB"}, "question_1": {"Cache", "Queue"}, "question_2": {"No"}},
		map[string]any{"question_0_custom": "MariaDB", "question_1": []string{"cache", "queue"}, "question_2": false})
}

// TestElicitationAgy: agy's ask_question, as agy 1.5.14 sends it: the
// title is the question, the options are numbered, and each question's
// free text is its own qN_other field.
func TestElicitationAgy(t *testing.T) {
	answerElicitation(t, `{"type":"object","properties":{"q1":{"type":"string","title":"Which color do you prefer?","oneOf":[{"const":"1","title":"Red"},{"const":"2","title":"Blue"}]},"q1_other":{"type":"string","title":"Other","description":"Optional free-form answer to: Which color do you prefer?"},"q2":{"type":"array","title":"Which fruits do you like?","items":{"anyOf":[{"const":"1","title":"Apple"},{"const":"2","title":"Banana"},{"const":"3","title":"Cherry"}]}},"q2_other":{"type":"string","title":"Other","description":"Optional free-form answer to: Which fruits do you like?"}}}`,
		"The agent has a question.", func(qs []session.Question) bool {
			return len(qs) == 2 && qs[0].Prompt == "Which color do you prefer?" && qs[0].Header == "" && qs[0].Options[1] == session.PromptOption{ID: "2", Label: "Blue"} &&
				qs[1].Prompt == "Which fruits do you like?" && qs[1].Multi && len(qs[1].Options) == 3
		},
		map[string][]string{"q1": {"Blue"}, "q2": {"Apple", "Cherry", "Kiwi"}},
		map[string]any{"q1": "2", "q2": []string{"1", "3"}, "q2_other": "Kiwi"})
}

// answerElicitation sends a form elicitation with schema, checks its
// questions with ok, answers them and wants content back.
func answerElicitation(t *testing.T, schema, message string, ok func([]session.Question) bool, answers map[string][]string, content map[string]any) {
	t.Helper()
	steps := script(t)[:3]
	steps[2].Send = []json.RawMessage{rpc("ask-1", "elicitation/create", map[string]any{
		"mode": "form", "sessionId": "s1", "toolCallId": "toolu_ask", "message": message, "requestedSchema": json.RawMessage(schema),
	})}
	steps = append(steps, step{Receive: result("ask-1", map[string]any{"action": "accept", "content": content}),
		Send: []json.RawMessage{result(3, map[string]string{"stopReason": "end_turn"})}})
	h, err := start(t, steps, session.Origin{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.d.Send(context.Background(), session.Message{Text: "fix it"}); err != nil {
		t.Fatal(err)
	}
	e := h.wait(t, session.EventInputRequested)
	if e.Detail != message || !ok(e.Questions) {
		t.Fatalf("input: %+v", e)
	}
	if err = h.d.Answer(context.Background(), e.RequestID, answers); err != nil {
		t.Fatal(err)
	}
	h.wait(t, session.EventInputResolved)
	if e := h.wait(t, session.EventTurnCompleted); e.State != session.TurnCompleted {
		t.Fatal(e)
	}
}

// A command the agent advertises reaches the snapshot with its
// description, and picking it with an argument sends the line as the
// prompt's text, which is how an ACP agent such as agy runs one.
func TestSlashCommandPrompt(t *testing.T) {
	update := notification("session/update", map[string]any{"sessionId": "s1", "update": map[string]any{
		"sessionUpdate": "available_commands_update", "availableCommands": []any{
			map[string]any{"name": "plan", "description": "Plan carefully before executing a task."}}}})
	steps := script(t)[:2]
	steps[1].Send = append([]json.RawMessage{update}, steps[1].Send...)
	steps = append(steps, step{Receive: rpc(3, "session/prompt", map[string]any{"sessionId": "s1",
		"prompt": []any{map[string]string{"type": "text", "text": "/plan add the tests"}}}),
		Send: []json.RawMessage{result(3, map[string]string{"stopReason": "end_turn"})}})
	h, err := start(t, steps, session.Origin{})
	if err != nil {
		t.Fatal(err)
	}
	if e := h.wait(t, session.EventCommands); len(e.Commands) != 1 || e.Commands[0].Name != "plan" || e.Commands[0].Description == "" {
		t.Fatalf("commands: %+v", e.Commands)
	}
	if _, err = h.d.Send(context.Background(), session.Message{Text: "/plan add the tests"}); err != nil {
		t.Fatal(err)
	}
	h.wait(t, session.EventTurnCompleted)
}
