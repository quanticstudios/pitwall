package panel

import (
	"strings"
	"unicode/utf8"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/mdview"
)

// nbsp pads a chip: a no-break space, so the chip never breaks across
// lines.
const nbsp = " "

// chip is an [Image #N] token as the prompt draws it: label is the rune
// range of its name in the drawn text.
type chip struct {
	label [2]int
	name  string
}

// ctext is a prompt with its tokens widened for chips, and the way back
// from the drawn text to the typed one.
type ctext struct {
	text  string
	chips []chip
	orig  []rune
	// lo and hi are, per rune of text, the typed runes it stands for:
	// all of a token for each rune of its chip.
	lo, hi []int
	// head is a line typed above the text that the bubble draws apart,
	// pitwall's sender header: a copy of the whole text gives it back.
	head string
}

// chipText widens each [Image #N] in s to lead no-break spaces, the
// name "Image #N" and trail spaces: the room a chip's icon and padding
// take.
func chipText(s string, lead, trail int) ctext {
	return spansChips([]mdview.Span{{Text: s}}, nil, [2]int{lead, trail}).ct
}

// typed is the typed text that drawn runes [a, b) stand for.
func (ct ctext) typed(a, b int) string {
	if ct.head != "" && a <= 0 && b >= len([]rune(ct.text)) {
		body := ct
		body.head = ""
		return ct.head + "\n" + body.typed(a, b)
	}
	if ct.orig == nil {
		r := []rune(ct.text)
		return string(r[max(0, a):min(b, len(r))])
	}
	if a >= b || a >= len(ct.lo) {
		return ""
	}
	b = min(b, len(ct.hi))
	return string(ct.orig[ct.lo[a]:ct.hi[b-1]])
}

// chipPad is how many no-break spaces go before and after a chip's
// name in text styled st: 6 dp, a 1 em icon and 4 dp before, 6 dp after.
func (d *cdraw) chipPad(gtx layout.Context, st tstyle) (lead, trail int) {
	w := func(s string) int {
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints = layout.Constraints{Max: gtx.Constraints.Max}
		g.Constraints.Max.X = 1 << 20
		dims := widget.Label{MaxLines: 1}.Layout(g, d.th.Shaper, st.f, st.size, s, op.CallOp{})
		m.Stop()
		return dims.Size.X
	}
	sp := max(1, (w("x"+strings.Repeat(nbsp, 8)+"x")-w("xx"))/8)
	need := gtx.Dp(6) + gtx.Sp(st.size) + gtx.Dp(4)
	return (need + sp - 1) / sp, (gtx.Dp(6) + sp - 1) / sp
}

// copyAs has the selection copy ct's part of sl's text as typed, so a
// chip copies as its token.
func (d *cdraw) copyAs(sl *widget.Selectable, ct ctext) {
	if key := d.t.msg.keys[sl]; key != "" && (ct.orig != nil || ct.head != "") {
		d.t.span.typed[key] = ct
	}
}

// tabbed is shown, orig with each tab drawn as mdview.Detab's four
// spaces, or the first part of it, mapped back to orig: a selection of
// code copies its tabs.
func tabbed(orig, shown string) ctext {
	ct := ctext{text: shown}
	if !strings.Contains(orig, "\t") {
		return ct
	}
	ct.orig = []rune(orig)
	n := utf8.RuneCountInString(shown)
	for i, r := range ct.orig {
		w := 1
		if r == '\t' {
			w = 4
		}
		for range w {
			ct.lo, ct.hi = append(ct.lo, i), append(ct.hi, i+1)
		}
	}
	ct.lo, ct.hi = ct.lo[:min(n, len(ct.lo))], ct.hi[:min(n, len(ct.hi))]
	return ct
}
