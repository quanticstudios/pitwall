package flow

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func TestSearchTranscripts(t *testing.T) {
	const stamp = "2026-10-30T12:34:56Z"
	for _, tc := range []struct {
		provider model.Provider
		record   string
	}{
		{model.ProviderClaude, `{"type":"assistant","timestamp":"` + stamp + `","message":{"content":[{"type":"text","text":"before NEEDLE after"}]}}`},
		{model.ProviderClaude, `{"type":"user","timestamp":"` + stamp + `","message":{"content":"before NEEDLE after"}}`},
		{model.ProviderCodex, `{"type":"response_item","timestamp":"` + stamp + `","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"before NEEDLE after"}]}}`},
		{model.ProviderPi, `{"type":"message","timestamp":"` + stamp + `","message":{"role":"assistant","content":[{"type":"text","text":"before NEEDLE after"}]}}`},
		{model.ProviderOmp, `{"type":"message","timestamp":"` + stamp + `","message":{"role":"user","content":"before NEEDLE after"}}`},
		{model.ProviderGemini, `{"type":"gemini","id":"m1","timestamp":"` + stamp + `","content":"before NEEDLE after"}`},
	} {
		t.Run(string(tc.provider)+tc.record[:16], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte("invalid JSON with needle\n"+tc.record+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var matches []TextMatch
			Search(context.Background(), tc.provider, path, "needle", func(m TextMatch) bool { matches = append(matches, m); return true })
			if len(matches) != 1 || matches[0].Record != 2 || matches[0].Snippet != "before NEEDLE after" || matches[0].Time.Format(time.RFC3339) != stamp {
				t.Fatalf("matches %+v", matches)
			}
			Search(context.Background(), tc.provider, path, "Needle", func(m TextMatch) bool { t.Error("smart case matched wrong case"); return true })
		})
	}
}

func TestSearchFullTextAndCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	content := strings.Repeat("prefix ", 400) + "needle " + strings.Repeat("suffix ", 400)
	raw, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": content}})
	if err := os.WriteFile(path, append(append(raw, '\n'), append(raw, '\n')...), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n := 0
	Search(ctx, model.ProviderClaude, path, "needle", func(m TextMatch) bool {
		n++
		if !strings.Contains(m.Snippet, "needle") || len([]rune(m.Snippet)) > 182 {
			t.Errorf("unbounded or missing match: %q", m.Snippet)
		}
		cancel()
		return true
	})
	if n != 1 {
		t.Fatalf("read %d matches after cancellation", n)
	}
	n = 0
	Search(context.Background(), model.ProviderClaude, path, "needle", func(TextMatch) bool { n++; return false })
	if n != 1 {
		t.Fatalf("emit stop read %d matches", n)
	}
	Search(ctx, model.ProviderClaude, path, "needle", func(TextMatch) bool { t.Error("cancelled search emitted"); return true })
	Search(context.Background(), model.ProviderClaude, t.TempDir(), "needle", func(TextMatch) bool { t.Error("directory emitted"); return true })
	Search(context.Background(), model.ProviderClaude, path+"missing", "needle", func(TextMatch) bool { t.Error("missing file emitted"); return true })
}

func TestSearchSnippetUnicode(t *testing.T) {
	if got := searchSnippet("İ 😀 ERROR\t終", "error", true); got != "İ 😀 ERROR 終" {
		t.Fatalf("unicode snippet %q", got)
	}
	if got := searchSnippet("x", "missing", false); got != "" {
		t.Fatalf("nonmatch %q", got)
	}
}

func TestSearchChildTranscript(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	childDir := filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents")
	if err := os.MkdirAll(childDir, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"call1","name":"Agent","input":{"description":"child"}}]}}` + "\n"
	if err := os.WriteFile(path, []byte(parent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "agent-a.meta.json"), []byte(`{"toolUseId":"call1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "agent-a.jsonl"), []byte(`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"child needle"}]}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var found []TextMatch
	Search(context.Background(), model.ProviderClaude, path, "needle", func(m TextMatch) bool { found = append(found, m); return true })
	if len(found) != 1 || found[0].Snippet != "child needle" {
		t.Fatalf("child matches %+v", found)
	}
}

func TestSearchSkipsOversizedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	data := strings.Repeat("x", maxLine+1) + "\n" + `{"type":"user","message":{"content":"needle after oversized record"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	var matches []TextMatch
	Search(context.Background(), model.ProviderClaude, path, "needle", func(m TextMatch) bool { matches = append(matches, m); return true })
	if len(matches) != 1 || matches[0].Record != 2 {
		t.Fatalf("matches %+v", matches)
	}
}

func TestSearchToolText(t *testing.T) {
	for _, tc := range []struct {
		provider model.Provider
		raw      string
	}{
		{model.ProviderClaude, `{"type":"assistant","message":{"content":[{"type":"tool_use","input":{"command":"needle in a command"}}]}}`},
		{model.ProviderClaude, `{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"needle in output"}]}]}}`},
		{model.ProviderCodex, `{"type":"response_item","payload":{"type":"function_call","arguments":"{\"command\":\"needle in a command\"}"}}`},
		{model.ProviderCodex, `{"type":"response_item","payload":{"type":"function_call_output","output":"needle in output"}}`},
		{model.ProviderPi, `{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","arguments":{"command":"needle in a command"}}]}}`},
		{model.ProviderGemini, `{"type":"gemini","toolCalls":[{"args":{"command":"needle in a command"}}]}`},
	} {
		text, _ := searchText(tc.provider, []byte(tc.raw))
		if !strings.Contains(text, "needle") {
			t.Errorf("%s tool text missing: %q", tc.provider, text)
		}
	}
	if got := searchJSONText(json.RawMessage(`{"needle":123,"output":["text",{"value":"body"}]}`)); got != "text\nbody" {
		t.Fatalf("tool values %q", got)
	}
}
