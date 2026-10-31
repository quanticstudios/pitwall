package acp

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

type writeBuffer struct{ bytes.Buffer }

func (*writeBuffer) Close() error { return nil }

func TestUnsupportedRequests(t *testing.T) {
	w := &writeBuffer{}
	d := &driver{stdin: w, s: newStream()}
	// Requests receive explicit unsupported errors, notifications never get a
	// response, and numeric or string request ids retain their JSON type.
	for _, id := range []any{9, "request"} {
		d.line(rpc(id, "fs/read_text_file", map[string]string{"path": "/tmp/file"}))
		var got rpcMessage
		if json.Unmarshal(w.Bytes(), &got) != nil || got.Error == nil || got.Error.Code != -32601 || !bytes.Equal(got.ID, raw(id)) {
			t.Fatalf("reply: %s", w.Bytes())
		}
		w.Reset()
	}
	for _, line := range []json.RawMessage{[]byte(`garbage`), notification("unknown", nil), notification("session/update", map[string]any{"sessionId": "wrong", "update": map[string]string{"sessionUpdate": "agent_message_chunk"}})} {
		if events := d.line(line); len(events) != 0 || w.Len() != 0 {
			t.Fatalf("unexpected events or reply: %s %+v", w.Bytes(), events)
		}
	}
}

func TestToolUpdatesAndPlans(t *testing.T) {
	s := newStream()
	s.turn = "turn"
	first := s.update([]byte(`{"sessionUpdate":"tool_call","toolCallId":"edit","title":"Edit file","status":"in_progress","rawInput":{"path":"/file"}}`))[0]
	events := s.update([]byte(`{"sessionUpdate":"tool_call_update","toolCallId":"edit","content":[{"type":"diff","path":"/file","oldText":"old","newText":"new"}],"status":"completed"}`))
	if len(events) != 1 || events[0].Tool.Status != "completed" || len(events[0].Tool.Files) != 1 || events[0].Tool.Diff == "" || string(events[0].Tool.Input) != `{"path":"/file"}` {
		t.Fatalf("edit: %+v", events)
	}
	if first.Tool.Status != "running" || first.Tool.Diff != "" {
		t.Fatal("earlier tool mutated")
	}
	events = s.update([]byte(`{"sessionUpdate":"plan","entries":[{"content":"Read code","priority":"medium","status":"in_progress"}]}`))
	if len(events) != 1 || len(events[0].Tool.Tasks) != 1 || events[0].Tool.Tasks[0].Subject != "Read code" {
		t.Fatalf("plan: %+v", events)
	}
	for _, line := range []string{`{"sessionUpdate":"agent_message_chunk","content":{"type":"image","data":"AQI=","mimeType":"image/png"}}`, `{"sessionUpdate":"config_option_update","configOptions":[]}`, `{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"already recorded"}}`} {
		if got := s.update([]byte(line)); len(got) != 0 {
			t.Fatalf("unexpected event: %+v", got)
		}
	}
}

func TestStopReasonValidation(t *testing.T) {
	for _, reason := range []string{"end_turn", "max_tokens", "max_turn_requests", "refusal", "cancelled", "unknown"} {
		d := &driver{stdin: &writeBuffer{}, s: newStream()}
		d.s.turn = "t"
		d.s.prompt = "3"
		got := d.line(result(3, map[string]string{"stopReason": reason}))
		want := session.TurnCompleted
		if reason == "cancelled" {
			want = session.TurnInterrupted
		}
		if reason == "unknown" || reason == "refusal" {
			want = session.TurnFailed
		}
		if len(got) == 0 || got[len(got)-1].State != want || d.s.turn != "" {
			t.Fatalf("reason %s: %+v", reason, got)
		}
	}
}

func TestMessageIdentity(t *testing.T) {
	s := newStream()
	s.turn = "t"
	chunk := func(id, kind string) session.Event {
		return s.update(raw(map[string]any{"sessionUpdate": kind, "messageId": id, "content": map[string]string{"type": "text", "text": "chunk"}}))[0]
	}
	a := chunk("message-a", "agent_message_chunk")
	b := chunk("message-b", "agent_message_chunk")
	thought := chunk("message-a", "agent_thought_chunk")
	again := chunk("message-a", "agent_message_chunk")
	if a.ItemID == b.ItemID || a.ItemID == thought.ItemID || a.ItemID != again.ItemID {
		t.Fatal("ACP message ids were lost")
	}
}
