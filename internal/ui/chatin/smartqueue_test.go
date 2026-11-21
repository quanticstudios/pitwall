package chatin

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestQueueStatus(t *testing.T) {
	plain := queue("a", "b", "c")
	alone := queue("a", "b", "c")
	alone[1].Alone = true
	cmd := queue("a", "b")
	cmd[0].Text = "/compact"
	failed := queue("x", "a", "b")
	failed[0].Error = "refused"
	for _, c := range []struct {
		name     string
		snap     session.Snapshot
		qs       []session.QueuedInput
		separate bool
		want     string
	}{
		{"three plain", session.Snapshot{}, plain, true, "The next 3 go as one message"},
		{"an older daemon", session.Snapshot{}, plain, false, ""},
		{"one alone", session.Snapshot{}, alone, true, ""},
		{"a command first", session.Snapshot{}, cmd, true, ""},
		{"past a failed row", session.Snapshot{}, failed, true, "The next 2 go as one message"},
		{"a reason", session.Snapshot{QueueReason: "Waiting for your answer"}, plain, true, "Waiting for your answer"},
		{"paused", session.Snapshot{QueuePaused: true}, plain, true, "Paused"},
	} {
		if got := QueueStatus(&c.snap, c.qs, c.separate); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// Send separately toggles a row; a command has no such button; a failed
// turn's hold offers Send, which resumes.
func TestQueueSeparate(t *testing.T) {
	h := newHarness(t)
	h.separate = true
	h.snap.SessionState = session.StateRunning
	h.snap.QueuedInputs = queue("a", "b")
	h.snap.QueuedInputs[1].Alone = true
	h.frame()
	h.c.click("qsep:a").Click()
	h.c.click("qsep:b").Click()
	m := h.frame()
	want := []any{proto.ChatQueue{Chat: "c1", Action: "separate", ID: "a"}, proto.ChatQueue{Chat: "c1", Action: "together", ID: "b"}}
	if !reflect.DeepEqual(m, want) {
		t.Fatalf("toggles sent %+v", m)
	}
	h.snap.QueuePaused, h.snap.QueueReason = true, "The last turn failed"
	h.frame()
	h.c.click("qresume").Click()
	if m := h.frame(); len(m) != 1 || m[0].(proto.ChatQueue).Action != "resume" {
		t.Fatalf("Send sent %+v", m)
	}
}

// TestQueueShots renders the queue strip in each state into
// $PITWALL_QUEUE_SHOTS, for a look at them; it skips without it.
func TestQueueShots(t *testing.T) {
	dir := os.Getenv("PITWALL_QUEUE_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_QUEUE_SHOTS is not set")
	}
	rows := func(texts ...string) []session.QueuedInput {
		out := make([]session.QueuedInput, len(texts))
		for i, s := range texts {
			out[i] = session.QueuedInput{ID: string(rune('a' + i)), Text: s}
		}
		return out
	}
	merged := rows("Also run the linter before you push", "And update the CHANGELOG line")
	alone := rows("Fix the flaky test in store_test.go", "/compact", "Then open the PR")
	alone[2].Alone = true
	maybe := rows("Rename the option to queue_file", "Add a test for it")
	maybe[0].Error = "pitwall stopped while sending this, so it may have been sent: Retry sends it again"
	for _, s := range []struct {
		name string
		snap session.Snapshot
	}{
		{"merged", session.Snapshot{QueuedInputs: merged}},
		{"separate", session.Snapshot{QueuedInputs: alone}},
		{"answer", session.Snapshot{QueuedInputs: merged, QueueReason: "Waiting for your answer"}},
		{"approval", session.Snapshot{QueuedInputs: merged, QueueReason: "Waiting for your approval"}},
		{"failed", session.Snapshot{QueuedInputs: merged, QueuePaused: true, QueueReason: "The last turn failed"}},
		{"limit", session.Snapshot{QueuedInputs: merged, QueueReason: "Waiting for the usage limit to reset at 15:00"}},
		{"paused", session.Snapshot{QueuedInputs: merged, QueuePaused: true}},
		{"agent-left", session.Snapshot{QueuedInputs: merged, QueuePaused: true, QueueReason: "The agent these were queued for left this pane"}},
		{"maybe-sent", session.Snapshot{QueuedInputs: maybe}},
	} {
		s.snap.SessionID, s.snap.SessionState = "c1", session.StateRunning
		shootQueue(t, s.snap, filepath.Join(dir, "queue-"+s.name+".png"))
	}
}

func shootQueue(t *testing.T, snap session.Snapshot, path string) {
	t.Helper()
	const scale = 2
	size := image.Pt(720*scale, 230*scale)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	var c Composer
	c.q.open = true // the fold shows its rows
	var r input.Router
	var ops op.Ops
	env := Env{Chat: "c1", Snap: &snap, Width: 720, Now: true, Queue: true, Separate: true, Steers: true}
	for range 3 {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: time.Date(2026, 11, 21, 14, 0, 0, 0, time.UTC),
			Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}
		paint.Fill(gtx.Ops, theme.Dark().Bg)
		gtx.Constraints.Min.Y = 0
		c.Layout(gtx, theme.Dark(), env)
		r.Frame(&ops)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
