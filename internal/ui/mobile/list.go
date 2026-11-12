package mobile

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"strconv"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// listView is the start screen: the header, what waits on you in every
// session, and the sidebar's tree of one session.
type listView struct {
	sessions, settings, retry widget.Clickable
	// menu is the open menu: "sessions", "" for none.
	menu      string
	scrim     widget.Clickable
	rows      map[string]*widget.Clickable // the menu's sessions and the waiting rows, by id
	add       widget.Clickable
	note      string // why answering a row failed, or a machine left
	notePane  string
	noteAt    int64
	noteUntil time.Time
	// waitFold opens and closes Waiting on you; waitOpen is whether it
	// lists every tab or only a summary row.
	waitFold widget.Clickable
	waitOpen bool
}

func (l *listView) btn(id string) *widget.Clickable {
	if l.rows == nil {
		l.rows = map[string]*widget.Clickable{}
	}
	b := l.rows[id]
	if b == nil {
		b = new(widget.Clickable)
		l.rows[id] = b
	}
	return b
}

// rowH is a list row's height in the app's dp: 48dp once zoomed.
const rowH unit.Dp = 35

func (a *App) listLayout(gtx C) {
	th, l := a.th, &a.list
	l.expireNote(gtx.Now, l.noteAt, true)
	if !l.noteUntil.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: l.noteUntil})
	}
	if l.sessions.Clicked(gtx) && (len(a.st.Sessions) > 1 || a.many()) {
		l.menu = "sessions"
	}
	if l.settings.Clicked(gtx) {
		a.machines, l.menu = true, ""
		a.mach = machinesView{}
	}
	shown := a.shown()
	if l.retry.Clicked(gtx) && shown != nil {
		shown.conn.Retry()
	}
	if a.many() {
		a.sb.Hosts = a.hosts()
		a.sb.HostOf = func(id string) string {
			m, _ := a.fleet.Owner(id)
			return m.Name
		}
	} else {
		a.sb.Hosts, a.sb.HostOf = nil, nil
	}
	if l.menu != "" {
		// Back, or Esc on a desktop, closes the menu. With none open it
		// goes unread, so Android leaves the app.
		for {
			ev, ok := gtx.Event(key.Filter{Name: key.NameBack}, key.Filter{Name: key.NameEscape})
			if !ok {
				break
			}
			if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
				l.menu = ""
			}
		}
	}
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.listHeader),
		layout.Rigid(a.updateLine),
		layout.Rigid(a.updateProgress),
		layout.Rigid(a.offlineLine),
		layout.Rigid(a.waiting),
		layout.Flexed(1, func(gtx C) D {
			if len(a.st.Workspaces) == 0 {
				fillMax(gtx, th.Sidebar)
				msg := "No tabs are open."
				if shown == nil || !shown.online {
					msg = ""
				}
				return layout.Center.Layout(gtx, func(gtx C) D { return body(gtx, th, th.Muted, msg) })
			}
			if shown != nil && !shown.online && len(a.sessionLinks()) == 1 {
				// The last list, dimmed, until the stream is back. A
				// session on several machines dims a down machine's rows.
				defer paint.PushOpacity(gtx.Ops, 0.55).Pop()
			}
			_, evs := a.sb.Layout(gtx, th, &a.st, a.session, "")
			for _, ev := range evs {
				switch ev := ev.(type) {
				case sidebar.SelectWorkspace:
					a.openChat(ev.WorkspaceID)
				case sidebar.Answer:
					a.answerRow(ev)
				}
			}
			return D{Size: gtx.Constraints.Max}
		}),
	)
	if l.menu == "sessions" {
		a.sessionMenu(gtx)
	}
}

// shown is the link of the machine whose session the list shows: in a
// session on several machines the first that is not online, else the
// first.
func (a *App) shown() *link {
	ls := a.sessionLinks()
	for _, l := range ls {
		if !l.online {
			return l
		}
	}
	if len(ls) == 0 {
		return nil
	}
	return ls[0]
}

// answerRow presses Allow or Deny for a row's permission prompt.
func (a *App) answerRow(ev sidebar.Answer) {
	a.list.note = ""
	m, t, ok := a.fleet.Tab(ev.PaneID)
	if !ok {
		return
	}
	if ev.Allow && t.AllowBlocked != "" {
		a.openChat(phone.NS(m.ID, t.ID))
		return
	}
	a.do(m.ID, func(ctx context.Context, c *phone.Client) error { return c.Answer(ctx, t.Pane, ev.At, ev.Allow) }, func(err error) {
		if err != nil {
			if _, current, ok := a.fleet.Tab(ev.PaneID); !ok || current.At != ev.At {
				return
			}
			a.list.note = sentence(err.Error())
			a.list.notePane, a.list.noteAt = ev.PaneID, ev.At
			a.list.noteUntil = time.Now().Add(6 * time.Second)
		}
	})
}

// listHeader is the sidebar's brand bar at a phone's size: the mark, the
// session with how many wait in the others, the computer, and settings.
func (a *App) listHeader(gtx C) D {
	th := a.th
	return a.bar(gtx, nil, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return sidebar.Logo(gtx, th, gtx.Dp(18)) }),
			layout.Rigid(func(gtx C) D {
				s := a.st.Session(a.session)
				if s == nil {
					return D{}
				}
				return layout.Inset{Left: 6}.Layout(gtx, func(gtx C) D { return a.sessionChip(gtx, s.Name) })
			}),
		)
	}, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(a.hostTag),
			layout.Rigid(func(gtx C) D { return iconButton(gtx, th, &a.list.settings, icon("settings")) }),
		)
	})
}

// icon draws one of sidebar.Icon's glyphs.
func icon(name string) func(gtx C, col color.NRGBA, size int) {
	return func(gtx C, col color.NRGBA, size int) { sidebar.Icon(gtx, name, size, col) }
}

// sessionChip names the session shown, with how many of the other
// sessions' tabs wait on you; with more than one session it opens the
// session menu.
func (a *App) sessionChip(gtx C, name string) D {
	th := a.th
	others := 0
	for _, s := range a.st.Sessions {
		if s.ID != a.session {
			others += phone.NeedsYou(&a.st, s.ID)
		}
	}
	many := len(a.st.Sessions) > 1
	return a.list.sessions.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min = image.Point{}
		m := op.Record(gtx.Ops)
		d := layout.Inset{Left: 6, Right: 6, Top: 4, Bottom: 4}.Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Large), th.Fg, 1, name)
				}),
				layout.Rigid(func(gtx C) D {
					if !many {
						return D{}
					}
					return layout.Inset{Left: 4}.Layout(gtx, func(gtx C) D {
						return sidebar.Icon(gtx, "chevrons-up-down", gtx.Dp(12), th.Muted)
					})
				}),
				layout.Rigid(func(gtx C) D {
					if others == 0 {
						return D{}
					}
					return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D { return badge(gtx, th, others) })
				}),
			)
		})
		call := m.Stop()
		if a.list.sessions.Pressed() || a.list.menu == "sessions" {
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rectangle{Max: d.Size}, gtx.Dp(6)).Op(gtx.Ops))
		}
		call.Add(gtx.Ops)
		return d
	})
}

// badge is a count of tabs that wait on you: white on red, as a phone's
// app badges are.
func badge(gtx C, th *theme.Theme, n int) D {
	m := op.Record(gtx.Ops)
	d := label(gtx, th, weight(th.UIFont, 700), th.Sp(theme.Caption)*0.9, th.OnDanger, 1, strconv.Itoa(n))
	call := m.Stop()
	h := gtx.Dp(14)
	size := image.Pt(max(h, d.Size.X+gtx.Dp(8)), h)
	paint.FillShape(gtx.Ops, th.Red, clip.UniformRRect(image.Rectangle{Max: size}, h/2).Op(gtx.Ops))
	defer op.Offset(size.Sub(d.Size).Div(2)).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
	return D{Size: size}
}

// hostTag is the shown session's machine after a dot: green while its
// stream is up, red while it reconnects, gray before the first answer.
func (a *App) hostTag(gtx C) D {
	th := a.th
	l := a.shown()
	if l == nil || len(a.sessionLinks()) > 1 { // the tree's machine lines name them
		return D{}
	}
	name, col := l.m.Name, linkColor(th, l)
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			r := gtx.Dp(3)
			paint.FillShape(gtx.Ops, col, clip.Ellipse(image.Rect(0, 0, 2*r, 2*r)).Op(gtx.Ops))
			return D{Size: image.Pt(2*r, 2*r)}
		}),
		layout.Rigid(func(gtx C) D {
			return layout.Inset{Left: 5, Right: 4}.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(110))
				return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, 1, name)
			})
		}),
	)
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
		left = pad
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
				gtx.Constraints.Min.Y = 0
				return trail(gtx)
			}),
		)
	})
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, h-1), Max: image.Pt(gtx.Constraints.Max.X, h)}.Op())
	return D{Size: image.Pt(gtx.Constraints.Max.X, h)}
}

// linkColor is a machine's dot: green while its stream is up, red while
// it reconnects, gray before the first answer.
func linkColor(th *theme.Theme, l *link) color.NRGBA {
	switch {
	case l.online:
		return th.Green
	case l.offline != "":
		return th.Red
	}
	return th.Muted
}

// offlineLine says the shown session's machine is unreachable, when the
// app tries again, and offers to try now. Before the first answer it says
// Connecting. Another machine being down shows only on its own rows.
func (a *App) offlineLine(gtx C) D {
	l := a.shown()
	if l == nil || l.online {
		return D{}
	}
	th := a.th
	if l.offline == "" {
		return box(gtx, th.Sidebar, color.NRGBA{}, 0, layout.Inset{Left: pad, Right: pad, Top: 6, Bottom: 6}, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, 1, "Connecting…")
		})
	}
	msg := offlineText(l.retryAt, gtx.Now)
	if a.many() {
		msg = l.m.Name + " o" + msg[1:]
	}
	if left := l.retryAt.Sub(gtx.Now); left > 0 {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(min(left, time.Second))})
	}
	red := th.Readable(th.Red, th.Bg)
	bg := theme.Mix(th.Bg, th.Red, 0.12)
	d := box(gtx, bg, color.NRGBA{}, 0, layout.Inset{Left: pad, Right: 4}, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				return layout.Inset{Right: 6}.Layout(gtx, func(gtx C) D { return sidebar.Icon(gtx, "wifi-off", gtx.Dp(12), red) })
			}),
			layout.Flexed(1, func(gtx C) D {
				return label(gtx, th, th.UIFont, th.Sp(theme.Small), th.Fg, 1, msg)
			}),
			layout.Rigid(func(gtx C) D { return textButton(gtx, th, &a.list.retry, "Retry") }),
		)
	})
	paint.FillShape(gtx.Ops, theme.Mix(th.Bg, th.Red, 0.3), clip.Rect{Min: image.Pt(0, d.Size.Y-1), Max: d.Size}.Op())
	return d
}

// textButton is a borderless primary label, touch-tall.
func textButton(gtx C, th *theme.Theme, c *widget.Clickable, s string) D {
	return c.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min = image.Point{}
		m := op.Record(gtx.Ops)
		d := layout.Inset{Left: 10, Right: 10}.Layout(gtx, func(gtx C) D {
			return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Small), th.Primary, 1, s)
		})
		call := m.Stop()
		size := image.Pt(d.Size.X, max(d.Size.Y, gtx.Dp(touch)))
		if c.Pressed() {
			paint.FillShape(gtx.Ops, th.Pressed, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		}
		defer op.Offset(image.Pt(0, (size.Y-d.Size.Y)/2)).Push(gtx.Ops).Pop()
		call.Add(gtx.Ops)
		return D{Size: size}
	})
}

// waiting lists what waits on you in every session, most pressing first:
// errors, approvals and questions, each with its session and state. A
// tap opens the tab's chat. Two or more fold into one row, the most
// pressing tab and how many more, until a tap opens the list.
func (a *App) waiting(gtx C) D {
	th, l := a.th, &a.list
	type wait struct {
		c            *widget.Clickable
		col          color.NRGBA
		title, right string
		down         bool
	}
	var rows []wait
	for _, act := range sidebar.Blocked(&a.st) {
		w := a.st.Workspace(act.WorkspaceID)
		if w == nil {
			continue
		}
		where := ""
		if s := a.st.Session(w.SessionID); s != nil {
			where = s.Name
		}
		lk := a.linkOf(w.ID)
		if a.many() && lk != nil {
			where = lk.m.Name
		}
		c := a.list.btn("w:" + act.PaneID)
		if c.Clicked(gtx) {
			defer a.openChat(w.ID)
		}
		rows = append(rows, wait{c, sidebar.StateColor(th, act.State), sidebar.Title(*w), where + " · " + model.PillLabel(act), lk == nil || !lk.online})
	}
	if len(rows) == 0 && l.note == "" {
		return D{}
	}
	for l.waitFold.Clicked(gtx) {
		l.waitOpen = !l.waitOpen
	}
	fillMax(gtx, th.Sidebar) // ponytail: fills the rest too; the tree paints over it
	var ws []layout.Widget
	if l.note != "" {
		ws = append(ws, func(gtx C) D {
			return layout.Inset{Left: pad, Right: pad, Top: 6}.Layout(gtx, func(gtx C) D {
				return small(gtx, th, th.Readable(th.Red, th.Sidebar), l.note)
			})
		})
	}
	row := func(r wait) layout.Widget {
		return func(gtx C) D {
			if r.down {
				// why: a machine that is down keeps its last list, dimmed, as its rows in the tree are.
				defer paint.PushOpacity(gtx.Ops, 0.55).Pop()
			}
			return a.waitRow(gtx, r.c, r.col, r.title, r.right, 0)
		}
	}
	switch {
	case len(rows) == 1:
		ws = append(ws, row(rows[0]))
	case len(rows) > 1 && !l.waitOpen:
		first := rows[0]
		ws = append(ws, func(gtx C) D {
			return a.waitRow(gtx, &l.waitFold, first.col, first.title, fmt.Sprintf("+%d", len(rows)-1), 1)
		})
	case len(rows) > 1:
		ws = append(ws, func(gtx C) D {
			return a.waitRow(gtx, &l.waitFold, color.NRGBA{}, "Waiting on you", strconv.Itoa(len(rows)), -1)
		})
		for _, r := range rows {
			ws = append(ws, row(r))
		}
	}
	ws = append(ws, func(gtx C) D {
		return layout.Inset{Top: 6}.Layout(gtx, func(gtx C) D { return divider(gtx, th.Border) })
	})
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rigids(ws)...)
}

func rigids(ws []layout.Widget) []layout.FlexChild {
	out := make([]layout.FlexChild, len(ws))
	for i, w := range ws {
		out[i] = layout.Rigid(w)
	}
	return out
}

// waitRow is one of Waiting on you: a dot in the state's color, the tab,
// and its session and state at the right, then a chevron pointing down
// for chev 1, up for -1, none for 0.
func (a *App) waitRow(gtx C, c *widget.Clickable, col color.NRGBA, title, right string, chev int) D {
	th := a.th
	h := gtx.Dp(rowH)
	return c.Layout(gtx, func(gtx C) D {
		size := image.Pt(gtx.Constraints.Max.X, h)
		if c.Pressed() {
			paint.FillShape(gtx.Ops, th.Pressed, clip.Rect{Max: size}.Op())
		}
		gtx.Constraints = layout.Exact(size)
		layout.Inset{Left: pad, Right: pad}.Layout(gtx, func(gtx C) D {
			// why: W centers its child in the row's height only while its own Min.Y holds that height.
			return layout.W.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						r := gtx.Dp(3)
						paint.FillShape(gtx.Ops, col, clip.Ellipse(image.Rect(0, 0, 2*r, 2*r)).Op(gtx.Ops))
						return D{Size: image.Pt(2*r+gtx.Dp(8), 2*r)}
					}),
					layout.Flexed(1, func(gtx C) D {
						return label(gtx, th, weight(th.UIFont, 500), th.Sp(theme.Body), th.Fg, 1, title)
					}),
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
							gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, gtx.Dp(140))
							return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, 1, right)
						})
					}),
					layout.Rigid(func(gtx C) D {
						if chev == 0 {
							return D{}
						}
						n := gtx.Dp(14)
						defer op.Offset(image.Pt(gtx.Dp(6), 0)).Push(gtx.Ops).Pop()
						if chev < 0 {
							c := f32.Pt(float32(n)/2, float32(n)/2)
							defer op.Affine(f32.AffineId().Rotate(c, math.Pi)).Push(gtx.Ops).Pop()
						}
						sidebar.Icon(gtx, "chevron-down", n, th.Muted)
						return D{Size: image.Pt(n+gtx.Dp(6), n)}
					}),
				)
			})
		})
		return D{Size: size}
	})
}

// menuPanel draws a menu over a scrim, under the header at the left
// (start) or the right, with rows; a tap on the scrim closes it.
func (a *App) menuPanel(gtx C, start bool, rows []layout.Widget) {
	th, l := a.th, &a.list
	if l.scrim.Clicked(gtx) {
		l.menu = ""
		return
	}
	full := gtx.Constraints.Max
	l.scrim.Layout(gtx, func(gtx C) D {
		paint.FillShape(gtx.Ops, th.Scrim, clip.Rect{Max: full}.Op())
		return D{Size: full}
	})
	w := min(full.X-2*gtx.Dp(pad), gtx.Dp(240))
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, full.Y)}
	m := op.Record(gtx.Ops)
	d := layout.UniformInset(4).Layout(g, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rigids(rows)...)
	})
	call := m.Stop()
	anchor := image.Rect(gtx.Dp(pad), 0, full.X-gtx.Dp(pad), gtx.Dp(barH))
	side := kit.BelowEnd
	if start {
		side = kit.BelowStart
	}
	at := kit.Place(anchor, d.Size, image.Rectangle{Max: full}, side, gtx.Dp(2), gtx.Dp(8))
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: d.Size}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	defer clip.Rect(rect).Push(gtx.Ops).Pop()
	// why: a tap on the panel between rows must not reach the scrim under it.
	(&widget.Clickable{}).Layout(gtx, func(gtx C) D { return D{Size: d.Size} })
	call.Add(gtx.Ops)
}

// menuHeading is a menu's small muted title.
func menuHeading(th *theme.Theme, s string) layout.Widget {
	return func(gtx C) D {
		return layout.Inset{Left: 8, Right: 8, Top: 6, Bottom: 4}.Layout(gtx, func(gtx C) D {
			return label(gtx, th, weight(th.UIFont, 500), th.Sp(theme.Caption), th.Muted, 1, s)
		})
	}
}

// sessionMenu lists each machine's sessions under its name, each with
// its tabs and what waits there, and Add machine; a pick shows it.
func (a *App) sessionMenu(gtx C) {
	th, l := a.th, &a.list
	if l.add.Clicked(gtx) {
		a.adding, l.menu = true, ""
		return
	}
	var rows []layout.Widget
	for _, m := range a.fleet.Machines {
		ln := a.links[m.ID]
		head := "Sessions on " + m.Name
		if !ln.online && ln.offline != "" {
			head += ", offline"
		}
		rows = append(rows, menuHeading(th, head))
		for _, s := range a.st.Sessions {
			if !a.on(s.ID, m.ID) {
				continue
			}
			c := l.btn("s:" + m.ID + "/" + s.ID)
			if c.Clicked(gtx) {
				a.session, a.picked, l.menu = s.ID, true, ""
				return
			}
			rows = append(rows, a.sessionRow(c, s, m.ID))
		}
	}
	rows = append(rows, func(gtx C) D {
		return layout.Inset{Top: 4}.Layout(gtx, func(gtx C) D { return divider(gtx, th.Border) })
	}, func(gtx C) D {
		return menuRow(gtx, th, &l.add, false, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					return layout.Inset{Right: 8}.Layout(gtx, func(gtx C) D { return sidebar.Icon(gtx, "plus", gtx.Dp(14), th.Primary) })
				}),
				layout.Rigid(func(gtx C) D { return label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Primary, 1, "Add machine") }),
			)
		})
	})
	a.menuPanel(gtx, true, rows)
}

// sessionRow is a session in the menu under machine: its name over its
// tabs there and what waits there, with a check on the one shown and a
// count on the others. A session on several machines is under each.
func (a *App) sessionRow(c *widget.Clickable, s model.Session, machine string) layout.Widget {
	th := a.th
	tabs, waits := 0, 0
	for _, w := range a.st.Workspaces {
		if o, _ := a.fleet.Owner(w.ID); w.SessionID == s.ID && o.ID == machine {
			tabs++
		}
	}
	for _, act := range sidebar.Blocked(&a.st) {
		w := a.st.Workspace(act.WorkspaceID)
		if o, _ := a.fleet.Owner(act.WorkspaceID); w != nil && w.SessionID == s.ID && o.ID == machine {
			waits++
		}
	}
	sub := strconv.Itoa(tabs) + map[bool]string{true: " tab", false: " tabs"}[tabs == 1]
	if waits > 0 {
		sub += " · " + strconv.Itoa(waits) + " waiting"
	}
	cur, name := s.ID == a.session, s.Name
	return func(gtx C) D {
		return menuRow(gtx, th, c, cur, func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Flexed(1, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Fg, 1, name) }),
						layout.Rigid(func(gtx C) D { return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, 1, sub) }),
					)
				}),
				layout.Rigid(func(gtx C) D {
					switch {
					case cur:
						return sidebar.Icon(gtx, "check", gtx.Dp(14), th.Primary)
					case waits > 0:
						return badge(gtx, th, waits)
					}
					return D{}
				}),
			)
		})
	}
}

// menuRow is a menu's touch-tall row, filled while pressed or current.
func menuRow(gtx C, th *theme.Theme, c *widget.Clickable, current bool, w layout.Widget) D {
	return c.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		m := op.Record(gtx.Ops)
		d := layout.Inset{Left: 8, Right: 10, Top: 5, Bottom: 5}.Layout(gtx, w)
		call := m.Stop()
		size := image.Pt(gtx.Constraints.Max.X, max(d.Size.Y, gtx.Dp(rowH)))
		rr := clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusControl))
		switch {
		case c.Pressed():
			paint.FillShape(gtx.Ops, th.Pressed, rr.Op(gtx.Ops))
		case current:
			paint.FillShape(gtx.Ops, th.SelectedBg, rr.Op(gtx.Ops))
		}
		defer op.Offset(image.Pt(0, (size.Y-d.Size.Y)/2)).Push(gtx.Ops).Pop()
		call.Add(gtx.Ops)
		return D{Size: size}
	})
}

func (l *listView) expireNote(now time.Time, at int64, exists bool) {
	if !l.noteUntil.IsZero() && (!now.Before(l.noteUntil) || !exists || at != l.noteAt) {
		l.note, l.notePane, l.noteUntil = "", "", time.Time{}
	}
}
