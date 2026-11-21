package chatin

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// frameWith is harness.frame with suggestion sug in the Env.
func (h *harness) frameWith(sug string) []any {
	h.ops.Reset()
	h.now = h.now.Add(10 * time.Millisecond)
	gtx := layout.Context{Ops: &h.ops, Source: h.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1000, 800)), Now: h.now}
	gtx.Constraints.Min.Y = 0
	env := h.env()
	env.Suggest = sug
	h.c.Layout(gtx, theme.Dark(), env)
	h.r.Frame(&h.ops)
	return h.c.Messages()
}

func (h *harness) pressWith(sug string, k key.Name, mods key.Modifiers) []any {
	h.r.Queue(key.Event{Name: k, Modifiers: mods, State: key.Press})
	return h.frameWith(sug)
}

// The empty box shows the suggestion; Tab fills it in, ready to edit or
// send, and Shift+Tab still cycles the mode.
func TestSuggestTab(t *testing.T) {
	h := newHarness(t)
	sug := "Now add a test for the retry"
	h.frameWith(sug)
	env := h.env()
	env.Suggest = sug
	if got := h.c.suggestion(env); got != sug {
		t.Fatalf("suggestion = %q", got)
	}
	if m := h.pressWith(sug, key.NameTab, key.ModShift); len(m) != 1 {
		t.Fatalf("Shift+Tab with a suggestion: %v, want the mode cycled", m)
	} else if _, ok := m[0].(proto.ChatSet); !ok {
		t.Fatalf("Shift+Tab sent %T", m[0])
	}
	if m := h.pressWith(sug, key.NameTab, 0); len(m) != 0 || h.c.Text() != sug {
		t.Fatalf("Tab: sent %v, text %q", m, h.c.Text())
	}
	if start, end := h.c.ed.Selection(); start != end || end != len([]rune(sug)) {
		t.Fatalf("caret %d-%d, want at the end", start, end)
	}
	// It is the draft now: no suggestion over it, and Enter sends it.
	if got := h.c.suggestion(env); got != "" {
		t.Fatalf("a suggestion over a draft: %q", got)
	}
	m := h.pressWith(sug, key.NameReturn, 0)
	if len(m) != 1 || m[0].(proto.ChatSend).Text != sug {
		t.Fatalf("Enter sent %v", m)
	}
}

// Typing replaces the suggestion; clearing the box brings it back.
func TestSuggestTyping(t *testing.T) {
	h := newHarness(t)
	sug := "Ship it"
	h.frameWith(sug)
	h.c.ed.Insert("w")
	h.frameWith(sug)
	env := h.env()
	env.Suggest = sug
	if h.c.suggestion(env) != "" {
		t.Fatal("the suggestion shows over typed text")
	}
	if m := h.pressWith(sug, key.NameTab, 0); h.c.Text() != "w" {
		t.Fatalf("Tab over typed text: %v, text %q", m, h.c.Text())
	}
	h.c.ed.SetText("")
	h.frameWith(sug)
	if h.c.suggestion(env) != sug {
		t.Fatal("an empty box again lost the suggestion")
	}
}

// Escape dismisses it, and only that suggestion: the next turn's shows.
func TestSuggestEscape(t *testing.T) {
	h := newHarness(t)
	sug := "Run the tests"
	h.frameWith(sug)
	if m := h.pressWith(sug, key.NameEscape, 0); len(m) != 0 {
		t.Fatalf("Escape sent %v", m)
	}
	env := h.env()
	env.Suggest = sug
	if h.c.suggestion(env) != "" {
		t.Fatal("Escape kept the suggestion")
	}
	if m := h.pressWith(sug, key.NameTab, 0); len(m) != 0 || h.c.Text() != "" {
		t.Fatalf("Tab after Escape: %v, text %q", m, h.c.Text())
	}
	env.Suggest = "Open a PR"
	if h.c.suggestion(env) != "Open a PR" {
		t.Fatal("the next suggestion stayed dismissed")
	}
}

// Never while the agent works, and never over an attached image.
func TestSuggestHidden(t *testing.T) {
	h := newHarness(t)
	h.snap.SessionState = session.StateRunning
	h.frameWith("x")
	env := h.env()
	env.Suggest = "x"
	if h.c.suggestion(env) != "" {
		t.Fatal("a suggestion while the agent works")
	}
	if m := h.pressWith("x", key.NameTab, 0); h.c.Text() != "" {
		t.Fatalf("Tab while working: %v, %q", m, h.c.Text())
	}
	h.snap.SessionState = session.StateReady
	h.c.draft.Images = []Image{{N: 1}}
	if h.c.suggestion(env) != "" {
		t.Fatal("a suggestion over an attached image")
	}
	h.c.draft.Images = nil
	env.Suggest = ""
	if h.c.suggestion(env) != "" {
		t.Fatal("no suggestion shows one")
	}
}

// TestSuggestShot writes the composer with a suggestion to
// $PITWALL_TOOL_SHOTS.
func TestSuggestShot(t *testing.T) {
	dir := os.Getenv("PITWALL_TOOL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TOOL_SHOTS is not set")
	}
	turn := "t1"
	done := "2026-11-21T10:00:00Z"
	snap := session.Snapshot{SessionID: "c1", SessionState: session.StateReady, Model: ptr("opus"),
		Turns: []session.TurnSummary{{ID: turn, State: session.TurnCompleted, CompletedAt: &done}}}
	shootSuggest(t, snap, "Now add a regression test for the cookie race", filepath.Join(dir, "suggest-composer.png"))
	shootSuggest(t, snap, "", filepath.Join(dir, "suggest-composer-off.png"))
}

func shootSuggest(t *testing.T, snap session.Snapshot, sug, path string) {
	t.Helper()
	const scale = 2
	size := image.Pt(720*scale, 150*scale)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	var c Composer
	var r input.Router
	var ops op.Ops
	env := Env{Chat: "c1", Snap: &snap, Width: 720, Now: true, Queue: true, Separate: true, Steers: true, Suggest: sug}
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
