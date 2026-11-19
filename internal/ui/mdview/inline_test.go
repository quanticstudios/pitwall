package mdview

import (
	"testing"

	"gioui.org/font"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestInline: a notice's line loses its markers, and its bold, italic
// and code keep their ranges; a cut-off bold still draws bold.
func TestInline(t *testing.T) {
	th := theme.Dark()
	txt, st := Inline(th, th.UIFont, "**\"Uptime\"** is `up` and *fine*")
	if txt != "\"Uptime\" is up and fine" {
		t.Fatalf("text %q", txt)
	}
	if len(st) != 3 || st[0].Start != 0 || st[0].End != 8 || st[0].Font.Weight != font.SemiBold ||
		st[1].Start != 12 || st[1].End != 14 || st[1].Font != th.MonoFont ||
		st[2].Start != 19 || st[2].Font.Style != font.Italic {
		t.Errorf("styles %+v", st)
	}
	if txt, _ := Inline(th, th.UIFont, "# Summary"); txt != "Summary" {
		t.Errorf("heading %q", txt)
	}
	if txt, st := Inline(th, th.UIFont, "a **long cut…"); txt != "a long cut…" || len(st) != 1 {
		t.Errorf("cut %q %+v", txt, st)
	}
}
