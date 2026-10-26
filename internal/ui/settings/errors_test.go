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

	"github.com/quanticstudios/pitwall/internal/errlog"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestErrorsPage: Agent errors reads the log off the UI goroutine, counts
// only the chosen window and says so when it is empty.
func TestErrorsPage(t *testing.T) {
	dir := t.TempDir()
	var p Page
	p.Show(filepath.Join(dir, "config.toml"))
	p.er.path = filepath.Join(dir, "errors.jsonl")
	p.focusSearch = false
	now := time.Now()
	l, err := errlog.Open(p.er.path)
	if err != nil {
		t.Fatal(err)
	}
	l.Add(errlog.Event{T: now.AddDate(0, 0, -20), Provider: "codex", Kind: errlog.API, Output: "stream disconnected"})
	for range 2 {
		l.Add(errlog.Event{T: now.Add(-time.Hour), Provider: "claude", Kind: errlog.Command, Command: "grep -rn x --include=*.go .", Output: "zsh: no matches found: --include=*.go"})
	}
	l.Add(errlog.Event{T: now.Add(-time.Minute), Provider: "codex", Kind: errlog.Tool, Tool: "Edit", Command: "/r/a.go", Output: "File has not been read yet."})
	l.Close(time.Second)

	var r input.Router
	var ops op.Ops
	draw := func() bool {
		ops.Reset()
		p.cat = catErrors
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1000, 700)), Now: time.Now()}
		p.Layout(gtx, theme.Dark(), p.s, nil)
		r.Frame(&ops)
		_, again := r.WakeupTime()
		return again
	}
	sum := func(win string) *errorSum {
		t.Helper()
		p.er.window, p.er.read = win, false
		for start := time.Now(); draw(); time.Sleep(time.Millisecond) {
			if time.Since(start) > 5*time.Second {
				t.Fatal("the page never stopped asking for frames")
			}
		}
		p.er.mu.Lock()
		defer p.er.mu.Unlock()
		if p.er.err != "" {
			t.Fatal(p.er.err)
		}
		return p.er.sum
	}
	s := sum(week)
	if s.Total != 3 || len(s.Groups) != 2 || s.Groups[0].Count != 2 || s.Kinds[errlog.Command] != 2 {
		t.Errorf("week %+v", s)
	}
	if got, want := errorSplit(s), "2 commands, 1 tool call · Claude Code 2, Codex 1"; got != want {
		t.Errorf("split %q, want %q", got, want)
	}
	days := func(s *errorSum) (n int) {
		for _, d := range s.Days {
			n += d.Count
		}
		return n
	}
	if len(s.Days) != 8 || days(s) != 3 {
		t.Errorf("week days %+v", s.Days)
	}
	if s := sum(month); s.Total != 4 || s.Kinds[errlog.API] != 1 || days(s) != 4 {
		t.Errorf("month %+v", s)
	}
	if got := errorsNote(&errorSum{}, true, false, week); got != "No errors in the last 7 days." {
		t.Errorf("empty week note %q", got)
	}
}
