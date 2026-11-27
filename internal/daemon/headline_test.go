package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

// headlineSnap is a chat with one finished turn t1 that ended just now:
// with tools a Bash run and a reply, without them reply alone.
func headlineSnap(tools bool, reply string) session.Snapshot {
	now := session.Stamp(time.Now())
	t1 := "t1"
	content := []session.Item{{ID: "u1", Kind: session.KindUserPrompt, Text: "Start the run, token " + awaySecret, CreatedAt: now, TurnID: &t1}}
	if tools {
		content = append(content, session.Item{ID: "c1", Kind: session.KindToolCall, CreatedAt: now, TurnID: &t1,
			Tool: &session.ToolCall{ToolName: "Bash", Command: "python train.py --steps 3478", Status: "completed"}})
	}
	content = append(content, session.Item{ID: "a1", Kind: session.KindAssistantText, Text: reply, CreatedAt: now, TurnID: &t1})
	return session.Snapshot{Content: content, Turns: []session.TurnSummary{{ID: t1, State: session.TurnCompleted, StartedAt: now, CompletedAt: &now}}}
}

// fakeHeadline makes awaySummarize answer out, err and counts its calls.
func fakeHeadline(t *testing.T, out string, err error) *atomic.Int32 {
	var calls atomic.Int32
	old := awaySummarize
	awaySummarize = func(_ context.Context, _ suggestRun, prompt string) (string, error) {
		calls.Add(1)
		if strings.Contains(prompt, awaySecret) {
			t.Errorf("the prompt carries a secret: %q", prompt)
		}
		return out, err
	}
	t.Cleanup(func() { awaySummarize = old })
	return &calls
}

func headlineOf(d *Daemon, id, turn string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return model.HeadlineOf(d.st.Headlines[id], turn)
}

// TestHeadline: a finished turn gets its line once, kept in the state
// that is saved; a trivial turn, a failed call and summaries off get none.
func TestHeadline(t *testing.T) {
	t.Run("stored", func(t *testing.T) {
		d, _, _ := remoteDaemon(t)
		d.o.AwaySummary = func() bool { return true }
		suggestPane(t, d, "a", headlineSnap(true, "Started."))
		calls := fakeHeadline(t, "Headline: \"Started SFT on the H100; 3,478 steps planned.\"\n", nil)
		d.headline("a", "a")
		d.headline("a", "a") // already has one: no second call
		if got := headlineOf(d, "a", "t1"); got != "Started SFT on the H100; 3,478 steps planned" {
			t.Fatalf("headline = %q", got)
		}
		if n := calls.Load(); n != 1 {
			t.Fatalf("%d model calls, want 1", n)
		}
		d.mu.Lock()
		b, _ := json.Marshal(d.st)
		d.mu.Unlock()
		var back model.State
		if err := json.Unmarshal(b, &back); err != nil || model.HeadlineOf(back.Headlines["a"], "t1") == "" {
			t.Fatalf("the headline does not survive a save: %v %s", err, b)
		}
	})
	t.Run("trivial", func(t *testing.T) {
		d, _, _ := remoteDaemon(t)
		d.o.AwaySummary = func() bool { return true }
		suggestPane(t, d, "a", headlineSnap(false, "Sure, done."))
		calls := fakeHeadline(t, "Said hi", nil)
		d.headline("a", "a")
		if n := calls.Load(); n != 0 || headlineOf(d, "a", "t1") != "" {
			t.Fatalf("a trivial turn called the model %d times", n)
		}
	})
	t.Run("fails", func(t *testing.T) {
		d, _, _ := remoteDaemon(t)
		d.o.AwaySummary = func() bool { return true }
		suggestPane(t, d, "a", headlineSnap(true, "Started."))
		calls := fakeHeadline(t, "Started SFT", context.DeadlineExceeded)
		d.headline("a", "a")
		if calls.Load() != 1 || headlineOf(d, "a", "t1") != "" {
			t.Fatalf("a failed call left headline %q", headlineOf(d, "a", "t1"))
		}
		fakeHeadline(t, "  \n", nil)
		d.headline("a", "a")
		if headlineOf(d, "a", "t1") != "" {
			t.Fatal("an empty answer left a headline")
		}
	})
	t.Run("off", func(t *testing.T) {
		d, _, _ := remoteDaemon(t)
		d.o.AwaySummary = func() bool { return false }
		suggestPane(t, d, "a", headlineSnap(true, "Started."))
		calls := fakeHeadline(t, "Started SFT", nil)
		d.headline("a", "a")
		if calls.Load() != 0 {
			t.Fatal("summaries off still called the model")
		}
	})
	t.Run("old turn", func(t *testing.T) {
		d, _, _ := remoteDaemon(t)
		d.o.AwaySummary = func() bool { return true }
		s := headlineSnap(true, "Started.")
		old := session.Stamp(time.Now().Add(-time.Hour))
		s.Turns[0].CompletedAt = &old
		suggestPane(t, d, "a", s)
		calls := fakeHeadline(t, "Started SFT", nil)
		d.headline("a", "a")
		if calls.Load() != 0 {
			t.Fatal("a turn read again after a restart asked for a headline")
		}
	})
}

func TestHeadlineLine(t *testing.T) {
	for in, want := range map[string]string{
		"Fixed the flaky login test.":           "Fixed the flaky login test",
		"\n- **Headline:** Ran `go test`\nmore": "Ran `go test`",
		"\"Merged PR #12\"":                     "Merged PR #12",
		"":                                      "",
		strings.Repeat("x", 200):                strings.Repeat("x", headlineRunes) + "…",
	} {
		if got := headlineLine(in); got != want {
			t.Errorf("headlineLine(%q) = %q, want %q", in, got, want)
		}
	}
	if headlinePrompt(headlineSnap(false, "ok"), "t1") != "" {
		t.Error("a short reply with no tools is not trivial")
	}
	if p := headlinePrompt(headlineSnap(false, strings.Repeat("word ", 80)), "t1"); p == "" {
		t.Error("a long reply is trivial")
	}
}
