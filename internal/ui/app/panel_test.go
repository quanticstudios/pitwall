package app

import (
	"context"
	"image"
	"sync"
	"testing"
	"time"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestPanelWidth: the panel takes its full width when the panes keep
// theirs, shrinks to its minimum, then hides.
func TestPanelWidth(t *testing.T) {
	for _, c := range []struct{ avail, want int }{
		{1200, 432}, // room to spare
		{760, 400},  // shrinks so the panes keep 360
		{680, 320},  // at its minimum
		{679, 0},    // hidden
		{0, 0},
	} {
		if got := panelWidth(c.avail, 432, 320, 360); got != c.want {
			t.Errorf("panelWidth(%d) = %d, want %d", c.avail, got, c.want)
		}
	}
}

// TestPanelToggle: Ctrl+Shift+L opens and closes the panel in both presets
// and never reaches the pane, while Ctrl+L reaches it to clear the screen.
func TestPanelToggle(t *testing.T) {
	for _, b := range []*config.Bindings{aide, conventional} {
		u, keys := keyWindow(t, b)
		if got := keys(press("L", key.ModCtrl|key.ModShift)); got != "" || !u.nav.panelOpen {
			t.Fatalf("%s: Ctrl+Shift+L: open %v, pane got %q", b.Preset, u.nav.panelOpen, got)
		}
		if got := keys(press("L", key.ModCtrl|key.ModShift)); got != "" || u.nav.panelOpen {
			t.Fatalf("%s: second Ctrl+Shift+L: open %v, pane got %q", b.Preset, u.nav.panelOpen, got)
		}
		if got := keys(press("L", key.ModCtrl)); got != "\x0c" || u.nav.panelOpen {
			t.Fatalf("%s: Ctrl+L: open %v, pane got %q, want the clear-screen byte", b.Preset, u.nav.panelOpen, got)
		}
		u.panel.mu.Lock()
		sliding := u.panel.filesDir != ""
		u.panel.mu.Unlock()
		if !sliding {
			t.Fatalf("%s: the panel stopped before it slid out", b.Preset)
		}
		var ops op.Ops // a frame once the slide is over
		u.layout(gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: u.panelAt.Add(time.Second)})
		u.panel.mu.Lock()
		dir := u.panel.filesDir
		u.panel.mu.Unlock()
		if dir != "" {
			t.Fatalf("%s: closing the panel left the file listing on %q", b.Preset, dir)
		}
	}
}

// TestPanelFollow: Watch starts for a pane with a transcript, keeps
// running while nothing changes, restarts on a new transcript, drops a
// stale feed, and stops with the panel; the file listing follows the
// directory the same way.
func TestPanelFollow(t *testing.T) {
	type watch struct {
		ctx     context.Context
		path    string
		changed func(flow.Feed)
	}
	var mu sync.Mutex
	var watches []watch
	var dirs []string
	oldW, oldF := watchFeed, listFiles
	t.Cleanup(func() { watchFeed, listFiles = oldW, oldF })
	watchFeed = func(ctx context.Context, _ model.Provider, path string, changed func(flow.Feed)) {
		mu.Lock()
		watches = append(watches, watch{ctx, path, changed})
		mu.Unlock()
	}
	listFiles = func(_ context.Context, dir string) (string, []gitstat.FileStat, error) {
		mu.Lock()
		dirs = append(dirs, dir)
		mu.Unlock()
		return "main", []gitstat.FileStat{{Path: "a.go", Add: 1, Status: 'M'}}, nil
	}
	started := func(n int) []watch {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
			mu.Lock()
			w := append([]watch(nil), watches...)
			mu.Unlock()
			if len(w) >= n {
				return w
			}
		}
		t.Fatalf("%d watches started, want %d", len(watches), n)
		return nil
	}
	invalidated := make(chan struct{}, 16)
	inv := func() { invalidated <- struct{}{} }

	var s sidePanel
	pane := &model.Pane{ID: "p1", Provider: model.ProviderClaude, Transcript: "/t/1.jsonl"}
	s.follow(&model.Pane{ID: "p1", Provider: model.ProviderClaude}, "/repo", inv) // no transcript yet
	s.follow(pane, "/repo", inv)
	s.follow(pane, "/repo", inv)
	w := started(1)
	w[0].changed(flow.Feed{Turns: []flow.Turn{{Prompt: "one"}}})
	<-invalidated
	s.mu.Lock()
	got := s.feed
	s.mu.Unlock()
	if got == nil || got.Turns[0].Prompt != "one" {
		t.Fatalf("feed = %+v, want the first watch's", got)
	}

	pane2 := *pane
	pane2.Transcript = "/t/2.jsonl"
	s.follow(&pane2, "/repo", inv)
	w = started(2)
	if w[0].ctx.Err() == nil || w[1].path != "/t/2.jsonl" {
		t.Fatalf("a new transcript left the old watch running or did not start one: %v, %q", w[0].ctx.Err(), w[1].path)
	}
	w[0].changed(flow.Feed{Turns: []flow.Turn{{Prompt: "stale"}}})
	s.mu.Lock()
	got = s.feed
	s.mu.Unlock()
	if got != nil {
		t.Fatalf("a stale watch's feed landed: %+v", got)
	}

	s.stop()
	if w[1].ctx.Err() == nil {
		t.Fatal("stop left the watch running")
	}
	if len(started(0)) != 2 {
		t.Fatal("follow restarted a watch for an unchanged transcript")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, d := range dirs {
		if d != "/repo" {
			t.Fatalf("listed %q, want only /repo", d)
		}
	}
	if len(dirs) == 0 {
		t.Fatal("the file listing never ran")
	}
}

// A hook can name the session file before the daemon sees the agent; the
// watch starts once the provider is known, not with an empty one.
func TestPanelWatchWaitsForProvider(t *testing.T) {
	var mu sync.Mutex
	var got []model.Provider
	oldW, oldF := watchFeed, listFiles
	t.Cleanup(func() { watchFeed, listFiles = oldW, oldF })
	watchFeed = func(_ context.Context, p model.Provider, _ string, _ func(flow.Feed)) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}
	listFiles = func(context.Context, string) (string, []gitstat.FileStat, error) { return "", nil, nil }
	var s sidePanel
	defer s.stop()
	s.follow(&model.Pane{ID: "p1", Transcript: "/t/1.jsonl"}, "", func() {})
	s.follow(&model.Pane{ID: "p1", Provider: model.ProviderClaude, Transcript: "/t/1.jsonl"}, "", func() {})
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 {
			break
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != model.ProviderClaude {
		t.Fatalf("watches started with providers %q, want one with claude", got)
	}
}

// TestSlide: a slide starts hidden or shown, eases out over 200ms, asks for
// frames until it ends, and a zero start means no slide.
func TestSlide(t *testing.T) {
	var u ui
	at := time.Now()
	for _, c := range []struct {
		after time.Duration
		open  bool
		want  float32
	}{
		{0, true, 0}, {0, false, 1},
		{100 * time.Millisecond, true, 0.875}, {100 * time.Millisecond, false, 0.125},
		{200 * time.Millisecond, true, 1}, {time.Second, false, 0},
	} {
		if got := u.slide(gl.Context{Ops: new(op.Ops), Now: at.Add(c.after)}, at, c.open); got != c.want {
			t.Errorf("slide after %v, open %v = %v, want %v", c.after, c.open, got, c.want)
		}
	}
	if got := u.slide(gl.Context{Ops: new(op.Ops), Now: at}, time.Time{}, false); got != 0 {
		t.Errorf("no slide, closed = %v, want 0", got)
	}
}
