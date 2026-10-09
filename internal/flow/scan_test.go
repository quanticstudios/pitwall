package flow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func writeFile(t testing.TB, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "")), 0o644); err != nil {
		t.Fatal(err)
	}
}

func claudeLine(ts time.Time, session, msg, req, model string, in, out, read int64) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":%q,"requestId":%q,"message":{"id":%q,"model":%q,"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":0}}}`+"\n",
		ts.UTC().Format(time.RFC3339Nano), session, req, msg, model, in, out, read)
}

func codexCount(ts time.Time, in, cached, out, totalIn int64) string {
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":0,"output_tokens":0},"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"output_tokens":%d},"model_context_window":258400}}}`+"\n",
		ts.UTC().Format(time.RFC3339Nano), totalIn, in, cached, out)
}

func sum(recs []Record) map[string]Tokens {
	out := map[string]Tokens{}
	for _, r := range recs {
		out[r.Model] = out[r.Model].plus(r.Tokens)
	}
	return out
}

// TestScanClaude: a reply's entries count once with the fuller usage, the
// same call in a subagent's file counts once, a user line quoting
// "usage" and a synthetic entry count not at all.
func TestScanClaude(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	ts := now.Add(-time.Hour)
	writeFile(t, filepath.Join(dir, "proj", "s1.jsonl"),
		claudeLine(ts, "s1", "msg_a", "req_a", "claude-opus-5-5", 10, 5, 100),
		claudeLine(ts, "s1", "msg_a", "req_a", "claude-opus-5-5", 10, 50, 100),
		`{"type":"user","timestamp":"`+ts.UTC().Format(time.RFC3339)+`","message":{"content":"what is \"usage\"?"}}`+"\n",
		claudeLine(ts, "s1", "msg_b", "req_b", "claude-sonnet-5-5", 1, 2, 0),
		claudeLine(ts, "s1", "msg_s", "req_s", "<synthetic>", 1, 1, 0),
	)
	writeFile(t, filepath.Join(dir, "proj", "s1", "subagents", "agent-x.jsonl"),
		claudeLine(ts, "s1", "msg_a", "req_a", "claude-opus-5-5", 10, 50, 100),
		claudeLine(ts, "s1", "msg_c", "req_c", "claude-haiku-4-5", 3, 4, 0),
	)
	var s Scanner
	recs := s.Scan([]Source{{model.ProviderClaude, dir}}, now.Add(-24*time.Hour))
	want := map[string]Tokens{
		"claude-opus-5-5":   {Input: 10, Output: 50, CacheRead: 100},
		"claude-sonnet-5-5": {Input: 1, Output: 2},
		"claude-haiku-4-5":  {Input: 3, Output: 4},
	}
	if got := sum(recs); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	for _, r := range recs {
		if r.Session != "s1" || r.Provider != model.ProviderClaude {
			t.Errorf("record %+v", r)
		}
	}
}

// TestScanCodex: a rollout's calls under the latest turn_context's model,
// a repeated token_count once; a forked rollout skips the burst of copied
// history and counts its own calls.
func TestScanCodex(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	t0 := now.Add(-time.Hour)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	stamp := func(s int) string { return at(s).UTC().Format(time.RFC3339Nano) }
	writeFile(t, filepath.Join(dir, "2026", "10", "05", "rollout-a.jsonl"),
		`{"timestamp":"`+stamp(0)+`","type":"session_meta","payload":{"id":"thread-a","source":"cli"}}`+"\n",
		codexCount(at(1), 100, 0, 1, 100), // before any turn_context: no model
		`{"timestamp":"`+stamp(2)+`","type":"turn_context","payload":{"model":"gpt-6.1-sol"}}`+"\n",
		codexCount(at(3), 1000, 600, 100, 1000),
		codexCount(at(3), 1000, 600, 100, 1000), // repeated
		codexCount(at(9), 2000, 1400, 150, 3000),
		`{"timestamp":"`+stamp(10)+`","type":"turn_context","payload":{"model":"gpt-6-luna"}}`+"\n",
		codexCount(at(12), 500, 0, 20, 3500),
	)
	writeFile(t, filepath.Join(dir, "2026", "10", "05", "rollout-b.jsonl"),
		`{"timestamp":"`+stamp(20)+`","type":"session_meta","payload":{"id":"thread-b","forked_from_id":"thread-a"}}`+"\n",
		`{"timestamp":"`+stamp(20)+`","type":"session_meta","payload":{"id":"thread-a"}}`+"\n",
		`{"timestamp":"`+stamp(20)+`","type":"turn_context","payload":{"model":"gpt-6.1-sol"}}`+"\n",
		codexCount(at(20), 1000, 600, 100, 1000), // copies of thread-a's
		codexCount(at(20), 2000, 1400, 150, 3000),
		codexCount(at(30), 40, 0, 4, 40),
	)
	var s Scanner
	recs := s.Scan([]Source{{model.ProviderCodex, dir}}, now.Add(-24*time.Hour))
	want := map[string]Tokens{
		"gpt-6.1-sol": {Input: 400 + 600 + 40, Output: 254, CacheRead: 2000},
		"gpt-6-luna":  {Input: 500, Output: 20},
	}
	if got := sum(recs); !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	sessions := map[string]int{}
	for _, r := range recs {
		sessions[r.Session]++
	}
	if !reflect.DeepEqual(sessions, map[string]int{"thread-a": 3, "thread-b": 1}) {
		t.Errorf("sessions = %v", sessions)
	}
}

// TestScanGemini: a chat's message copies count once, under the session
// id its first line names; a user line quoting "tokens" counts not at all.
func TestScanGemini(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	stamp := func(s int) string { return now.Add(time.Duration(s-3600) * time.Second).UTC().Format(time.RFC3339Nano) }
	msg := func(s int, id string, in, out, cached int64) string {
		return fmt.Sprintf(`{"id":%q,"timestamp":%q,"type":"gemini","content":"ok","model":"gemini-3-pro","tokens":{"input":%d,"output":%d,"cached":%d,"thoughts":2,"tool":0,"total":0}}`+"\n",
			id, stamp(s), in, out, cached)
	}
	writeFile(t, filepath.Join(dir, "demo", "chats", "session-2026-10-05T10-00-3f2a9c1e.jsonl"),
		`{"sessionId":"s-1","projectHash":"9b","startTime":"`+stamp(0)+`"}`+"\n",
		`{"id":"u1","timestamp":"`+stamp(1)+`","type":"user","content":[{"text":"count the \"tokens\""}]}`+"\n",
		msg(2, "g1", 1000, 10, 600),
		`{"$set":{"lastUpdated":"`+stamp(2)+`"}}`+"\n",
		msg(2, "g1", 1000, 10, 600), // the same message, appended again
		msg(5, "g2", 1200, 20, 1000),
	)
	var s Scanner
	recs := s.Scan([]Source{{model.ProviderGemini, dir}}, now.Add(-24*time.Hour))
	if got, want := sum(recs), map[string]Tokens{"gemini-3-pro": {Input: 400 + 200, Output: 12 + 22, CacheRead: 1600}}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
	if len(recs) != 2 || recs[0].Session != "s-1" || recs[0].Provider != model.ProviderGemini {
		t.Errorf("recs = %+v", recs)
	}
}

// TestScanMtime: a file last written well before the window is not
// opened, even though its lines claim times inside it; calls before the
// window are left out of a file that is read.
func TestScanMtime(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	since := now.Add(-48 * time.Hour)
	old := filepath.Join(dir, "p", "old.jsonl")
	writeFile(t, old, claudeLine(now.Add(-time.Hour), "old", "m1", "r1", "claude-opus-5-5", 1, 1, 0))
	stale := since.Add(-mtimeSlack - time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "p", "new.jsonl"),
		claudeLine(since.Add(-time.Hour), "new", "m2", "r2", "claude-opus-5-5", 2, 2, 0),
		claudeLine(now.Add(-time.Hour), "new", "m3", "r3", "claude-opus-5-5", 3, 3, 0),
	)
	var s Scanner
	recs := s.Scan([]Source{{model.ProviderClaude, dir}}, since)
	if len(recs) != 1 || recs[0].Session != "new" || recs[0].Input != 3 {
		t.Errorf("recs = %+v", recs)
	}
}

// TestScanResume: a file that grew is read from where the last scan
// stopped, a line without its newline yet counts once it has one, and an
// unchanged file is not read again.
func TestScanResume(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	path := filepath.Join(dir, "p", "s.jsonl")
	first := claudeLine(now.Add(-time.Hour), "s", "m1", "r1", "claude-opus-5-5", 1, 1, 0)
	second := claudeLine(now.Add(-time.Hour), "s", "m2", "r2", "claude-opus-5-5", 2, 2, 0)
	half := second[:len(second)/2]
	writeFile(t, path, first, half)
	src := []Source{{model.ProviderClaude, dir}}
	since := now.Add(-24 * time.Hour)
	var s Scanner
	if recs := s.Scan(src, since); len(recs) != 1 {
		t.Fatalf("first scan: %+v", recs)
	}
	if n := s.read.Load(); n != int64(len(first+half)) {
		t.Fatalf("first scan read %d bytes", n)
	}
	s.read.Store(0)
	if s.Scan(src, since); s.read.Load() != 0 {
		t.Errorf("an unchanged file was read again: %d bytes", s.read.Load())
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	third := claudeLine(now.Add(-time.Hour), "s", "m3", "r3", "claude-opus-5-5", 3, 3, 0)
	if _, err := f.WriteString(second[len(half):] + third); err != nil {
		t.Fatal(err)
	}
	f.Close()
	recs := s.Scan(src, since)
	if got := sum(recs)["claude-opus-5-5"]; got != (Tokens{Input: 6, Output: 6}) {
		t.Errorf("after growing: %+v", got)
	}
	if n, want := s.read.Load(), int64(len(second+third)); n != want {
		t.Errorf("resumed scan read %d bytes, want %d", n, want)
	}
}

// BenchmarkScanCold scans synthetic Claude and Codex sessions with an
// empty cache: mostly long tool output, a call every few lines.
func BenchmarkScanCold(b *testing.B) {
	dir := b.TempDir()
	now := time.Now()
	// Tool output as a transcript holds it: source text, JSON-escaped.
	src, _ := json.Marshal(strings.Repeat("func (s *Scanner) Scan(srcs []Source) {\n\tif err != nil {\n\t\treturn fmt.Errorf(\"read %q: %w\", path, err)\n\t}\n", 40))
	output := string(src[1 : len(src)-1])
	var size int64
	for i := range 40 {
		var cl, cx []string
		for j := range 4000 {
			ts := now.Add(-time.Duration(j) * time.Minute)
			cl = append(cl, `{"type":"user","timestamp":"`+ts.UTC().Format(time.RFC3339)+`","message":{"content":[{"type":"tool_result","content":"`+output+`"}]}}`+"\n")
			cx = append(cx, `{"timestamp":"`+ts.UTC().Format(time.RFC3339)+`","type":"response_item","payload":{"type":"function_call_output","output":"`+output+`"}}`+"\n")
			if j%2 == 0 {
				cl = append(cl, strings.Replace(claudeLine(ts, fmt.Sprint("s", i), fmt.Sprint("m", i, "-", j), fmt.Sprint("r", i, "-", j), "claude-opus-5-5", 10, 500, 90000), `"text":"ok"`, `"text":"`+output+`"`, 1))
				cx = append(cx, `{"timestamp":"`+ts.UTC().Format(time.RFC3339)+`","type":"turn_context","payload":{"model":"gpt-6.1-sol","cwd":"/repo"}}`+"\n",
					codexCount(ts, 90000, 80000, 500, int64(j)*90000))
			}
		}
		writeFile(b, filepath.Join(dir, "claude", fmt.Sprint(i), "s.jsonl"), cl...)
		writeFile(b, filepath.Join(dir, "codex", fmt.Sprintf("rollout-%d.jsonl", i)), cx...)
	}
	srcs := []Source{{model.ProviderClaude, filepath.Join(dir, "claude")}, {model.ProviderCodex, filepath.Join(dir, "codex")}}
	for b.Loop() {
		var s Scanner
		if n := len(s.Scan(srcs, now.Add(-30*24*time.Hour))); n != 40*4000 {
			b.Fatalf("%d records", n)
		}
		size = s.read.Load()
	}
	b.SetBytes(size)
}
