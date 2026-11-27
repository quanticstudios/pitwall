package panel

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Inset is the live corner view of an agent's terminal screen over its
// chat: the last few lines above the agent's input box, while it works.
// Keep one per chat; closing it hides it for that chat.
type Inset struct {
	open, close widget.Clickable
	closed      bool

	lines []string
	at    time.Time // when lines were last read from the screen

	// The body's text, recorded once per change of lines and replayed.
	ops  op.Ops
	body op.CallOp
	key  string
}

const (
	// InsetRefresh is how often the inset reads the screen again.
	InsetRefresh = 250 * time.Millisecond
	insetLines   = 6
	insetWidth   = unit.Dp(300)
)

// Shown reports whether the inset draws: the agent works, the user has
// not closed it, and the screen has text to show.
func (in *Inset) Shown(working bool) bool {
	return working && !in.closed && len(in.lines) > 0
}

// Layout draws the inset over the chat transcript while agent works:
// right-aligned in col's columns, its bottom at col.Max.Y, the composer's
// gap above the composer. frame reads the agent's screen, at most every
// InsetRefresh. It reports whether Open was clicked. It takes no keys and
// leaves pointer events off its buttons to the transcript under it.
func (in *Inset) Layout(gtx layout.Context, th *theme.Theme, agent string, working bool, col image.Rectangle, frame func() (vt.Grid, bool)) bool {
	if in.close.Clicked(gtx) {
		in.closed = true
	}
	opened := in.open.Clicked(gtx)
	if !working {
		in.lines, in.at = nil, time.Time{} // the next turn starts from a fresh screen
		return opened
	}
	if in.closed {
		return opened
	}
	if gtx.Now.Sub(in.at) >= InsetRefresh {
		in.at = gtx.Now
		if g, ok := frame(); ok {
			in.lines = screenTail(g, insetLines)
		}
	}
	if !in.Shown(working) {
		return opened
	}
	pad := gtx.Dp(theme.SpaceS)
	w := min(gtx.Dp(insetWidth), col.Dx())
	if w < gtx.Dp(180) { // too narrow a chat for a readable inset
		return opened
	}
	head := gtx.Dp(28)
	mono := th.Sp(theme.Small)
	lineH := gtx.Sp(mono) * 145 / 100
	h := head + 1 + pad + lineH*len(in.lines) + pad
	rect := image.Rect(col.Max.X-w, col.Max.Y-h, col.Max.X, col.Max.Y)
	if rect.Min.Y < h/2 { // a chat too short to keep the transcript readable
		return opened
	}
	// The composer's card: its radius, ring and fill, no shadow.
	rad := gtx.Dp(theme.RadiusCard)
	fill := th.Surface
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect, rad).Op(gtx.Ops))
	kit.Border(gtx, rect, rad, gtx.Dp(1), th.BorderStrong)
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	defer clip.UniformRRect(image.Rectangle{Max: rect.Size()}, rad).Push(gtx.Ops).Pop()

	// Header: "<agent> · live", then Open and close at the right.
	hg := gtx
	hg.Constraints = layout.Constraints{Max: image.Pt(w, head)}
	layout.Inset{Left: theme.SpaceM, Right: theme.Space2XS}.Layout(hg, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min.Y = head
				return layout.W.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, agent+" · live", 1)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return kit.Button(gtx, th, &in.open, kit.Ghost, kit.Small, "Open")
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return kit.IconButton(gtx, th, &in.close, kit.Ghost, kit.Small, func(gtx layout.Context, col color.NRGBA, size int) {
					sidebar.Icon(gtx, "x", size, col)
				})
			}),
		)
	})
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(1, head), Max: image.Pt(w-1, head+1)}.Op())

	// Body: the lines, drawn once per change and replayed between.
	fg := theme.Mix(fill, th.Fg, 0.8)
	key := fmt.Sprintf("%q %d %d %v %v", in.lines, w, lineH, fg, th.MonoFont)
	if key != in.key {
		in.key = key
		in.ops.Reset()
		bg := gtx
		bg.Ops = &in.ops
		m := op.Record(bg.Ops)
		for i, s := range in.lines {
			o := op.Offset(image.Pt(gtx.Dp(theme.SpaceM), i*lineH)).Push(bg.Ops)
			lg := bg
			lg.Constraints = layout.Constraints{Max: image.Pt(w-2*gtx.Dp(theme.SpaceM), lineH)}
			label(lg, th, th.MonoFont, mono, fg, s, 1)
			o.Pop()
		}
		in.body = m.Stop()
	}
	o := op.Offset(image.Pt(0, head+1+pad)).Push(gtx.Ops)
	in.body.Add(gtx.Ops)
	o.Pop()
	return opened
}

// InsetColumn is where the inset goes in a transcript area of gtx's size
// with composer env under it: the composer card's columns, and a bottom
// that leaves the composer's own gap, 12dp, between the two cards.
func InsetColumn(gtx layout.Context, env chatin.Env) image.Rectangle {
	full := gtx.Constraints.Max.X
	// As Composer.Layout places its card, whose top sits SpaceM below in
	// Column and SpaceXS below in Ledger.
	w := max(0, min(full-2*gtx.Dp(theme.SpaceXL), gtx.Dp(env.Width)))
	x, gap := (full-w)/2, 0
	if env.Ledger {
		x, w, _ = chatin.LedgerColumn(gtx, full, gtx.Dp(env.Gutter))
		gap = gtx.Dp(theme.SpaceM - theme.SpaceXS)
	}
	return image.Rect(x, 0, x+w, gtx.Constraints.Max.Y-gap)
}

// screenTail is the last n rows of text on screen g above the agent's
// input box, right-trimmed, with blank rows at the end dropped. The box is
// the one Claude Code draws between two rules near the bottom; a screen
// without one keeps all its rows.
func screenTail(g vt.Grid, n int) []string {
	var rows []string
	var b strings.Builder
	for y := range g.Rows {
		b.Reset()
		for x := range g.Cols {
			switch c := g.At(x, y); {
			case c.Width == 0:
			case c.Content == "":
				b.WriteByte(' ')
			default:
				b.WriteString(c.Content)
			}
		}
		rows = append(rows, strings.TrimRight(tuiGlyphs.Replace(b.String()), " "))
	}
	// Cut at the upper of the last two rules within the bottom rows.
	var rules []int
	for y := len(rows) - 1; y >= max(0, len(rows)-12) && len(rules) < 2; y-- {
		if ruleRow(rows[y]) {
			rules = append(rules, y)
		}
	}
	if len(rules) == 2 {
		rows = rows[:rules[1]]
	}
	for len(rows) > 0 && strings.TrimSpace(rows[len(rows)-1]) == "" {
		rows = rows[:len(rows)-1]
	}
	return rows[max(0, len(rows)-n):]
}

// tuiGlyphs swaps the marks Claude Code draws, which the chat's mono font
// lacks and would draw as boxes, for near ones it has.
var tuiGlyphs = strings.NewReplacer("⏺", "●", "⎿", "└", "✻", "*", "✶", "*", "✳", "*", "✢", "*", "✽", "*")

// ruleRow reports whether row is a horizontal rule, as Claude Code draws
// above and below its input box.
func ruleRow(row string) bool {
	return (strings.HasPrefix(row, "───") || strings.HasPrefix(row, "━━━")) &&
		(strings.HasSuffix(row, "─") || strings.HasSuffix(row, "━"))
}
