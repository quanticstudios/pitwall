package panel

import (
	"strconv"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"
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
}

// chipText widens each [Image #N] in s to lead no-break spaces, the
// name "Image #N" and trail spaces: the room a chip's icon and padding
// take.
func chipText(s string, lead, trail int) ctext {
	refs := imageRefs(s)
	if len(refs) == 0 {
		return ctext{text: s}
	}
	var b strings.Builder
	ct := ctext{orig: []rune(s)}
	n, at := 0, 0 // runes written, and the typed rune where s[last:] starts
	last := 0
	put := func(str string, lo, hi int) {
		for range str {
			ct.lo, ct.hi = append(ct.lo, lo), append(ct.hi, hi)
			n++
		}
		b.WriteString(str)
	}
	for _, r := range refs {
		for _, c := range s[last:r.start] {
			put(string(c), at, at+1)
			at++
		}
		tok := len([]rune(s[r.start:r.end]))
		name := "Image" + nbsp + "#" + strconv.Itoa(r.n)
		put(strings.Repeat(nbsp, lead), at, at+tok)
		start := n
		put(name, at, at+tok)
		ct.chips = append(ct.chips, chip{label: [2]int{start, n}, name: strings.ReplaceAll(name, nbsp, " ")})
		put(strings.Repeat(nbsp, trail), at, at+tok)
		at += tok
		last = r.end
	}
	for _, c := range s[last:] {
		put(string(c), at, at+1)
		at++
	}
	ct.text = b.String()
	return ct
}

// typed is the typed text that drawn runes [a, b) stand for.
func (ct ctext) typed(a, b int) string {
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

// copyAs takes Ctrl+C on sl before the selectable does, and copies the
// typed text the selection stands for, so a chip copies as its token.
func (d *cdraw) copyAs(gtx layout.Context, sl *widget.Selectable, ct ctext) {
	if ct.orig == nil {
		return
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: sl, Name: "C", Required: key.ModShortcut})
		if !ok {
			return
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			a, b := sl.Selection()
			if a > b {
				a, b = b, a
			}
			if s := ct.typed(a, b); s != "" {
				d.t.copy(gtx, "selection", s)
			}
		}
	}
}
