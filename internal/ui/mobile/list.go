package mobile

import (
	"image"
	"image/color"
	"slices"
	"strconv"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// listView is the start screen: the sidebar's tree of one session.
type listView struct {
	menu, sessions widget.Clickable
}

func (a *App) listLayout(gtx C) {
	th := a.th
	if a.list.menu.Clicked(gtx) || a.list.sessions.Clicked(gtx) {
		a.sheet = sheetView{open: "menu"}
	}
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return a.bar(gtx, nil, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Title), th.Fg, 1, "pitwall")
					}),
					layout.Flexed(1, func(gtx C) D {
						s := a.st.Session(a.session)
						if s == nil {
							return D{}
						}
						return layout.Inset{Left: 10}.Layout(gtx, func(gtx C) D { return a.sessionChip(gtx, s.Name) })
					}),
				)
			}, func(gtx C) D { return iconButton(gtx, th, &a.list.menu, dots) })
		}),
		layout.Rigid(a.offlineLine),
		layout.Flexed(1, func(gtx C) D {
			if len(a.tabs) == 0 {
				fillMax(gtx, th.Sidebar)
				msg := "No tabs are open."
				if !a.online {
					msg = ""
				}
				return layout.Center.Layout(gtx, func(gtx C) D { return body(gtx, th, th.Muted, msg) })
			}
			_, evs := a.sb.Layout(gtx, th, &a.st, a.session, "")
			for _, ev := range evs {
				if sel, ok := ev.(sidebar.SelectWorkspace); ok {
					if i := slices.IndexFunc(a.tabs, func(t remote.Tab) bool { return t.ID == sel.WorkspaceID }); i >= 0 {
						a.openChat(a.tabs[i].Pane)
					}
				}
			}
			return D{Size: gtx.Constraints.Max}
		}),
	)
}

// sessionChip names the session shown, with how many of the other
// sessions' tabs wait on you; it opens the menu, where they switch.
func (a *App) sessionChip(gtx C, name string) D {
	th := a.th
	others := 0
	for _, s := range a.st.Sessions {
		if s.ID != a.session {
			others += phone.NeedsYou(&a.st, s.ID)
		}
	}
	return a.list.sessions.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min = image.Point{}
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Muted, 1, name)
			}),
			layout.Rigid(func(gtx C) D {
				if len(a.st.Sessions) < 2 {
					return D{}
				}
				return layout.Inset{Left: 4, Top: 1}.Layout(gtx, func(gtx C) D {
					chevronDown(gtx, th.Muted, gtx.Dp(12))
					return D{Size: image.Pt(gtx.Dp(12), gtx.Dp(12))}
				})
			}),
			layout.Rigid(func(gtx C) D {
				if others == 0 {
					return D{}
				}
				return layout.Inset{Left: 6}.Layout(gtx, func(gtx C) D { return badge(gtx, th, others) })
			}),
		)
	})
}

// badge is a count of tabs that wait on you, in the needs-you color.
func badge(gtx C, th *theme.Theme, n int) D {
	bg, fg := th.Chip(th.Yellow, th.Sidebar)
	return box(gtx, bg, color.NRGBA{}, 8, layout.Inset{Left: 6, Right: 6, Top: 1, Bottom: 1}, func(gtx C) D {
		return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Caption), fg, 1, strconv.Itoa(n))
	})
}

// bar is the top bar: lead (a Back button, or nil), the middle, and the
// trailing buttons, over a line.
func (a *App) bar(gtx C, lead, mid, trail layout.Widget) D {
	th := a.th
	h := gtx.Dp(barH)
	paint.FillShape(gtx.Ops, th.Sidebar, clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, h)}.Op())
	gtx.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X, h))
	left := unit.Dp(4)
	if lead == nil {
		left = pad + 2
	}
	layout.Inset{Left: left, Right: 4}.Layout(gtx, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				if lead == nil {
					return D{}
				}
				return lead(gtx)
			}),
			layout.Flexed(1, func(gtx C) D {
				gtx.Constraints.Min.Y = 0
				return layout.W.Layout(gtx, mid)
			}),
			layout.Rigid(func(gtx C) D {
				if trail == nil {
					return D{}
				}
				return trail(gtx)
			}),
		)
	})
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, h-1), Max: image.Pt(gtx.Constraints.Max.X, h)}.Op())
	return D{Size: image.Pt(gtx.Constraints.Max.X, h)}
}

// offlineLine says the stream is down while it reconnects.
func (a *App) offlineLine(gtx C) D {
	if a.online {
		return D{}
	}
	th := a.th
	msg := "Connecting"
	if a.offline != "" {
		msg = "Offline, reconnecting"
	}
	bg, fg := th.Chip(th.Yellow, th.Bg)
	return box(gtx, bg, color.NRGBA{}, 0, layout.Inset{Left: pad + 2, Right: pad, Top: 6, Bottom: 6}, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return label(gtx, th, th.UIFont, th.Sp(theme.Small), fg, 1, msg)
	})
}

// dots is the overflow menu's glyph: three dots across.
func dots(gtx C, col color.NRGBA, size int) {
	r := max(size/8, 1)
	for i := range 3 {
		x := size/2 + (i-1)*size*3/10
		paint.FillShape(gtx.Ops, col, clip.Ellipse(image.Rect(x-r, size/2-r, x+r, size/2+r)).Op(gtx.Ops))
	}
}
