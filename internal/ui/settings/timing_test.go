package settings

import (
	"image"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/timelog"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestTimingPage: Timing reads the log off the UI goroutine, sums only
// the chosen window and draws its charts without asking for frames
// forever.
func TestTimingPage(t *testing.T) {
	dir := t.TempDir()
	var p Page
	p.Show(filepath.Join(dir, "config.toml"))
	p.tm.path = filepath.Join(dir, "timing.jsonl")
	p.focusSearch = false
	now := time.Now()
	l, err := timelog.Open(p.tm.path)
	if err != nil {
		t.Fatal(err)
	}
	turn := func(end time.Time, tab string) timelog.Event {
		return timelog.Event{T: end, Kind: timelog.Turn, Tab: tab, Provider: "claude", Start: end.Add(-10 * time.Minute), Ms: 600000, Model: 300000, Wait: 60000,
			Tools: map[string]int64{timelog.Test: 200000, timelog.Search: 40000}, Slow: []timelog.Cmd{{Command: "go test ./...", Kind: timelog.Test, Ms: 200000}}}
	}
	l.Add(turn(now.AddDate(0, 0, -20), "old"))
	l.Add(turn(now.Add(-2*time.Hour), "t"))
	l.Add(turn(now.Add(-time.Hour), "t"))
	l.Add(timelog.Event{T: now.Add(-30 * time.Minute), Kind: timelog.PR, Tab: "t", PR: 4})
	l.Close(time.Second)

	var r input.Router
	var ops op.Ops
	draw := func() bool {
		ops.Reset()
		p.cat = catTiming
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1000, 700)), Now: time.Now()}
		p.Layout(gtx, theme.Dark(), p.s, nil)
		r.Frame(&ops)
		_, again := r.WakeupTime()
		return again
	}
	sum := func(win string) *timelog.Summary {
		t.Helper()
		p.tm.window, p.tm.read = win, false
		for start := time.Now(); draw(); time.Sleep(time.Millisecond) {
			if time.Since(start) > 5*time.Second {
				t.Fatal("the page never stopped asking for frames")
			}
		}
		p.tm.mu.Lock()
		defer p.tm.mu.Unlock()
		if p.tm.err != "" {
			t.Fatal(p.tm.err)
		}
		return p.tm.sum
	}
	s := sum(week)
	if s.Turns != 2 || s.Parts[timelog.Test] != 400000 || len(s.Tabs) != 1 || s.Tabs[0].PR != 4 || len(s.Sinks) != 1 {
		t.Errorf("week %+v", s)
	}
	if s := sum(month); s.Turns != 3 || len(s.Tabs) != 2 {
		t.Errorf("month %+v", s)
	}
	if got := timingNote(&timelog.Summary{}, true, false, week); got != "No turns in the last 7 days." {
		t.Errorf("empty week note %q", got)
	}
	if pt := p.partOf(timelog.Git); pt.name != "Other commands" {
		t.Errorf("git is in %q", pt.name)
	}
}
