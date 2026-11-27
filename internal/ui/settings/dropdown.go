package settings

import (
	"image"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// dropdown is a dropdown of a few fixed values for table.k, labels
// naming opts, with cur picked; picking one saves it, as themePicker's
// list does.
func (p *Page) dropdown(table, k string, opts, labels []string, cur string) gl.Widget {
	ddKey := "pick:" + table + "." + k
	return func(gtx gl.Context) gl.Dimensions {
		th := p.th
		c := p.btn("dd:" + ddKey)
		for c.Clicked(gtx) {
			if p.dd == ddKey {
				p.dd = ""
			} else {
				p.dd, p.conflict, p.rec = ddKey, nil, slot{}
			}
		}
		label := cur
		for i, o := range opts {
			for p.btn(ddKey + ":" + o).Clicked(gtx) {
				p.dd = ""
				if o != cur {
					p.saveValue(table, k, config.Quote(o))
				}
			}
			if o == cur {
				label = labels[i]
			}
		}
		w, h := gtx.Dp(160), gtx.Dp(32)
		d := c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			border := th.Border
			if c.Hovered() || p.dd == ddKey {
				border = theme.Mix(th.Border, th.Fg, 0.25)
			}
			if gtx.Focused(c) {
				kit.FocusRing(gtx, th, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl))
			}
			rrect(gtx, border, image.Rect(0, 0, w, h), gtx.Dp(theme.RadiusControl))
			rrect(gtx, th.SurfaceElevated, image.Rect(1, 1, w-1, h-1), gtx.Dp(theme.RadiusControl)-1)
			m := op.Record(gtx.Ops)
			td := p.text(gtx, th.UIFont, th.Sp(theme.Body), th.Fg, label)
			call := m.Stop()
			o := op.Offset(image.Pt(gtx.Dp(10), (h-td.Size.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			o.Pop()
			is := gtx.Dp(14)
			o = op.Offset(image.Pt(w-gtx.Dp(10)-is, (h-is)/2)).Push(gtx.Ops)
			icon(gtx, th.Muted, is, chevronGlyph)
			o.Pop()
			defer clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops).Pop()
			pointer.CursorPointer.Add(gtx.Ops)
			return gl.Dimensions{Size: image.Pt(w, h)}
		})
		if p.dd == ddKey {
			p.dropList(gtx, ddKey, opts, labels, label, w, h)
		}
		return d
	}
}

// dropList is dropdown's open list under its trigger, w wide and top
// down, with the option labeled cur checked; a press outside it closes it.
func (p *Page) dropList(gtx gl.Context, ddKey string, opts, labels []string, cur string, w, top int) {
	th := p.th
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &p.ddTag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok {
			p.dd = ""
		}
	}
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &p.ddPanel, Kinds: pointer.Press}); !ok {
			break
		}
	}
	m := op.Record(gtx.Ops)
	bg := clip.Rect{Min: image.Pt(-1<<14, -1<<14), Max: image.Pt(1<<14, 1<<14)}.Push(gtx.Ops)
	event.Op(gtx.Ops, &p.ddTag)
	bg.Pop()
	itemH, pad := gtx.Dp(30), gtx.Dp(6)
	panel := image.Rect(0, 0, w, 2*pad+len(opts)*itemH)
	o := op.Offset(image.Pt(0, top+gtx.Dp(4))).Push(gtx.Ops)
	kit.Surface(gtx, panel, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	pa := clip.Rect(panel).Push(gtx.Ops)
	event.Op(gtx.Ops, &p.ddPanel)
	pa.Pop()
	for i, opt := range opts {
		c := p.btn(ddKey + ":" + opt)
		at := op.Offset(image.Pt(pad, pad+i*itemH)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(image.Pt(panel.Dx()-2*pad, itemH))
		c.Layout(g, func(gtx gl.Context) gl.Dimensions {
			sz := gtx.Constraints.Max
			on := labels[i] == cur
			if on {
				rrect(gtx, th.SelectedBg, image.Rectangle{Max: sz}, gtx.Dp(4))
			} else if c.Hovered() {
				rrect(gtx, theme.Mix(th.SurfaceElevated, th.Fg, 0.06), image.Rectangle{Max: sz}, gtx.Dp(4))
			}
			f := th.UIFont
			if on {
				f = weight(f, font.Medium)
				is := gtx.Dp(14)
				oo := op.Offset(image.Pt(sz.X-gtx.Dp(8)-is, (itemH-is)/2)).Push(gtx.Ops)
				icon(gtx, th.Primary, is, checkGlyph)
				oo.Pop()
			}
			mm := op.Record(gtx.Ops)
			d := p.text(gtx, f, th.Sp(theme.Body), th.Fg, labels[i])
			call := mm.Stop()
			oo := op.Offset(image.Pt(gtx.Dp(8), (itemH-d.Size.Y)/2)).Push(gtx.Ops)
			call.Add(gtx.Ops)
			oo.Pop()
			defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
			pointer.CursorPointer.Add(gtx.Ops)
			return gl.Dimensions{Size: sz}
		})
		at.Pop()
	}
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
}
