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

	// Claude Code's own menu still up over its box after the typing: Esc
	// closes it, so Enter sends the line as typed, not its highlighted row.
	f.pane(0).mu.Lock()
	f.pane(0).screen = []string{"❯ /review-pr   Review a pull request (user)", "────────", "❯ /rev", "────────"}
	f.pane(0).mu.Unlock()
	before := len(f.pane(0).got())
	if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "a", Text: "/rev"}); r.(proto.ChatResult).Code != "" {
		t.Fatalf("send: %+v", r)
	}
	if got := gotAfter(t, f.pane(0), before, len("/rev")+2); !strings.HasSuffix(got, "\x1b\r") {
		t.Errorf("typed %q, want Esc before Enter", got)
	}
}

// A slash line goes to agy with its Enter in one write only when it is
// one line: a longer message is typed and entered as any other.
func TestAgySlashLine(t *testing.T) {
	for text, want := range map[string]bool{"/plan add tests": true, "/compact": true, "plan it": false, "/plan\nsecond line": false, "": false} {
		if got := agySlashLine(text); got != want {
			t.Errorf("agySlashLine(%q) = %v, want %v", text, got, want)
		}
	}
}

// A pane chat's message with a Codex $skill goes in as typed, a normal
// prompt; one that ends in the skill gets a space after it, so Codex's
// skill menu, which opens on the word, closes before Enter instead of
// taking it.
func TestPaneChatCodexSkillSend(t *testing.T) {
	d, f, _ := remoteDaemon(t)
	b, err := os.ReadFile("../session/codex/testdata/rollout.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	rollout := filepath.Join(t.TempDir(), "rollout.jsonl")
	os.WriteFile(rollout, b, 0o600)
	d.mu.Lock()
	d.st.Panes[1].Transcript = rollout
	d.st.Activities[d.activityIndex("b")].State = model.StateCompleted
	d.mu.Unlock()
	f.pane(1).mu.Lock()
	f.pane(1).screen = []string{"› "}
	f.pane(1).mu.Unlock()
	for text, want := range map[string]string{
		"use $release now": "use $release now\r",
		"then $release":    "then $release \r",
	} {
		paneRead(t, d, "b", session.Snapshot{}, 0)
		before := len(f.pane(1).got())
		if r, _ := d.chatRequest(context.Background(), proto.ChatSend{Chat: "b", Text: text}); r.(proto.ChatResult).Code != "" {
			t.Fatalf("send %q: %+v", text, r)
		}
		if got := gotAfter(t, f.pane(1), before, len(want)); !strings.HasSuffix(got, want) {
			t.Errorf("typed %q, want it to end %q", got, want)
		}
	}
}
