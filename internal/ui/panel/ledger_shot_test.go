package panel

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// ledgerFrame draws a chat the way the app does, the transcript over the
// composer, w by h px at scale 1, and returns the picture and the
// composer's height.
func ledgerFrame(t *testing.T, th *theme.Theme, s *session.Snapshot, chatLayout string, w, h int) (*image.RGBA, int) {
	t.Helper()
	return ledgerFrameMode(t, th, s, chatLayout, ToolsAll, w, h)
}

// ledgerFrameMode is ledgerFrame with tool calls shown as mode says.
func ledgerFrameMode(t *testing.T, th *theme.Theme, s *session.Snapshot, chatLayout, mode string, w, h int) (*image.RGBA, int) {
	t.Helper()
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	tr := Transcript{Dir: "/repo", ChatLayout: chatLayout, ToolCalls: mode, Width: 760}
	var cmp chatin.Composer
	var ops op.Ops
	now := time.Date(2026, 11, 21, 14, 4, 0, 0, time.UTC)
	ch := 0
	for range 3 {
		ops.Reset()
		now = now.Add(16 * time.Millisecond)
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(w, h)), Now: now}
		env := chatin.Env{Chat: "c", Snap: s, Width: 760, Gutter: LedgerGutter(s.Provider), Ledger: chatLayout == LayoutLedger, Key: tr.Key, Answer: tr.Answer, Branch: "ledger-align"}
		layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return tr.Layout(gtx, th, s, false) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d := cmp.Layout(gtx, th, env)
				ch = d.Size.Y
				return d
			}),
		)
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	return img, ch
}

// inked is whether the pixel at x, y differs from bg past antialiasing noise.
func inked(img *image.RGBA, x, y int, bg color.NRGBA) bool {
	c := img.RGBAAt(x, y)
	d := func(a, b uint8) int { return max(int(a)-int(b), int(b)-int(a)) }
	return d(c.R, bg.R)+d(c.G, bg.G)+d(c.B, bg.B) > 12
}

// inkSpan is the leftmost and rightmost inked x in rows y0..y1 between
// x0 and x1, -1 for none.
func inkSpan(img *image.RGBA, x0, x1, y0, y1 int, bg color.NRGBA) (lo, hi int) {
	lo, hi = -1, -1
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if inked(img, x, y, bg) {
				if lo < 0 || x < lo {
					lo = x
				}
				hi = max(hi, x)
			}
		}
	}
	return lo, hi
}

// TestLedgerTextMeetsComposer draws a Ledger chat with one prompt headless
// and checks the pixels: the composer card's left and right edges are the
// text column's, the prompt's text starts at that left edge, and no
// bubble fills behind it.
func TestLedgerTextMeetsComposer(t *testing.T) {
	th := theme.Dark()
	when := time.Date(2026, 11, 21, 14, 1, 0, 0, time.UTC).Format(time.RFC3339Nano)
	turn := "t1"
	s := &session.Snapshot{SessionID: "s1", Provider: model.ProviderClaude, SessionState: session.StateReady, Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "Fix large pasted images.", TurnID: &turn, CreatedAt: when, UpdatedAt: when},
	}}
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	for _, w := range []int{1600, 1000, 600} {
		const h = 420
		img, ch := ledgerFrame(t, th, s, LayoutLedger, w, h)
		g := ledgerMetrics(gtx, s.Provider, w)

		// The card: the composer's widest inked span, under its status line.
		lo, hi := inkSpan(img, 0, w, h-ch, h, th.Bg)
		if lo != g.textX || hi != g.textX+g.textW-1 {
			t.Errorf("at %d: composer card spans %d..%d, text column %d..%d", w, lo, hi, g.textX, g.textX+g.textW-1)
		}

		// The prompt's row: its text starts at the column's edge, left aligned.
		y0 := gtx.Dp(theme.SpaceM)
		y1 := y0 + gtx.Dp(40)
		if !g.outside {
			y0 += d0HeadH(th, gtx)
			y1 += d0HeadH(th, gtx)
		}
		tlo, thi := inkSpan(img, g.textX, g.textX+g.textW, y0, y1, th.Bg)
		if tlo < g.textX || tlo > g.textX+gtx.Dp(3) || thi > g.textX+g.textW/2 {
			t.Errorf("at %d: prompt text inked %d..%d, want it from %d on the left", w, tlo, thi, g.textX)
		}
		// No bubble: a filled box would ink the whole row's height at its edges.
		bubble := 0
		for y := y0; y < y1; y++ {
			if c := img.RGBAAt(g.textX+1, y); c.R == th.SurfaceSecondary.R && c.G == th.SurfaceSecondary.G && c.B == th.SurfaceSecondary.B {
				bubble++
			}
		}
		if bubble > 4 {
			t.Errorf("at %d: %d px of bubble fill behind the prompt", w, bubble)
		}
		// The gutter: You and the time beside the text when it fits, else above it.
		if g.outside {
			if glo, _ := inkSpan(img, 0, g.textX, y0, y1, th.Bg); glo != g.x0 && glo != g.x0+1 && glo != g.x0+2 {
				t.Errorf("at %d: gutter inked from %d, want %d", w, glo, g.x0)
			}
		} else if glo, _ := inkSpan(img, 0, g.textX, 0, h-ch, th.Bg); glo >= 0 {
			t.Errorf("at %d: gutter above the text, yet ink left of the column at %d", w, glo)
		}
	}
}

func d0HeadH(th *theme.Theme, gtx layout.Context) int {
	d := &cdraw{th: th}
	return d.ledgerHeadH(gtx) + gtx.Dp(theme.SpaceXS)
}

// TestLedgerAlignShots renders Ledger at 1600, 1000 and 600 px panes and
// Column at 1000 px, with a reply, tool rows, the user's messages and the
// composer, to PITWALL_LEDGER_SHOTS for the PR.
func TestLedgerAlignShots(t *testing.T) {
	dir := os.Getenv("PITWALL_LEDGER_SHOTS")
	if dir == "" {
		t.Skip("set PITWALL_LEDGER_SHOTS to render Ledger PNGs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	th := theme.Dark()
	s := ledgerSnapshot()
	s.Running, s.SessionState, s.PendingApprovals = false, session.StateReady, nil
	for _, c := range []struct {
		name, layout string
		w            int
	}{
		{"ledger-1600.png", LayoutLedger, 1600},
		{"ledger-1000.png", LayoutLedger, 1000},
		{"ledger-600.png", LayoutLedger, 600},
		{"column-1000.png", LayoutColumn, 1000},
	} {
		img, _ := ledgerFrame(t, th, s, c.layout, c.w, 900)
		f, err := os.Create(filepath.Join(dir, c.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
