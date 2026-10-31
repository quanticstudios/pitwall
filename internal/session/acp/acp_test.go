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
	dir := t.TempDir()
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
	h.d, err = (Provider{Command: exe, Args: []string{"--scripted-acp"}, Env: append([]string{"PITWALL_TEST_ACP_SCRIPT=" + path}, extra...)}).Start(ctx, session.Config{Cwd: dir}, from, func(e session.Event) { h.events <- e })
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
	if err = h.d.Answer(context.Background(), "x", nil); !errors.Is(err, session.ErrUnsupported) {
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
