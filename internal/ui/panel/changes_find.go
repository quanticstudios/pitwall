package panel

import (
	"strconv"
	"strings"

	gioevent "gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// sideFind is the Changes view's find: a query found in every file's
// diff box, each match the n-th in its box, top to bottom.
type sideFind struct {
	FindBar
	sig  string // the query and the turn the hits are of
	hits []sideHit
	cur  int // the current hit; -1 for none
	jump bool
	over bool // the pointer is on the panel, so Ctrl+F finds in it
	tag  int  // the panel's area, for over
}

// sideHit is the n-th match in file file's box, on its line line.
type sideHit struct {
	file, n, line int
	box           string
}

// boxText is lines as a box's selectable holds them: a line each, tabs
// spaced out.
func boxText(lines []monoLine) string {
	var sb strings.Builder
	for i, l := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(mdview.Detab(l.text))
	}
	return sb.String()
}

// sideBox is file f of turn's box id in the Changes view.
func sideBox(turn string, f fileChange) string { return "side:" + turn + ":" + f.path }

// sideHits are q's matches, folded, in every file's diff of turn's
// changes fs, as the Changes view draws them: file by file, and in each
// top to bottom.
func sideHits(turn string, fs []fileChange, q []rune) []sideHit {
	var out []sideHit
	if len(q) == 0 {
		return nil
	}
	for k, f := range fs {
		txt := session.Fold(boxText(diffMono(changeLines(f))))
		line, from := 0, 0
		for n, a := range session.FindRunes(txt, q) {
			for _, c := range txt[from:a] {
				if c == '\n' {
					line++
				}
			}
			from = a
			out = append(out, sideHit{file: k, n: n, line: line, box: sideBox(turn, f)})
		}
	}
	return out
}

// sideFindUpdate takes the find bar's keys, finds again when the query or
// the turn changed, and moves the current match.
func (t *Transcript) sideFindUpdate(gtx layout.Context, turn string, fs []fileChange) {
	sf := &t.side.find
	if !sf.on {
		return
	}
	step, shut := sf.Update(gtx)
	if shut {
		sf.hits, sf.sig, sf.jump = nil, "", false
		return
	}
	q := sf.Query()
	sig := string(q) + "\x00" + turn + "\x00" + strconv.Itoa(len(fs))
	if sig != sf.sig {
		sf.sig, sf.hits, sf.cur = sig, sideHits(turn, fs, q), -1
		if len(sf.hits) > 0 {
			sf.cur, sf.jump = 0, true
		}
	}
	if step != 0 && len(sf.hits) > 0 {
		sf.cur, sf.jump = wrap(sf.cur, step, len(sf.hits)), true
	}
}

// sideFindJump scrolls the Changes view to the current match: its file's
// row, offset to its line, a little under the find bar.
func (t *Transcript) sideFindJump(gtx layout.Context, d *cdraw) {
	sf := &t.side.find
	if !sf.on || !sf.jump || sf.cur < 0 || sf.cur >= len(sf.hits) {
		return
	}
	sf.jump = false
	h := sf.hits[sf.cur]
	st := d.mono()
	lh := int(float32(gtx.Sp(st.size))*st.lh + 0.5)
	y := gtx.Dp(hit+theme.SpaceXS) + gtx.Dp(6) + h.line*lh
	t.side.list.Position = layout.Position{First: h.file + 1, Offset: max(0, y-sf.Height(gtx)-2*lh)}
}

// sideCount is the Changes find bar's count: "3 of 12", "No results",
// "" without a query.
func (sf *sideFind) count() string {
	switch {
	case len(sf.Query()) == 0:
		return ""
	case len(sf.hits) == 0:
		return "No results"
	}
	return strconv.Itoa(sf.cur+1) + " of " + strconv.Itoa(len(sf.hits))
}

// sideOver notes whether the pointer is on the panel, for OpenFind. It
// goes in the panel's own clip area, before what the panel draws, so the
// area holds all of it.
func (t *Transcript) sideOver(gtx layout.Context) {
	sf := &t.side.find
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &sf.tag, Kinds: pointer.Enter | pointer.Leave | pointer.Move})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok {
			sf.over = e.Kind != pointer.Leave
		}
	}
	gioevent.Op(gtx.Ops, &sf.tag)
}

// sideMarks paints the Changes find's matches in box key's text txt,
// laid out in sl, under it.
func (t *Transcript) sideMarks(gtx layout.Context, d *cdraw, sl *widget.Selectable, key, txt string) {
	sf := &t.side.find
	q := sf.Query()
	box, ok := strings.CutPrefix(key, "box:")
	if !sf.on || len(q) == 0 || !ok {
		return
	}
	cur := sideHit{n: -1}
	if sf.cur >= 0 && sf.cur < len(sf.hits) {
		cur = sf.hits[sf.cur]
	}
	other, now := theme.Mix(d.th.Bg, d.th.Yellow, 0.28), theme.Mix(d.th.Bg, d.th.Yellow, 0.62)
	var rg []widget.Region
	for n, a := range session.FindRunes(session.Fold(txt), q) {
		c := other
		if box == cur.box && n == cur.n {
			c = now
		}
		rg = sl.Regions(a, a+len(q), rg[:0])
		for _, g := range rg {
			paint.FillShape(gtx.Ops, c, clip.UniformRRect(g.Bounds.Inset(-gtx.Dp(1)), gtx.Dp(theme.Space2XS)).Op(gtx.Ops))
		}
	}
}
