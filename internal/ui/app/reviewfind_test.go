package app

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/review"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Ctrl+F in the review view finds in every file: a match in unchanged
// lines folded between hunks unfolds them, one in a file over hugeLines
// selects and shows it, Enter and Shift+Enter move and wrap, and Escape
// shuts the bar before it closes the view.
func TestReviewFind(t *testing.T) {
	var a, b strings.Builder
	a.WriteString("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,40 +1,40 @@\n-old 1\n+new 1\n")
	for i := 2; i < 40; i++ {
		if i == 20 {
			a.WriteString(" the needle here\n")
			continue
		}
		fmt.Fprintf(&a, " line %d\n", i)
	}
	a.WriteString("-old 40\n+new 40\n")
	b.WriteString("diff --git a/b.go b/b.go\nnew file mode 100644\n--- /dev/null\n+++ b/b.go\n@@ -0,0 +1,3001 @@\n")
	for i := 1; i <= 3001; i++ {
		if i == 1500 {
			b.WriteString("+\tNeedle()\n")
			continue
		}
		fmt.Fprintf(&b, "+row %d\n", i)
	}
	old := readDiff
	t.Cleanup(func() { readDiff = old })
	readDiff = func(context.Context, string, string) (gitstat.Patch, error) {
		return gitstat.Patch{Base: "refs/heads/main", Root: "/repo", Diff: []byte(a.String() + b.String())}, nil
	}
	be := newAgentBackend(model.StateCompleted)
	u := &ui{b: be, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
	cleanupReview(t, &u.review)
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	press := func(name key.Name, m key.Modifiers) {
		r.Queue(key.Event{Name: name, Modifiers: m, State: key.Press})
		frame()
	}
	frame()
	press("R", key.ModCtrl|key.ModShift)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, _, _, loaded := u.review.snapshot(); loaded || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	frame()
	rv := &u.review
	files, _, _, _, _ := rv.snapshot()
	if len(files) != 2 || !rv.huge(&files[1]) {
		t.Fatalf("files %d, b.go huge %v", len(files), len(files) == 2 && rv.huge(&files[1]))
	}
	if lineRow(rv.rowsOf(&files[0]), 20) >= 0 {
		t.Fatal("a.go's line 20 shows: the test needs it folded")
	}
	press("F", key.ModCtrl)
	if !rv.find.bar.On() {
		t.Fatal("Ctrl+F opened no find bar")
	}
	rv.find.bar.SetQuery("NEEDLE")
	frame()
	frame()
	if got := rv.find.count(); got != "1 of 2" || rv.selPath != "a.go" {
		t.Fatalf("count %q, file %q", got, rv.selPath)
	}
	// Line 20 is the 21st line: old line 1 and new line 1 come first.
	if row := lineRow(rv.rowsOf(&files[0]), 20); row < 0 || rv.find.current(0, 20) != 4 {
		t.Errorf("a.go's match: row %d, current at %d", row, rv.find.current(0, 20))
	}
	reviewShot(t, frame, &ops, "review-find.png")
	press(key.NameReturn, 0)
	frame()
	if got := rv.find.count(); got != "2 of 2" || rv.selPath != "b.go" || rv.huge(&files[1]) {
		t.Errorf("Enter: count %q, file %q, huge %v", got, rv.selPath, rv.huge(&files[1]))
	}
	if row := lineRow(rv.rowsOf(&files[1]), 1499); row < 0 {
		t.Errorf("b.go's line 1500 has no row")
	}
	press(key.NameReturn, 0) // wraps to the first
	frame()
	if got := rv.find.count(); got != "1 of 2" || rv.selPath != "a.go" {
		t.Errorf("Enter past the last: %q, %q", got, rv.selPath)
	}
	press(key.NameReturn, key.ModShift) // and back past the first
	if got := rv.find.count(); got != "2 of 2" {
		t.Errorf("Shift+Enter past the first: %q", got)
	}
	press(key.NameEscape, 0)
	if rv.find.bar.On() || !rv.on {
		t.Fatalf("Escape: bar %v, view %v", rv.find.bar.On(), rv.on)
	}
	frame()
	press(key.NameEscape, 0)
	if rv.on {
		t.Error("a second Escape left the view open")
	}
}

// The hits cover every line, folded or not, tabs spaced out as drawn,
// and leave binary files out.
func TestReviewHits(t *testing.T) {
	files := []review.File{
		{Path: "a", Lines: []review.Line{{Kind: ' ', Text: "\tx needle"}, {Kind: '+', Text: "NEEDLE needle"}}},
		{Path: "bin", Binary: true, Lines: []review.Line{{Text: "needle"}}},
		{Path: "c", Lines: []review.Line{{Kind: '-', Text: "neeedle"}}},
	}
	got := fmt.Sprint(reviewHits(files, session.Fold("needle")))
	if want := "[{0 0 6} {0 1 0} {0 1 7}]"; got != want {
		t.Errorf("reviewHits = %s, want %s", got, want)
	}
	if reviewHits(files, nil) != nil {
		t.Error("an empty query found something")
	}
}

// reviewShot draws the window's frame offscreen to $PITWALL_SHOTS/name
// as a PNG, for a look at it; nothing without it or a GPU.
func reviewShot(t *testing.T, frame func(), ops *op.Ops, name string) {
	t.Helper()
	dir := os.Getenv("PITWALL_SHOTS")
	if dir == "" {
		return
	}
	win, err := headless.NewWindow(1280, 800)
	if err != nil {
		t.Log("no headless GPU:", err)
		return
	}
	defer win.Release()
	frame()
	if err := win.Frame(ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1280, 800))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
