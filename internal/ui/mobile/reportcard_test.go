package mobile

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// TestReportCardsPlace checks the demo's cards go under the entries they
// came in after, and a new card under the newest entry.
func TestReportCardsPlace(t *testing.T) {
	c, env, _ := demoChat("reports")
	c.reportsMark(env)
	if len(c.reps.under[4]) != 2 || len(c.reps.under[6]) != 1 {
		t.Fatalf("cards under %v, want two under 4 and one under 6", c.reps.under)
	}
	r := env.Tab.Reports[0]
	r.Turn = 9
	env.Tab.Reports = append(env.Tab.Reports, r)
	c.reportsMark(env)
	if n := len(c.data.Entries); len(c.reps.under[c.data.Entries[n-1].Seq]) != 2 {
		t.Fatalf("a new card is not under the newest entry: %v", c.reps.under)
	}
}

// TestReportCardTap checks a tap opens the worker's tab, or the report
// in place once that tab is gone.
func TestReportCardTap(t *testing.T) {
	c, env, _ := demoChat("reports")
	r := env.Tab.Reports[0]
	r.Body = "All of it."
	var opened []string
	live := true
	env.OpenTab = func(tab string) bool { opened = append(opened, tab); return live }
	tap := func() {
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(360, 800))}
		c.reportCard(gtx, Theme(), env, r)
		c.reps.click[r.Key()].Click()
		c.reportCard(gtx, Theme(), env, r)
	}
	tap()
	if len(opened) != 1 || opened[0] != "w1" || c.reps.open[r.Key()] {
		t.Fatalf("opened %v, open %v", opened, c.reps.open[r.Key()])
	}
	live = false
	tap()
	if !c.reps.open[r.Key()] {
		t.Fatal("the card did not open in place with its tab gone")
	}
}

// TestReportShot renders the reports demo at 360 dp wide, dark, to
// PITWALL_REPORT_SHOTS, and checks no card is wider than the screen.
func TestReportShot(t *testing.T) {
	dir := os.Getenv("PITWALL_REPORT_SHOTS")
	if dir == "" {
		t.Skip("set PITWALL_REPORT_SHOTS to render the phone's report cards")
	}
	for _, before := range []bool{true, false} {
		reportShot(t, dir, before)
	}
}

func reportShot(t *testing.T, dir string, before bool) {
	c, env, _ := demoChat("reports")
	name := "reports-phone-360.png"
	if before {
		env.Tab.Reports, name = nil, "reports-phone-360-before.png"
	}
	const scale = 2
	size := image.Pt(360*scale, 1000*scale)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	for range 3 {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: env.Now, Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}
		c.layout(gtx, Theme(), env)
		r.Frame(&ops)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
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
