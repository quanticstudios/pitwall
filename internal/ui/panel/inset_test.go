package panel

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// gridOf is a screen of rows, cols wide.
func gridOf(cols int, rows ...string) vt.Grid {
	g := vt.Grid{Cols: cols, Rows: len(rows), Cells: make([]vt.Cell, cols*len(rows))}
	for y, r := range rows {
		x := 0
		for _, c := range r {
			if x < cols {
				g.Cells[y*cols+x] = vt.Cell{Content: string(c), Width: 1}
			}
			x++
		}
	}
	return g
}

// claudeScreen is Claude Code mid-turn: work, the spinner, then its
// input box between two rules and the hint line.
var claudeScreen = gridOf(40,
	"⏺ Read internal/ui/panel/inset.go",
	"",
	"⏺ Bash(go test ./internal/ui/panel)",
	"  ⎿  ok  panel 1.2s",
	"",
	"✻ Testing… (2m 31s · esc to interrupt)",
	"",
	"────────────────────────────────────────",
	"❯ ",
	"────────────────────────────────────────",
	"  ? for shortcuts",
)

// TestScreenTail: the inset shows the rows above Claude Code's input
// box, the last n of them, without the box or the blank rows after.
func TestScreenTail(t *testing.T) {
	got := screenTail(claudeScreen, 3)
	want := []string{"  └  ok  panel 1.2s", "", "* Testing… (2m 31s · esc to interrupt)"}
	if !slices.Equal(got, want) {
		t.Fatalf("tail = %q, want %q", got, want)
	}
	// No box: the screen's last rows, trailing blanks dropped.
	got = screenTail(gridOf(10, "a", "b", "c", "", ""), 2)
	if !slices.Equal(got, []string{"b", "c"}) {
		t.Fatalf("no-box tail = %q", got)
	}
	if got := screenTail(gridOf(10, "", ""), 4); len(got) != 0 {
		t.Fatalf("blank screen tail = %q", got)
	}
}

// TestInsetShows: the inset shows while the agent works and has a
// screen, hides when it goes idle, and stays hidden once closed.
func TestInsetShows(t *testing.T) {
	th := theme.Dark()
	var in Inset
	var ops op.Ops
	var r input.Router
	now := time.Date(2026, 11, 27, 17, 0, 0, 0, time.UTC)
	reads := 0
	frame := func() (vt.Grid, bool) { reads++; return claudeScreen, true }
	lay := func(working bool) bool {
		ops.Reset()
		now = now.Add(100 * time.Millisecond)
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(900, 600)), Now: now}
		open := in.Layout(gtx, th, "Claude", working, image.Rect(0, 0, 900, 600), frame)
		r.Frame(&ops)
		return open
	}
	lay(true)
	if !in.Shown(true) {
		t.Fatal("hidden while the agent works")
	}
	for range 4 {
		lay(true)
	}
	if reads != 2 { // five frames 100ms apart, a read every 250ms at most: at 0 and 300
		t.Fatalf("read the screen %d times in 400ms, want 2", reads)
	}
	lay(false)
	if in.Shown(true) || in.Shown(false) {
		t.Fatal("shown after the agent went idle, before a new read")
	}
	lay(true)
	if !in.Shown(true) {
		t.Fatal("hidden when the agent works again")
	}
	// Click the close button: the inset's top right, in the corner of
	// the 900x600 column, then the 24dp button 2dp in.
	h := 28 + 1 + 8 + 6*(12*145/100) + 8
	top := 600 - h
	click := func(x, y float32) {
		r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(x, y)},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(x, y)})
	}
	click(900-2-12, float32(top+14))
	lay(true)
	if in.Shown(true) {
		t.Fatal("shown after close")
	}
	lay(false)
	lay(true)
	if in.Shown(true) {
		t.Fatal("a new turn brought a closed inset back")
	}
}

// TestInsetShot draws a running chat with the inset, in both layouts,
// and the same chat idle, at 1440x900 when PITWALL_RENDER_DIR is set.
func TestInsetShot(t *testing.T) {
	dir := os.Getenv("PITWALL_RENDER_DIR")
	if dir == "" {
		t.Skip("PITWALL_RENDER_DIR not set")
	}
	c, _ := config.Builtin("aide-dark")
	th, err := theme.New(c, config.Font{UISize: 13})
	if err != nil {
		t.Fatal(err)
	}
	const w, h = 1440, 900
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	snap := renderSnap(t, filepath.Join("testdata", "ask-then-work-running.jsonl"))
	for _, lay := range []string{LayoutLedger, LayoutColumn} {
		for _, working := range []bool{true, false} {
			var tr Transcript
			var cmp chatin.Composer
			var in Inset
			tr.Width, tr.ChatLayout = 760, lay
			s := snap
			s.Running = working
			now := time.Date(2026, 11, 27, 17, 0, 0, 0, time.UTC)
			var ops op.Ops
			for range 3 {
				ops.Reset()
				now = now.Add(300 * time.Millisecond)
				gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(w, h)), Now: now}
				env := chatin.Env{Chat: "c", Snap: &s, Width: 760, Focus: true, Ledger: lay == LayoutLedger, Gutter: LedgerGutter(s.Provider)}
				layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						d := tr.Layout(gtx, th, &s, false)
						in.Layout(gtx, th, "Claude", working, InsetColumn(gtx, env), func() (vt.Grid, bool) { return claudeScreen, true })
						return d
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions { return cmp.Layout(gtx, th, env) }),
				)
			}
			if err := win.Frame(&ops); err != nil {
				t.Fatal(err)
			}
			img := image.NewRGBA(image.Rect(0, 0, w, h))
			if err := win.Screenshot(img); err != nil {
				t.Fatal(err)
			}
			name := "inset-" + strings.ToLower(lay)
			if !working {
				name += "-idle"
			}
			f, err := os.Create(filepath.Join(dir, name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, img)
			f.Close()
		}
	}
}

// BenchmarkInset is one frame of the inset between screen reads: the
// body replays its recorded text.
func BenchmarkInset(b *testing.B) {
	th := theme.Dark()
	var in Inset
	var ops op.Ops
	now := time.Now()
	frame := func() (vt.Grid, bool) { return claudeScreen, true }
	for b.Loop() {
		ops.Reset()
		now = now.Add(16 * time.Millisecond)
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(900, 600)), Now: now}
		in.Layout(gtx, th, "Claude", true, image.Rect(0, 0, 900, 600), frame)
	}
}
