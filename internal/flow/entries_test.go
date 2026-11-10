package flow

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// short is an entry as kind:text or tool:arg=out, for comparing.
func short(es []Entry) string {
	var out []string
	for _, e := range es {
		s := e.Kind + ":" + e.Text
		if e.Kind == EntryTool {
			s = fmt.Sprintf("tool:%s %s=%s", e.Tool, e.Arg, e.Out)
			if e.Failed {
				s += "!"
			}
		}
		out = append(out, s)
	}
	return strings.Join(out, "|")
}

func logOf(t *testing.T, p model.Provider, lines ...string) (*Log, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewLog(p, path)
	l.Poll()
	return l, path
}

func TestClaudeEntries(t *testing.T) {
	l, _ := logOf(t, model.ProviderClaude,
		`{"type":"user","message":{"content":"fix the test"}}`,
		`{"type":"user","isMeta":true,"message":{"content":"caveat"}}`,
		`{"type":"user","message":{"content":"<command-name>/model</command-name>"}}`,
		`{"type":"assistant","message":{"model":"claude-opus-5-5","content":[{"type":"thinking","thinking":"hidden"}]}}`,
		`{"type":"assistant","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"Looking."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test ./...\nmore"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true,"content":[{"type":"text","text":"FAIL"}]}]}}`,
		`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"subagent"}]}}`,
		`{"type":"assistant","isApiErrorMessage":true,"message":{"model":"<synthetic>","content":[{"type":"text","text":"API Error: 529"}]}}`,
		`{"type":"user","message":{"content":"[Request interrupted by user]"}}`,
		`{"type":"user","origin":{"kind":"task-notification"},"message":{"content":"done"}}`,
		`not json`,
	)
	want := "user:fix the test|assistant:Looking.|tool:Bash go test ./...=FAIL!|error:API Error: 529|error:Interrupted"
	if got := short(l.Last(10)); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if l.Model != "claude-opus-5-5" {
		t.Errorf("model %q", l.Model)
	}
}

func TestCodexEntries(t *testing.T) {
	l, _ := logOf(t, model.ProviderCodex,
		`{"type":"turn_context","payload":{"model":"gpt-6.1-sol"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<environment_context>x</environment_context>"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"add a flag"}]}}`,
		`{"type":"response_item","payload":{"type":"reasoning","summary":[]}}`,
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"ls -la\"}"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","call_id":"c1","output":"{\"output\":\"a.go\\nb.go\",\"metadata\":{\"exit_code\":0}}"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"c2","input":"*** Begin Patch\n*** Update File: cmd/x.go\n"}}`,
		`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c2","output":"Exit code: 1\nfailed"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}}`,
		`{"type":"event_msg","payload":{"type":"error","message":"stream disconnected"}}`,
	)
	want := "user:add a flag|tool:exec_command ls -la=a.go\nb.go|tool:apply_patch x.go=Exit code: 1\nfailed!|assistant:Done.|error:stream disconnected"
	if got := short(l.Last(10)); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if l.Model != "gpt-6.1-sol" {
		t.Errorf("model %q", l.Model)
	}
}

func TestPiEntries(t *testing.T) {
	l, _ := logOf(t, model.ProviderPi,
		`{"type":"session","id":"x"}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"rename it"}]}}`,
		`{"type":"message","message":{"role":"assistant","model":"m1","stopReason":"toolUse","content":[{"type":"thinking","thinking":"no"},{"type":"text","text":"Reading."},{"type":"toolCall","id":"p1","name":"read","arguments":{"path":"/src/a.go"}}]}}`,
		`{"type":"message","message":{"role":"toolResult","toolCallId":"p1","content":[{"type":"text","text":"package a"}]}}`,
		`{"type":"message","message":{"role":"assistant","stopReason":"error","errorMessage":"overloaded","content":[]}}`,
	)
	want := "user:rename it|assistant:Reading.|tool:read a.go=package a|error:overloaded"
	if got := short(l.Last(10)); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if l.Model != "m1" {
		t.Errorf("model %q", l.Model)
	}
}

// A Log follows the file as it grows, starts over when it is truncated or
// replaced, and pages back with Before.
func TestLogTail(t *testing.T) {
	l, path := logOf(t, model.ProviderClaude, `{"type":"user","message":{"content":"one"}}`)
	rev, gen := l.Rev, l.Gen
	appendLine := func(s string) {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	// A line without its newline yet waits for it.
	appendLine(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t","name":"Read","input":{"file_path":"/a/b.go"}}]}}` + "\n" + `{"type":"user","message":{"content":[{"type":"tool_result",`)
	l.Poll()
	if got := short(l.Since(rev)); got != "tool:Read b.go=" {
		t.Fatalf("since: %s", got)
	}
	rev = l.Rev
	appendLine(`"tool_use_id":"t","content":"ok"}]}}` + "\n")
	l.Poll()
	if s := l.Since(rev); len(s) != 1 || s[0].Seq != 2 || s[0].Out != "ok" || !s[0].Done {
		t.Fatalf("result did not update the call: %+v", s)
	}
	if l.Gen != gen {
		t.Fatal("growth started over")
	}
	// Truncated: the entries start over, and seqs keep counting up.
	os.WriteFile(path, []byte(`{"type":"user","message":{"content":"two"}}`+"\n"), 0o600)
	l.Poll()
	if l.Gen == gen || short(l.Last(10)) != "user:two" || l.Last(1)[0].Seq != 3 {
		t.Fatalf("truncation: gen %d, %s", l.Gen, short(l.Last(10)))
	}
	// Removed: nothing left.
	os.Remove(path)
	l.Poll()
	if len(l.Last(10)) != 0 {
		t.Fatal("a removed file kept its entries")
	}
	// Paging back.
	var lines []string
	for i := range 10 {
		lines = append(lines, fmt.Sprintf(`{"type":"user","message":{"content":"m%d"}}`, i))
	}
	l, _ = logOf(t, model.ProviderClaude, lines...)
	last := l.Last(3)
	if short(last) != "user:m7|user:m8|user:m9" {
		t.Fatalf("last: %s", short(last))
	}
	if got := short(l.Before(last[0].Seq, 2)); got != "user:m5|user:m6" {
		t.Errorf("before: %s", got)
	}
	if got := l.Before(1, 5); len(got) != 0 {
		t.Errorf("before the first: %v", got)
	}
}

func TestCutBytes(t *testing.T) {
	if got := cutBytes("aé", 2); got != "a…" {
		t.Errorf("%q", got)
	}
	if got := cutBytes("abc", 3); got != "abc" {
		t.Errorf("%q", got)
	}
}
