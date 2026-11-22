package term

import (
	"image/color"
	"testing"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestSelectionColorsFromTheme(t *testing.T) {
	cells := []vt.Cell{{Content: "a", Width: 1}, {Content: "b", Width: 1}}

	// Without custom selection colors, selected() blends over cell bg and keeps fg.
	thDefault := &theme.Theme{
		TermFg: color.NRGBA{R: 0xe0, G: 0xe0, B: 0xe0, A: 0xff},
		TermBg: color.NRGBA{R: 0x10, G: 0x10, B: 0x10, A: 0xff},
	}
	v := &View{th: thDefault}
	st := v.resolveStyles(nil, cells, 0, 1)
	if st[0].bg != selected(thDefault.TermBg) || st[0].fg != thDefault.TermFg {
		t.Fatalf("default selection: bg=%v fg=%v", st[0].bg, st[0].fg)
	}

	// With custom TermSelBg / TermSelFg, selected cells use them directly.
	selBg := color.NRGBA{R: 0x33, G: 0x55, B: 0x77, A: 0xff}
	selFg := color.NRGBA{R: 0xff, G: 0xee, B: 0xdd, A: 0xff}
	thCustom := &theme.Theme{
		TermFg:    thDefault.TermFg,
		TermBg:    thDefault.TermBg,
		TermSelBg: selBg,
		TermSelFg: selFg,
	}
	v.th = thCustom
	st = v.resolveStyles(nil, cells, 0, 1)
	if st[0].bg != selBg || st[0].fg != selFg {
		t.Fatalf("custom selection: bg=%v fg=%v, want %v %v", st[0].bg, st[0].fg, selBg, selFg)
	}
	if st[1].bg != thCustom.TermBg || st[1].fg != thCustom.TermFg {
		t.Fatalf("unselected cell changed: bg=%v fg=%v", st[1].bg, st[1].fg)
	}
}
