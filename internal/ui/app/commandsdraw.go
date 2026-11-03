package app

import (
	"image"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Adapted from tuios (MIT): internal/app/render_scrollback_browser.go
func (u *ui) drawCommandBrowser(gtx gl.Context) {
	b, th := &u.browser, u.th
	if !b.open {
		return
	}
	if reader, ok := u.b.(CommandReader); ok {
		for _, r := range reader.CommandResults() {
			b.take(r)
		}
	}
	if b.close.Clicked(gtx) {
		b.open = false
		return
	}
	if b.docs.Clicked(gtx) {
		openLink(shellIntegrationURL)
	}
	if b.copyCommand.Clicked(gtx) {
		b.copy = "command"
	}
	if b.copyOutput.Clicked(gtx) {
		b.copy = "output"
	}
	if b.copyBoth.Clicked(gtx) {
		b.copy = "both"
	}
	for i := range b.rows {
		if b.rows[i].Clicked(gtx) {
			b.selectBlock(i)
		}
	}
	if u.backdrop(gtx, 1, &b.backdrop) {
		b.open = false
		return
	}
	size := gtx.Constraints.Max
	w, h := min(gtx.Dp(1040), size.X-gtx.Dp(32)), min(gtx.Dp(700), size.Y-gtx.Dp(32))
	defer u.overlayCard(gtx, 1, w, h, &b.card)()
	pad := gtx.Dp(16)
	inner := w - 2*pad
	text := func(x, y int, s string, title bool) {
		font, sp, col := th.UIFont, th.Sp(theme.Small), th.Muted
		if title {
			font, sp, col = semibold(font), th.Sp(theme.Title), th.Fg
		}
		g := gtx
		g.Constraints.Max.X = max(0, w-x-pad)
		drawText(g, th, image.Pt(x, y), font, sp, col, s)
	}
	text(pad, pad, "Scrollback commands", true)
	text(pad, pad+gtx.Dp(30), u.commandBrowserStatus(), false)
	o := op.Offset(image.Pt(w-pad-gtx.Dp(56), pad)).Push(gtx.Ops)
	kit.Button(gtx, th, &b.close, kit.Ghost, kit.Small, "Close")
	o.Pop()
	top, bottom := pad+gtx.Dp(60), h-pad-gtx.Dp(70)
	if len(b.result.Blocks) == 0 {
		o := op.Offset(image.Pt(pad, top)).Push(gtx.Ops)
		kit.Button(gtx, th, &b.docs, kit.Secondary, kit.Medium, "Shell integration docs")
		o.Pop()
	} else {
		list := image.Rect(pad, top, pad+inner*2/5, bottom)
		preview := image.Rect(list.Max.X+pad, top, w-pad, bottom)
		if w < gtx.Dp(780) {
			list = image.Rect(pad, top, w-pad, top+(bottom-top)/2)
			preview = image.Rect(pad, list.Max.Y+gtx.Dp(8), w-pad, bottom)
		}
		u.commandList(gtx, list)
		u.commandPreview(gtx, preview)
	}
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect(image.Rect(pad, bottom+gtx.Dp(8), w-pad, bottom+gtx.Dp(9))).Op())
	o = op.Offset(image.Pt(pad, bottom+gtx.Dp(16))).Push(gtx.Ops)
	gl.Flex{Axis: gl.Horizontal}.Layout(gtx,
		gl.Rigid(func(g gl.Context) gl.Dimensions {
			return kit.ButtonHint(g, th, &b.copyCommand, kit.Secondary.When(len(b.rows) > 0), kit.Small, "Command", "c")
		}),
		gl.Rigid(gl.Spacer{Width: 8}.Layout),
		gl.Rigid(func(g gl.Context) gl.Dimensions {
			return kit.ButtonHint(g, th, &b.copyOutput, kit.Secondary.When(len(b.rows) > 0), kit.Small, "Output", "y")
		}),
		gl.Rigid(gl.Spacer{Width: 8}.Layout),
		gl.Rigid(func(g gl.Context) gl.Dimensions {
			return kit.ButtonHint(g, th, &b.copyBoth, kit.Secondary.When(len(b.rows) > 0), kit.Small, "Both", "Shift+Y")
		}),
	)
	o.Pop()
	o = op.Offset(image.Pt(pad, h-pad-gtx.Dp(22))).Push(gtx.Ops)
	u.drawHints(gtx, gtx.Dp(22), [][2]string{{"↑↓", "move"}, {"PgUp/Dn", "output"}, {"Esc", "close"}})
	o.Pop()
	u.copyCommandBlock(gtx)
}

func (u *ui) commandList(gtx gl.Context, rect image.Rectangle) {
	b, th := &u.browser, u.th
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	defer clip.Rect{Max: rect.Size()}.Push(gtx.Ops).Pop()
	gtx.Constraints = gl.Exact(rect.Size())
	b.list.Layout(gtx, len(b.rows), func(g gl.Context, i int) gl.Dimensions {
		size := image.Pt(rect.Dx(), g.Dp(56))
		c := &b.rows[i]
		g.Constraints = gl.Exact(size)
		c.Layout(g, func(gl.Context) gl.Dimensions { return gl.Dimensions{Size: size} })
		box := image.Rectangle{Max: size}.Inset(g.Dp(2))
		if i == b.sel || c.Hovered() {
			u.highlight(g, box, false)
		}
		if i == b.sel {
			u.highlight(g, box, true)
		}
		block := b.result.Blocks[i]
		o := op.Offset(image.Pt(g.Dp(10), g.Dp(6))).Push(g.Ops)
		lg := g
		lg.Constraints.Max.X -= g.Dp(20)
		widget.Label{MaxLines: 1, Truncator: "…"}.Layout(lg, th.Shaper, th.MonoFont, th.Sp(theme.Body), block.Command, colorCall(g, th.Fg))
		o.Pop()
		col := th.Muted
		if block.Finished && block.StatusKnown && block.ExitCode != 0 {
			col = th.Red
		}
		o = op.Offset(image.Pt(g.Dp(10), g.Dp(30))).Push(g.Ops)
		widget.Label{MaxLines: 1, Truncator: "…"}.Layout(lg, th.Shaper, th.UIFont, th.Sp(theme.Small), commandBlockMeta(block), colorCall(g, col))
		o.Pop()
		return gl.Dimensions{Size: size}
	})
}

func (u *ui) commandPreview(gtx gl.Context, rect image.Rectangle) {
	b, th := &u.browser, u.th
	if b.sel >= len(b.result.Blocks) {
		return
	}
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	defer clip.Rect{Max: rect.Size()}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rectangle{Max: rect.Size()}, gtx.Dp(8)).Op(gtx.Ops))
	gtx.Constraints = gl.Exact(rect.Size())
	lines := b.output
	gl.UniformInset(10).Layout(gtx, func(g gl.Context) gl.Dimensions {
		return b.preview.Layout(g, len(lines), func(g gl.Context, i int) gl.Dimensions {
			g.Constraints.Min.Y = 0
			s := lines[i]
			if s == "" {
				s = " "
			}
			return widget.Label{}.Layout(g, th.Shaper, th.MonoFont, th.Sp(theme.Body), s, colorCall(g, th.Fg))
		})
	})
}
