package app

import (
	"image"
	"os"
	"strconv"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// hostPicker is the header's "New tab on" menu in a window of several
// machines: this one and each host, a down host disabled with its reason.
type hostPicker struct {
	open               bool
	sel                int
	at                 image.Point // the + button's bottom left, in window pixels
	items              []pickItem
	btns               []widget.Clickable
	outside            int
	requested, focused bool
}

type pickItem struct {
	host      string // "" for this machine
	name, sub string
	right     string // the latency
	off       bool   // down: not pickable
}

// pickItems are the picker's rows for hosts, this machine first.
func pickItems(hosts []sidebar.HostInfo, now time.Time) []pickItem {
	out := make([]pickItem, 0, len(hosts))
	for _, h := range hosts {
		it := pickItem{host: h.Name, name: h.Name}
		switch {
		case h.Name == "":
			name, _ := os.Hostname()
			it.name, it.sub = "This machine", "~ on "+name
		case !h.Up:
			it.off = true
			it.sub = "offline"
			if e := errLine(h.Note); e != "" {
				it.sub += ": " + e
			}
			if !h.Retry.IsZero() {
				it.sub += ", retry in " + retryIn(h.Retry, now)
			}
		default:
			it.sub = "~ on " + h.SSH
			if h.Latency > 0 {
				it.right = sidebar.Latency(h.Latency)
			}
		}
		out = append(out, it)
	}
	return out
}

// pickStart is the row the picker opens on: the current tab's machine,
// or this machine when that one is down.
func pickStart(items []pickItem, cur string) int {
	for i, it := range items {
		if it.host == cur && !it.off {
			return i
		}
	}
	return 0
}

// openPicker opens the picker under the header's +.
func (u *ui) openPicker(st *model.State) {
	p := &u.picker
	p.items = pickItems(u.hosts, time.Now())
	p.sel = pickStart(p.items, u.hostOf(u.nav.workspace))
	p.open, p.requested, p.focused = true, false, false
	if len(p.btns) < len(p.items) {
		p.btns = make([]widget.Clickable, len(p.items))
	}
}

// pick opens a tab on the picked row's machine: in the current tab's
// folder on that tab's machine, else in the home folder there.
func (u *ui) pick(st *model.State, i int) {
	p := &u.picker
	p.open = false
	if i < 0 || i >= len(p.items) || p.items[i].off {
		return
	}
	u.openOn(st, p.items[i].host)
}

// openOn opens a tab on host, "" for this machine: in the current tab's
// folder on that tab's machine, else in the home folder there.
func (u *ui) openOn(st *model.State, host string) {
	tab := u.nav.looseTab(st)
	if host != u.hostOf(u.nav.workspace) {
		tab.Cwd, tab.FromPane = "", ""
	}
	u.send(NewTabOn{Host: host, Tab: tab})
}

// layoutPicker handles the picker's keys and clicks and draws it.
func (u *ui) layoutPicker(gtx gl.Context, st *model.State) {
	p := &u.picker
	if !p.open {
		return
	}
	if !p.requested {
		gtx.Execute(key.FocusCmd{Tag: p})
		p.requested = true
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &p.outside, Kinds: pointer.Press}, key.FocusFilter{Target: p},
			key.Filter{Focus: p, Name: key.NameEscape}, key.Filter{Focus: p, Name: key.NameReturn}, key.Filter{Focus: p, Name: key.NameEnter},
			key.Filter{Focus: p, Name: key.NameUpArrow}, key.Filter{Focus: p, Name: key.NameDownArrow}, key.Filter{Focus: p, Name: ""})
		if !ok {
			break
		}
		switch e := ev.(type) {
		case pointer.Event:
			p.open = false
		case key.FocusEvent:
			if p.focused && !e.Focus {
				p.open = false
			}
			p.focused = e.Focus
		case key.Event:
			if e.State != key.Press {
				continue
			}
			switch e.Name {
			case key.NameEscape:
				p.open = false
			case key.NameReturn, key.NameEnter:
				u.pick(st, p.sel)
			case key.NameUpArrow, key.NameDownArrow:
				d := 1
				if e.Name == key.NameUpArrow {
					d = -1
				}
				for range p.items {
					p.sel = (p.sel + d + len(p.items)) % len(p.items)
					if !p.items[p.sel].off {
						break
					}
				}
			default:
				if n, err := strconv.Atoi(string(e.Name)); err == nil && n >= 1 && n <= len(p.items) {
					u.pick(st, n-1)
				}
			}
		}
	}
	for i := range p.items {
		if p.btns[i].Clicked(gtx) {
			u.pick(st, i)
		}
	}
	if !p.open {
		return
	}
	th := u.th
	c := clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops)
	event.Op(gtx.Ops, &p.outside)
	c.Pop()
	pad, rowH, w := gtx.Dp(4), gtx.Dp(46), gtx.Dp(340)
	head, headS := textCall(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption), th.Muted, "New tab on")
	sess := ""
	if s := st.Session(u.nav.session); s != nil {
		sess = "in " + s.Name
	}
	sc, ss := textCall(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, sess)
	cur := "this machine"
	if h := u.hostOf(u.nav.workspace); h != "" {
		cur = h
	}
	foot := "From a shell: pitwall new --host <name>. A group's + opens in that group's folder on its machine."
	if k := firstChord(u.nav.bind().Global["new_tab"]); k != "" {
		foot = k + " opens on the machine of the tab you are in, now " + cur + ", without this menu. " + foot
	}
	fg := gtx
	fg.Constraints = gl.Constraints{Max: image.Pt(w-2*pad-gtx.Dp(16), 400)}
	m := op.Record(gtx.Ops)
	fd := para(fg, th, th.UIFont, th.Sp(theme.Caption), th.Muted, foot)
	footCall := m.Stop()
	headH := headS.Y + gtx.Dp(12)
	size := image.Pt(w, 2*pad+headH+len(p.items)*rowH+gtx.Dp(9)+fd.Size.Y+gtx.Dp(12))
	at := kit.Place(image.Rectangle{Min: p.at, Max: p.at}, size, image.Rectangle{Max: gtx.Constraints.Max}, kit.BelowStart, 0, gtx.Dp(8))
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: size}
	kit.Surface(gtx, rect.Inset(-1), gtx.Dp(theme.RadiusPopover)+1, kit.Floating, th.BorderStrong, th.SurfaceElevated)
	c = clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, p)
	c.Pop()
	x0 := pad + gtx.Dp(8)
	o := op.Offset(image.Pt(x0, pad+gtx.Dp(8))).Push(gtx.Ops)
	head.Add(gtx.Ops)
	o.Pop()
	o = op.Offset(image.Pt(w-pad-gtx.Dp(8)-ss.X, pad+gtx.Dp(8))).Push(gtx.Ops)
	sc.Add(gtx.Ops)
	o.Pop()
	y := pad + headH
	row := image.Pt(w-2*pad, rowH)
	for i, it := range p.items {
		o := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
		g := gtx
		g.Constraints = gl.Exact(row)
		p.btns[i].Layout(g, func(gtx gl.Context) gl.Dimensions {
			rr := clip.UniformRRect(image.Rectangle{Max: row}, gtx.Dp(5))
			switch {
			case i == p.sel:
				paint.FillShape(gtx.Ops, th.SelectedBg, rr.Op(gtx.Ops))
			case p.btns[i].Hovered() && !it.off:
				paint.FillShape(gtx.Ops, th.SurfaceSecondary, rr.Op(gtx.Ops))
			}
			if !it.off {
				pointer.CursorPointer.Add(gtx.Ops)
			}
			nameC, icon := th.Fg, "monitor"
			if it.host != "" {
				nameC, icon = th.Readable(u.hostColor(it.host), th.SurfaceElevated), "server"
			}
			subC := th.Muted
			if it.off {
				nameC, subC = th.Muted, th.Readable(th.Red, th.SurfaceElevated)
			}
			is := gtx.Dp(14)
			top := gtx.Dp(7)
			drawIconAt(gtx, image.Pt(gtx.Dp(8), top+gtx.Dp(1)), func(gtx gl.Context) { sidebar.Icon(gtx, icon, is, nameC) })
			nc, ns := textCall(gtx, th, semibold(th.UIFont), th.Sp(theme.Body), nameC, it.name)
			oo := op.Offset(image.Pt(gtx.Dp(34), top)).Push(gtx.Ops)
			nc.Add(gtx.Ops)
			oo.Pop()
			sub, _ := textCall(gtx, th, th.MonoFont, th.Sp(theme.Caption), subC, it.sub)
			oo = op.Offset(image.Pt(gtx.Dp(34), top+ns.Y+gtx.Dp(2))).Push(gtx.Ops)
			sub.Add(gtx.Ops)
			oo.Pop()
			kc, ks := textCall(gtx, th, th.MonoFont, th.Sp(theme.Caption), th.Muted, strconv.Itoa(i+1))
			oo = op.Offset(image.Pt(row.X-gtx.Dp(10)-ks.X, top+gtx.Dp(1))).Push(gtx.Ops)
			kc.Add(gtx.Ops)
			oo.Pop()
			if it.right != "" {
				rc, rs := textCall(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, it.right)
				oo = op.Offset(image.Pt(row.X-gtx.Dp(22)-ks.X-rs.X, top+gtx.Dp(1))).Push(gtx.Ops)
				rc.Add(gtx.Ops)
				oo.Pop()
			}
			return gl.Dimensions{Size: row}
		})
		o.Pop()
		y += rowH
	}
	y += gtx.Dp(4)
	paint.FillShape(gtx.Ops, th.BorderStrong, clip.Rect{Min: image.Pt(pad+gtx.Dp(2), y), Max: image.Pt(w-pad-gtx.Dp(2), y+1)}.Op())
	y += gtx.Dp(5) + gtx.Dp(6)
	o = op.Offset(image.Pt(x0, y)).Push(gtx.Ops)
	footCall.Add(gtx.Ops)
	o.Pop()
}
