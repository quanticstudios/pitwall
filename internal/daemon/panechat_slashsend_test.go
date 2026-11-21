package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// A command picked in a pane chat goes into the agent's TUI as typing it
// there does, its argument kept: Claude Code's and Codex's typed, then
// Enter; agy's with its Enter in the same write, so agy's own menu, drawn
// as the command is typed, cannot swap in its highlighted row.
func TestPaneChatSlashSend(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	dir := t.TempDir()
	copyTo := func(src, name string) string {
		t.Helper()
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	claudeLog := filepath.Join(dir, "s.jsonl")
	os.WriteFile(claudeLog, []byte(`{"type":"user","uuid":"p1","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"hi"}}`+"\n"), 0o600)
	d.mu.Lock()
	d.st.Panes[0].Transcript = claudeLog
	d.st.Panes[1].Transcript = copyTo("../session/codex/testdata/rollout.jsonl", "rollout.jsonl")
	d.st.Panes[2].Provider, d.st.Panes[2].Transcript = model.ProviderAntigravity, copyTo("../session/antigravity/testdata/transcript_full.jsonl", "agy.jsonl")
	d.st.Panes[2].Env = []string{"HOME=" + dir}
	for _, id := range []string{"a", "b", "c"} {
		d.st.Activities[d.activityIndex(id)].State = model.StateCompleted
	}
	d.mu.Unlock()
	for i, box := range []string{"> ", "› ", "> "} {
		f.pane(i).mu.Lock()
		f.pane(i).screen = []string{box}
		f.pane(i).mu.Unlock()
	}
	var writes [3][]string
	for i := range 3 {
		p := f.pane(i)
		p.mu.Lock()
		n := len(p.input)
		p.wrote = func() { writes[i] = append(writes[i], string(p.input[n:])); n = len(p.input) }
		p.mu.Unlock()
	}

	for i, c := range []struct{ id, text string }{
		{"a", "/review focus on auth"},
		{"b", "/review focus on auth"},
		{"c", "/plan add the tests"},
	} {
		paneRead(t, d, c.id, session.Snapshot{}, 0)
		before := len(f.pane(i).got())
		if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: c.id, Text: c.text}); r.(proto.ChatResult).Code != "" {
			t.Fatalf("%s: send: %+v", c.id, r)
		}
		got := gotAfter(t, f.pane(i), before, len(c.text)+1)
		if !strings.Contains(got, c.text) || !strings.HasSuffix(got, "\r") {
			t.Errorf("%s: typed %q, want %q then Enter", c.id, got, c.text)
		}
	}
	f.pane(2).mu.Lock()
	agy := writes[2]
	f.pane(2).mu.Unlock()
	if len(agy) != 1 || agy[0] != "/plan add the tests\r" {
		t.Errorf("agy got writes %q, want the line and its Enter as one", agy)
	}
}
