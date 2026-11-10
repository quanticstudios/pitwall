package mobile

import (
	"image"
	"net/url"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// sheetView is the sheet over the bottom of the screen with the sessions
// and unpairing, open while open is "menu".
type sheetView struct {
	open   string
	scrim  widget.Clickable
	panel  int // the panel's pointer tag, so taps on it miss the scrim
	btns   map[string]*widget.Clickable
	unpair widget.Clickable
	list   layout.List
}

func (s *sheetView) btn(key string) *widget.Clickable {
	if s.btns == nil {
		s.btns = map[string]*widget.Clickable{}
	}
	b := s.btns[key]
	if b == nil {
		b = new(widget.Clickable)
		s.btns[key] = b
	}
	return b
}

func (a *App) sheetLayout(gtx C) {
	s, th := &a.sheet, a.th
	if s.open == "" {
		return
	}
	if s.scrim.Clicked(gtx) {
		a.sheet = sheetView{}
		return
	}
	var w []layout.Widget
	switch s.open {
	case "menu":
		w = a.menuSheet(gtx)
	}
	if s.open == "" { // a pick closed it
		return
	}
	s.scrim.Layout(gtx, func(gtx C) D {
		paint.FillShape(gtx.Ops, th.Scrim, clip.Rect{Max: gtx.Constraints.Max}.Op())
		return D{Size: gtx.Constraints.Max}
	})
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints.Min = image.Point{}
	g.Constraints.Max.Y = gtx.Constraints.Max.Y * 3 / 4
	d := layout.Inset{Left: pad + 4, Right: pad + 4, Top: 16, Bottom: 16}.Layout(g, func(gtx C) D {
		s.list.Axis = layout.Vertical
		return s.list.Layout(gtx, len(w), func(gtx C, i int) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return w[i](gtx)
		})
	})
	call := m.Stop()
	defer op.Offset(image.Pt(0, gtx.Constraints.Max.Y-d.Size.Y)).Push(gtx.Ops).Pop()
	r := gtx.Dp(theme.RadiusCard)
	rect := image.Rectangle{Max: image.Pt(gtx.Constraints.Max.X, d.Size.Y+r)}
	shape := clip.RRect{Rect: rect, NE: r, NW: r}
	paint.FillShape(gtx.Ops, th.BorderStrong, shape.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.RRect{Rect: image.Rect(0, 1, rect.Max.X, rect.Max.Y), NE: r, NW: r}.Op(gtx.Ops))
	area := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &s.panel)
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &s.panel, Kinds: pointer.Press | pointer.Release}); !ok {
			break
		}
	}
	area.Pop()
	call.Add(gtx.Ops)
}

func heading(th *theme.Theme, s string) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Bottom: 8}.Layout(gtx, func(gtx C) D {
			return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Small), th.Muted, 1, s)
		})
	}
}

// menuSheet switches sessions and unpairs.
func (a *App) menuSheet(gtx C) []layout.Widget {
	s, th := &a.sheet, a.th
	var w []layout.Widget
	if len(a.st.Sessions) > 0 {
		w = append(w, heading(th, "Sessions"))
	}
	for _, ss := range a.st.Sessions {
		b := s.btn("s:" + ss.ID)
		if b.Clicked(gtx) {
			a.session, a.sheet = ss.ID, sheetView{}
			return nil
		}
		id, name, n := ss.ID, ss.Name, phone.NeedsYou(&a.st, ss.ID)
		w = append(w, func(gtx C) D {
			return b.Layout(gtx, func(gtx C) D {
				h := gtx.Dp(touch + 6)
				gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, h)
				if id == a.session {
					paint.FillShape(gtx.Ops, th.SelectedBg, clip.UniformRRect(image.Rect(0, 0, gtx.Constraints.Max.X, h), gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
				}
				return layout.Inset{Left: 10, Right: 10}.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Flexed(1, func(gtx C) D { return body(gtx, th, th.Fg, name) }),
						layout.Rigid(func(gtx C) D {
							if n == 0 {
								return D{}
							}
							return badge(gtx, th, n)
						}),
					)
				})
			})
		})
	}
	if s.unpair.Clicked(gtx) {
		a.unpair("")
		return nil
	}
	host := a.client.Server
	if u, err := url.Parse(host); err == nil {
		host = u.Host
	}
	who := "This phone"
	if a.hello.Device != "" {
		who = "This phone, " + a.hello.Device + ","
	}
	w = append(w, gap(16), heading(th, "Pairing"), func(gtx C) D {
		return body(gtx, th, th.Fg, who+" is paired with "+host+". Unpairing forgets its token here; pitwall remote revoke ends it on the computer.")
	}, gap(12), func(gtx C) D {
		return layout.Flex{}.Layout(gtx, layout.Rigid(func(gtx C) D { return button(gtx, th, &s.unpair, kit.Danger, "Unpair this phone") }))
	})
	return w
}
