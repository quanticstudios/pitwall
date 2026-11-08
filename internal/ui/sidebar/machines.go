package sidebar

import (
	"image"
	"slices"
	"strconv"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Machine sections: in the tree of a shared session (a tab on another
// host), each machine's tabs and groups follow a line of their own, this
// machine first, then the hosts in [[hosts]] order. The line names the
// machine and its health; rows under it carry no host chip. With
// HostSections the line is a header that folds its tabs away, else a thin
// divider.

// NewTabOnHost opens a tab on Host, "" for this machine, without asking
// which machine: the + on a machine's line.
type NewTabOnHost struct{ Host string }

// machineIndent is how far a host's section (HostSections) sets its rows
// in, right of its rule in the host's color.
const machineIndent = 8

// machineState is a machine line's input: a click on it, its +, and its
// right-click tag.
type machineState struct {
	head, add widget.Clickable
	ctx       int
	chev      anim.Value
}

func (s *Sidebar) machine(host string) *machineState {
	if s.machines == nil {
		s.machines = map[string]*machineState{}
	}
	m := s.machines[host]
	if m == nil {
		m = &machineState{}
		s.machines[host] = m
	}
	return m
}

// sectionView splits v's top-level items by machine when v is the tree of
// a shared session: v.parts holds each machine's in order, and v.top
// becomes this machine's, then each host's in Hosts order, without those
// of folded sections. The active tab's section unfolds when the active
// tab moves into it.
func (s *Sidebar) sectionView(v *view) {
	if v.inbox || s.TreeOnly || s.HostOf == nil || len(s.Hosts) < 2 {
		return
	}
	of := map[string]string{}
	shared := false
	for _, id := range v.top {
		of[id] = s.hostOf(id)
		shared = shared || of[id] != ""
	}
	if !shared {
		return
	}
	v.sectioned, v.parts = true, map[string][]string{}
	for _, h := range s.Hosts {
		v.hosts = append(v.hosts, h.Name)
	}
	for _, id := range v.top {
		h := of[id]
		if !slices.Contains(v.hosts, h) {
			v.hosts = append(v.hosts, h)
		}
		v.parts[h] = append(v.parts[h], id)
	}
	if v.active != s.foldFollow {
		s.foldFollow = v.active
		if v.active != "" {
			delete(s.hostFold, foldKey(v.session, s.hostOf(v.active)))
		}
	}
	top := make([]string, 0, len(v.top))
	for _, h := range v.hosts {
		if !s.sectionFolded(v, h) {
			top = append(top, v.parts[h]...)
		}
	}
	v.top = top
}

func foldKey(session, host string) string { return session + "\x00" + host }

// sectionFolded reports whether host's section of v is folded away.
func (s *Sidebar) sectionFolded(v *view, host string) bool {
	return s.HostSections && s.hostFold[foldKey(v.session, host)]
}

// partOf is host's top-level items in v, folded or not, in order.
func (v *view) partOf(host string) []string { return v.parts[host] }

// sectionCounts is how many tabs host's section of v holds and how many of
// them wait on the user, workers included.
func (v *view) sectionCounts(host string) (tabs, need int) {
	var count func(id string)
	count = func(id string) {
		tabs++
		if a := v.activity[id]; a != nil && (model.Tier(a) == model.TierAttention || model.NeedsYou(a.State) && a.Unseen) {
			need++
		}
		if v.separate {
			for _, k := range v.kids[id] {
				count(k.ID)
			}
		}
	}
	for _, id := range v.partOf(host) {
		if !v.groups[id] {
			count(id)
			continue
		}
		for _, ws := range v.byProject[id] {
			count(ws.ID)
		}
	}
	return tabs, need
}

// machineGap and machineHeight are the space above a machine's line, less
// for the first, and the line's own height.
func (s *Sidebar) machineGap(gtx layout.Context, first bool) int {
	switch {
	case first && s.HostSections:
		return gtx.Dp(2)
	case first:
		return gtx.Dp(4)
	case s.HostSections:
		return gtx.Dp(8)
	}
	return gtx.Dp(10)
}

func (s *Sidebar) machineHeight(gtx layout.Context) int {
	if s.HostSections {
		return gtx.Dp(30)
	}
	return gtx.Dp(22)
}

// machineNote is what a machine's line says of its health: "This
// machine" for this one, a host's latency, or while it is down when the
// next try is, and whether that is bad news.
func machineNote(hi HostInfo, now time.Time) (string, bool) {
	return healthNote(hi, now), hi.Name != "" && !hi.Up
}

// machineHeader draws host's line in gtx's exact size and takes its
// input: a click folds a section (HostSections) or, on a host's divider,
// opens the color row, as a right-click does on either; the + opens a tab
// there.
func (s *Sidebar) machineHeader(gtx layout.Context, v *view, host string) {
	th := v.th
	ms := s.machine(host)
	hi, _ := FindHost(s.Hosts, host)
	down := host != "" && !hi.Up
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	// why: the line takes its input while the tree draws, after Layout
	// asked for the frame that shows input; a fold must not wait for the
	// next pointer move.
	acted := false
	defer func() {
		if acted {
			gtx.Execute(op.InvalidateCmd{})
		}
	}()
	for ms.head.Clicked(gtx) {
		acted = true
		if s.HostSections {
			s.toggleSection(v, host)
		} else if host != "" {
			s.toggleHostColor(host)
		}
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &ms.ctx, Kinds: pointer.Press})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok && pe.Buttons == pointer.ButtonSecondary && host != "" {
			acted = true
			s.toggleHostColor(host)
		}
	}
	for ms.add.Clicked(gtx) {
		acted = true
		if !down {
			s.events = append(s.events, NewTabOnHost{Host: host})
		}
	}
	if down && !hi.Retry.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second)})
	}
	hovered := ms.head.Hovered() || ms.add.Hovered()

	area := clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops)
	event.Op(gtx.Ops, &ms.ctx)
	defer area.Pop()

	btn := gtx.Dp(20)
	plus := hovered && !down // a down host takes no new tab
	hg := gtx
	hg.Constraints = layout.Exact(image.Pt(w-btn-gtx.Dp(4), h))
	clickable(hg, &ms.head, func(gtx layout.Context) layout.Dimensions {
		if s.HostSections {
			s.sectionLine(gtx, v, host, hi, down, hovered)
		}
		return layout.Dimensions{Size: gtx.Constraints.Max}
	})
	if !s.HostSections {
		end := w - gtx.Dp(4)
		if plus {
			end = w - btn - gtx.Dp(6)
		}
		s.dividerLine(gtx, th, host, hi, down, end)
	}
	o := op.Offset(image.Pt(w-btn-gtx.Dp(2), (h-btn)/2)).Push(gtx.Ops)
	headerButton(gtx, th, &ms.add, icPlus, btn, gtx.Dp(13), plus)
	o.Pop()
	if hovered && host != "" && s.hostColor != host {
		tip := "ssh " + hi.SSH
		if down && hi.Note != "" {
			tip = firstLine(hi.Note)
		}
		s.tipAt(gtx, th, image.Pt(gtx.Dp(4), h+gtx.Dp(2)), w, tip)
	}
	if s.hostColor == host && host != "" {
		s.colorRow(gtx, th, image.Pt(0, h+gtx.Dp(2)), w)
	}
}

// toggleSection folds or unfolds host's section of v.
func (s *Sidebar) toggleSection(v *view, host string) {
	if s.hostFold == nil {
		s.hostFold = map[string]bool{}
	}
	k := foldKey(v.session, host)
	if s.hostFold[k] {
		delete(s.hostFold, k)
	} else {
		s.hostFold[k] = true
	}
}

func (s *Sidebar) toggleHostColor(host string) {
	if s.hostColor == host {
		s.hostColor = ""
	} else {
		s.hostColor = host
	}
}

// sectionLine is a section's header: chevron, a dot in the machine's
// color, its name, its latency and tab count (or, folded, what waits on
// the user), then room for the +.
func (s *Sidebar) sectionLine(gtx layout.Context, v *view, host string, hi HostInfo, down, hovered bool) {
	th := v.th
	ms := s.machine(host)
	h := gtx.Constraints.Max.Y
	if hovered {
		w := gtx.Constraints.Max.X + gtx.Dp(24)
		paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(image.Rect(0, 0, w, h), gtx.Dp(6)).Op(gtx.Ops))
	}
	name, nameC, dot := "This machine", th.Fg, th.Green
	if host != "" {
		name, nameC, dot = host, th.Readable(HostColor(th, s.Hosts, host), th.Sidebar), HostColor(th, s.Hosts, host)
	}
	if down {
		nameC, dot = th.Muted, th.Red
	}
	note, bad := machineNote(hi, gtx.Now)
	noteC := th.Muted
	if bad {
		noteC = th.Readable(th.Red, th.Sidebar)
	}
	folded := s.sectionFolded(v, host)
	tabs, need := v.sectionCounts(host)
	count, countC := strconv.Itoa(tabs)+" "+plural(tabs, "tab", "tabs"), th.Muted
	if folded && need > 0 {
		count, countC = strconv.Itoa(need)+" "+plural(need, "needs you", "need you"), th.Readable(th.Yellow, th.Sidebar)
	}
	in := gtx.Dp(8)
	if host != "" {
		in = gtx.Dp(4) // its rule already sets it in
	}
	g := gtx
	g.Constraints = layout.Exact(image.Pt(gtx.Constraints.Max.X-in, h))
	defer op.Offset(image.Pt(in, 0)).Push(gtx.Ops).Pop()
	items := []item{
		{w: func(gtx layout.Context) layout.Dimensions {
			return chevron(gtx, &ms.chev, !folded, gtx.Dp(12), th.Muted)
		}},
		{w: func(gtx layout.Context) layout.Dimensions {
			d := gtx.Dp(7)
			paint.FillShape(gtx.Ops, dot, clip.Ellipse{Max: image.Pt(d, d)}.Op(gtx.Ops))
			return layout.Dimensions{Size: image.Pt(d, d)}
		}},
		{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Small), nameC, name)
		}},
	}
	if note != "" {
		items = append(items, item{right: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Caption), noteC, note)
		}}, item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Muted, "·")
		}})
	}
	items = append(items, item{right: note == "", w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, th.UIFont, th.Sp(theme.Caption), countC, count)
	}})
	hrow(g, h, gtx.Dp(7), items...)
}

// dividerLine is a thin labelled rule: the machine's name, its health,
// then a line to end.
func (s *Sidebar) dividerLine(gtx layout.Context, th *theme.Theme, host string, hi HostInfo, down bool, end int) {
	h := gtx.Constraints.Max.Y
	name, nameC := "This machine", th.Muted
	if host != "" {
		name, nameC = host, th.Readable(HostColor(th, s.Hosts, host), th.Sidebar)
	}
	if down {
		nameC = th.Muted
	}
	note, bad := machineNote(hi, gtx.Now)
	noteC := th.Muted
	if bad {
		noteC = th.Readable(th.Red, th.Sidebar)
	}
	in := gtx.Dp(4)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(max(0, end-in), h)}
	o := op.Offset(image.Pt(in, 0)).Push(gtx.Ops)
	items := []item{{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Caption), nameC, name)
	}}}
	if note != "" {
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Caption), noteC, note)
		}})
	}
	d := hrowFit(g, h, gtx.Dp(8), items...)
	o.Pop()
	x0, x1 := in+d.Size.X+gtx.Dp(8), end
	if x1 > x0 {
		y := h / 2
		paint.FillShape(gtx.Ops, th.BorderStrong, clip.Rect{Min: image.Pt(x0, y), Max: image.Pt(x1, y+1)}.Op())
	}
}

// sectionRule is the thin line in host's color down the left of its
// section (HostSections), from top to bot.
func sectionRule(gtx layout.Context, th *theme.Theme, hosts []HostInfo, host string, top, bot int) {
	c := HostColor(th, hosts, host)
	if HostDown(hosts, host) {
		c = th.Red
	}
	c = theme.Mix(th.Sidebar, c, 0.6)
	paint.FillShape(gtx.Ops, c, clip.Rect{Min: image.Pt(gtx.Dp(2), top), Max: image.Pt(gtx.Dp(4), bot)}.Op())
}

// tipAt draws txt in a small card at, w wide at most, over everything.
func (s *Sidebar) tipAt(gtx layout.Context, th *theme.Theme, at image.Point, w int, txt string) {
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(w, gtx.Dp(200))}
	d := label(g, th, th.MonoFont, th.Sp(theme.Caption), th.Fg, txt)
	c := m.Stop()
	box := image.Rectangle{Min: at, Max: at.Add(d.Size).Add(image.Pt(2*gtx.Dp(8), 2*gtx.Dp(5)))}
	m = op.Record(gtx.Ops)
	paint.FillShape(gtx.Ops, th.SurfaceElevated, clip.UniformRRect(box, gtx.Dp(6)).Op(gtx.Ops))
	o := op.Offset(box.Min.Add(image.Pt(gtx.Dp(8), gtx.Dp(5)))).Push(gtx.Ops)
	c.Add(gtx.Ops)
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
}
