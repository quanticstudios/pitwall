package panel

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestPanelReduceMotion verifies Panel, Transcript, and TodoStrip do not
// schedule 30fps redraws when reduce_motion is enabled.
func TestPanelReduceMotion(t *testing.T) {
	prev := anim.Reduced()
	anim.SetReduced(true)
	t.Cleanup(func() { anim.SetReduced(prev) })

	th := theme.Dark()
	now := time.Unix(1e9, 0)

	// Panel with a working agent must not schedule a 30fps spinner wakeup.
	var p Panel
	var r input.Router
	var ops op.Ops
	pn := model.Pane{ID: "p1", WorkspaceID: "w1", Provider: model.ProviderClaude}
	act := model.Activity{PaneID: "p1", WorkspaceID: "w1", Provider: model.ProviderClaude, State: model.StateWorking, UpdatedAt: now}
	ops.Reset()
	gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(360, 600)), Now: now}
	p.Layout(gtx, th, Input{Pane: &pn, Activity: &act, Now: now})
	r.Frame(&ops)
	if wake, ok := r.WakeupTime(); ok && wake.Sub(now) < time.Second {
		t.Fatalf("Panel scheduled fast redraw in %v under reduce_motion", wake.Sub(now))
	}

	// Transcript and TodoStrip with a running turn must not schedule 30fps wakeups.
	turn := "t1"
	snap := &session.Snapshot{
		SessionID:    "0123456789abcdef",
		SessionState: session.StateRunning,
		Running:      true,
		Turns:        []session.TurnSummary{{ID: turn, State: session.TurnRunning}},
		Content: []session.Item{
			{ID: "p", Kind: session.KindUserPrompt, Text: "work"},
			{ID: "todo", Kind: session.KindToolCall, TurnID: &turn, Tool: &session.ToolCall{
				ToolName: "TodoWrite",
				Status:   "completed",
				Tasks:    []session.Task{{Subject: "do thing", Status: "in_progress"}},
			}},
		},
	}
	var tr Transcript
	var r2 input.Router
	ops.Reset()
	gtx = layout.Context{Ops: &ops, Source: r2.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(900, 700)), Now: now}
	tr.Layout(gtx, th, snap, false)
	r2.Frame(&ops)
	if wake, ok := r2.WakeupTime(); ok && wake.Sub(now) < time.Second {
		t.Fatalf("Transcript scheduled fast redraw in %v under reduce_motion", wake.Sub(now))
	}

	var r3 input.Router
	ops.Reset()
	gtx = layout.Context{Ops: &ops, Source: r3.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(900, 120)), Now: now}
	tr.TodoStrip(gtx, th, snap)
	r3.Frame(&ops)
	if wake, ok := r3.WakeupTime(); ok && wake.Sub(now) < time.Second {
		t.Fatalf("TodoStrip scheduled fast redraw in %v under reduce_motion", wake.Sub(now))
	}
}

// TestTranscriptIsCopiedSingleWakeup verifies isCopied schedules a single
// expiry invalidation instead of setting t.live for 30fps polling.
func TestTranscriptIsCopiedSingleWakeup(t *testing.T) {
	var tr Transcript
	now := time.Unix(1e9, 0)
	tr.now = now
	tr.copied = map[string]time.Time{"k": now}
	exp := now.Add(1500 * time.Millisecond)
	if !tr.isCopied("k") {
		t.Fatal("isCopied returned false before expiry")
	}
	if tr.live {
		t.Fatal("isCopied set live=true (30fps) instead of single wakeAt")
	}
	if !tr.wakeAt.Equal(exp) {
		t.Fatalf("wakeAt = %v, want %v", tr.wakeAt, exp)
	}
	tr.now = exp.Add(time.Millisecond)
	if tr.isCopied("k") {
		t.Fatal("isCopied returned true after expiry")
	}
}

// TestTranscriptHrefMemoized verifies t.href caches os.Stat results per
// (Dir, path) and re-checks when Dir changes.
func TestTranscriptHrefMemoized(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "README.md")
	if err := os.WriteFile(f, []byte("# hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var tr Transcript
	tr.Dir = dir
	first := tr.href("README.md", true)
	if first == "" {
		t.Fatal("expected file URL for existing file")
	}
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	// Cached hit should still return the memoized URL without re-statting.
	if second := tr.href("README.md", true); second != first {
		t.Fatalf("second href = %q, want cached %q", second, first)
	}
	// Changing Dir must not return stale entries from the previous Dir.
	tr.Dir = t.TempDir()
	if third := tr.href("README.md", true); third != "" {
		t.Fatalf("other Dir href = %q, want empty", third)
	}
}
