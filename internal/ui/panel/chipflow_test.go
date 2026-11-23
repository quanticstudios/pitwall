package panel

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// chipSnap is a chat of two messages that open with image chips: one on a
// line, one that wraps.
func chipSnap() *session.Snapshot {
	at := func(min int) string { return time.Date(2026, 11, 21, 14, min, 0, 0, time.UTC).Format(time.RFC3339Nano) }
	t1, t2 := "t1", "t2"
	return &session.Snapshot{SessionID: "s1", Provider: model.ProviderClaude, SessionState: session.StateReady, Content: []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "[Image #69] [Image #70] can we have radius on every corner", TurnID: &t1, CreatedAt: at(1), UpdatedAt: at(1)},
		{ID: "a1", Kind: session.KindAssistantText, Text: "Yes.", TurnID: &t1, CreatedAt: at(2), UpdatedAt: at(2)},
		{ID: "p2", Kind: session.KindUserPrompt, Text: "[Image #69] [Image #70] can we have radius on every corner on every hover state etc, its kinda looking out of the place, and [Image #71] here too", TurnID: &t2, CreatedAt: at(3), UpdatedAt: at(3)},
		{ID: "a2", Kind: session.KindAssistantText, Text: "Yes.", TurnID: &t2, CreatedAt: at(4), UpdatedAt: at(4)},
	}}
}

// TestChipFlowShots renders chips with text, on one line and wrapped, in
// Ledger and Column to $PITWALL_RENDER_DIR; it skips without it.
func TestChipFlowShots(t *testing.T) {
	dir := os.Getenv("PITWALL_RENDER_DIR")
	if dir == "" {
		t.Skip("PITWALL_RENDER_DIR is not set")
	}
	for _, c := range []struct{ name, layout string }{
		{"chips-ledger.png", LayoutLedger},
		{"chips-column.png", LayoutColumn},
	} {
		img, _ := ledgerFrame(t, theme.Dark(), chipSnap(), c.layout, 900, 520)
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

// TestChipFlow: chips flow like words. The first one starts at the
// text's left edge, and the gap between two chips is the gap between a
// chip and the word after it.
func TestChipFlow(t *testing.T) {
	th := theme.Dark()
	img, _ := ledgerFrame(t, th, chipSnap(), LayoutLedger, 900, 520)
	chipBg, _ := th.Chip(th.Blue, th.SurfaceSecondary)
	isChip := func(x, y int) bool { return near(img.RGBAAt(x, y), chipBg) }
	// top is the first row with chip color on it: the top of the first
	// message's chips.
	top := 0
	for ; top < 200; top++ {
		hit := false
		for x := range 900 {
			hit = hit || isChip(x, top)
		}
		if hit {
			break
		}
	}
	h := 16 // the chips' height, about
	// A column is a chip's when chip color shows anywhere in its height,
	// so the icon and label inside don't split it.
	col := func(x int) bool {
		for y := top; y < top+h; y++ {
			if isChip(x, y) {
				return true
			}
		}
		return false
	}
	var runs [][2]int
	for x := 0; x < 900; x++ {
		if col(x) {
			x0 := x
			for x < 900 && col(x) {
				x++
			}
			runs = append(runs, [2]int{x0, x})
		}
	}
	if len(runs) < 2 {
		t.Fatalf("chips %v, want two", runs)
	}
	ink := func(x0, x1, y0, y1 int) int { // the first column in x0..x1 with text on it
		for x := x0; x < x1; x++ {
			for y := y0; y < y1; y++ {
				if inked(img, x, y, th.Bg) {
					return x
				}
			}
		}
		return -1
	}
	left := ink(runs[0][0]-12, runs[0][0]+12, top+40, top+60) // the reply "Yes." under it, clear of its "Claude"
	word := ink(runs[1][1]+1, runs[1][1]+40, top, top+h)      // past the chip's antialiased edge
	t.Logf("chips %v, reply at %d, word at %d", runs, left, word)
	if d := runs[0][0] - left; d < -1 || d > 1 {
		t.Errorf("first chip starts at %d, the text at %d", runs[0][0], left)
	}
	if between, after := runs[1][0]-runs[0][1], word-runs[1][1]; between-after < -1 || between-after > 1 {
		t.Errorf("gap between chips %d px, after the chip %d px", between, after)
	}
}
