package settings

import (
	"fmt"
	"image"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/decisionlog"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestStatsPage: Decision stats reads the log off the UI goroutine when
// it shows, asks for frames until the read is done, says what fills an
// empty page, and counts only the chosen window.
func TestStatsPage(t *testing.T) {
	dir := t.TempDir()
	var p Page
	p.Show(filepath.Join(dir, "config.toml"))
	p.st.path = filepath.Join(dir, "decisions.jsonl") // missing
	p.focusSearch = false                             // its caret blink asks for frames too
	var r input.Router
	var ops op.Ops
	// draw lays the page out and reports whether it asked for another
	// frame.
	draw := func() bool {
		t.Helper()
		ops.Reset()
		p.cat = catStats
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1000, 700)), Now: time.Now()}
		p.Layout(gtx, theme.Dark(), p.s, nil)
		r.Frame(&ops)
		_, again := r.WakeupTime()
		return again
	}
	note := func() string {
		p.st.mu.Lock()
		defer p.st.mu.Unlock()
		return statsNote(p.st.stats, p.st.any, p.st.loading || !p.st.read, p.st.window)
	}
	// wait draws frames until one no longer asks for another.
	wait := func() {
		t.Helper()
		for start := time.Now(); draw(); time.Sleep(time.Millisecond) {
			if time.Since(start) > 5*time.Second {
				t.Fatal("the page never stopped asking for frames")
			}
		}
	}
	if got := note(); got != statsLoading {
		t.Errorf("before the first frame: %q", got)
	}
	defer func(f func(string, time.Time) ([]decisionlog.Event, error)) { readLog = f }(readLog)
	hold := make(chan struct{})
	readLog = func(path string, since time.Time) ([]decisionlog.Event, error) {
		<-hold
		return decisionlog.Read(path, since)
	}
	if !draw() || !draw() {
		t.Error("a frame asked for no other while reading")
	}
	close(hold)
	wait()
	if got := note(); got != statsEmpty {
		t.Errorf("missing log: %q", got)
	}
	if len(p.stats()) != 1 {
		t.Error("the empty page has more than its first section")
	}

	now := time.Now()
	l, err := decisionlog.Open(p.st.path)
	if err != nil {
		t.Fatal(err)
	}
	for i, age := range []time.Duration{10 * 24 * time.Hour, 24 * time.Hour} {
		id := fmt.Sprint("d", i)
		l.Add(decisionlog.Event{T: now.Add(-age), Kind: decisionlog.Call, ID: id, Feature: "approvals", Ms: 120, Tokens: 900, Answer: "allow"})
		l.Add(decisionlog.Event{T: now.Add(-age), Kind: decisionlog.Outcome, ID: id, Feature: "approvals", User: decisionlog.Allowed, WaitMs: 4000, Held: i == 1})
	}
	l.Close(time.Second)
	calls := func() int {
		p.st.mu.Lock()
		defer p.st.mu.Unlock()
		return p.st.stats.Calls
	}
	// Opening the page again reads the log again.
	p.Show(filepath.Join(dir, "config.toml"))
	p.focusSearch = false
	for _, tc := range []struct {
		window string
		calls  int
	}{{"", 1}, {month, 2}, {ever, 2}} {
		if p.st.window != tc.window {
			p.st.window = tc.window
			p.readStats()
		}
		wait()
		if got := note(); got != "" || calls() != tc.calls {
			t.Errorf("window %q: note %q, %d calls", tc.window, got, calls())
		}
	}
	if secs := p.stats(); len(secs) != 3 || secs[1].title != "Approvals" || secs[2].title != "Calls per day" {
		t.Errorf("sections: %+v", secs)
	}
}

func TestToGo(t *testing.T) {
	for _, tc := range []struct {
		shown, held int
		want        string
	}{{0, 27, "30 more with it shown and 3 more held out to go."}, {36, 27, "3 more held out to go."}, {29, 40, "1 more with it shown to go."}} {
		if got := toGo(tc.shown, tc.held); got != tc.want {
			t.Errorf("toGo(%d, %d) = %q", tc.shown, tc.held, got)
		}
	}
}

func TestCount(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1234: "-1,234"} {
		if got := count(n); got != want {
			t.Errorf("count(%d) = %q", n, got)
		}
	}
}
