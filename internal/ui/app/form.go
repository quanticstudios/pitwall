package app

import (
	"fmt"
	"image"
	"image/color"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/form"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Forms from `pitwall form` float over their tab. One that asks the user
// something sits in the middle of the tab over a dim, holds the keys and
// ends with a button, Enter in a one-line field, or Escape. A dashboard
// sits in the tab's top right corner and leaves the keys to the panes.

// formUI is the window's copy of one open form: the parsed spec and what
// the user typed and picked, which an update of the spec keeps for the
// fields whose ids stay.
type formUI struct {
	spec     string
	f        *form.Form // nil when the spec does not parse here
	bad      string     // why it does not
	edit     map[string]*widget.Editor
	picked   map[string]map[string]bool // choice id: option value: picked
	clicks   map[string]*widget.Clickable
	list     widget.List
	card     int // tag: presses on the card, and its keys while no field has them
	back     int // tag: presses on the dim around it
	shut     widget.Clickable
	openedAt time.Time
	sent     bool // answered here; hidden until the daemon drops it
}

func (fu *formUI) click(key string) *widget.Clickable {
	c := fu.clicks[key]
	if c == nil {
		c = new(widget.Clickable)
		fu.clicks[key] = c
	}
	return c
}

// sync takes spec, keeping the text and picks of fields whose ids stay.
func (fu *formUI) sync(spec string) {
	if fu.edit == nil {
		fu.edit, fu.picked, fu.clicks = map[string]*widget.Editor{}, map[string]map[string]bool{}, map[string]*widget.Clickable{}
		fu.list.Axis = gl.Vertical
	}
	if spec == fu.spec && (fu.f != nil || fu.bad != "") {
		return
	}
	fu.spec = spec
	f, err := form.Parse([]byte(spec))
	if err != nil {
		fu.f, fu.bad = nil, err.Error()
		return
	}
	fu.f, fu.bad = f, ""
	for _, it := range f.Items {
		switch it.Kind {
		case form.Input:
			e := fu.edit[it.ID]
			if e == nil {
				e = new(widget.Editor)
				e.SetText(it.Text)
				n := utf8.RuneCountInString(it.Text)
				e.SetCaret(n, n) // typing adds to a starting value
				fu.edit[it.ID] = e
			}
			e.SingleLine, e.Submit = it.Lines == 1, it.Lines == 1
		case form.Choice:
			if fu.picked[it.ID] == nil {
				m := map[string]bool{}
				for _, o := range it.Options {
					m[o.Value] = o.Selected
				}
				fu.picked[it.ID] = m
			}
		}
	}
}

// buttons are the form's buttons, or Cancel alone for a spec that does
// not parse here.
func (fu *formUI) buttons() []form.Item {
	if fu.f == nil {
		return []form.Item{{Kind: form.Button, ID: "cancel", Text: "Cancel", Cancel: true}}
	}
	return fu.f.Buttons()
}

// primary is the button Enter presses: the first that does not cancel.
func primary(bs []form.Item) *form.Item {
	for i := range bs {
		if !bs[i].Cancel {
			return &bs[i]
		}
	}
	return nil
}

// answer is the JSON the form sends for a press of button.
func (fu *formUI) answer(button string) string {
	text := map[string]string{}
	for id, e := range fu.edit {
		text[id] = e.Text()
	}
	return form.Answer(fu.f, button, text, fu.picked)
}

// shownForm is the form drawn over tab ws: the oldest that asks
// something, else the oldest dashboard; nil for none.
func (u *ui) shownForm(st *model.State, ws string) *model.Form {
	var dash *model.Form
	for i := range st.Forms {
		f := &st.Forms[i]
		if f.WorkspaceID != ws || u.forms[f.ID] != nil && u.forms[f.ID].sent {
			continue
		}
		if f.Waits {
			return f
		}
		if dash == nil {
			dash = f
		}
	}
	return dash
}

// formHoldsKeys reports whether a form that asks something is over the
// shown tab, so its panes give up the keys.
func (u *ui) formHoldsKeys(st *model.State) bool {
	f := u.shownForm(st, u.nav.workspace)
	return f != nil && f.Waits
}

// layoutForm draws the shown tab's form over its panes, in the panes'
// area, and sends the user's answer.
func (u *ui) layoutForm(gtx gl.Context, st *model.State) {
	for id := range u.forms {
		if !slices.ContainsFunc(st.Forms, func(f model.Form) bool { return f.ID == id }) {
			delete(u.forms, id)
		}
	}
	f := u.shownForm(st, u.nav.workspace)
	if f == nil {
		u.formFocus = ""
		return
	}
	if u.forms == nil {
		u.forms = map[string]*formUI{}
	}
	fu := u.forms[f.ID]
	if fu == nil {
		fu = &formUI{openedAt: gtx.Now}
		u.forms[f.ID] = fu
	}
	fu.sync(f.Spec)
	if f.Waits {
		u.layoutAsk(gtx, f, fu)
	} else {
		u.layoutDashboard(gtx, f, fu)
	}
}

// formEvents handles the presses and keys of form f and reports the
// button pressed, if any.
func (u *ui) formEvents(gtx gl.Context, f *model.Form, fu *formUI) (button *form.Item) {
	bs := fu.buttons()
	for i := range bs {
		for fu.click("b" + strconv.Itoa(i)).Clicked(gtx) {
			button = &bs[i]
		}
	}
	if fu.f != nil {
		for _, it := range fu.f.Items {
			if it.Kind != form.Choice {
				continue
			}
			for _, o := range it.Options {
				for fu.click("o\x00" + it.ID + "\x00" + o.Value).Clicked(gtx) {
					m := fu.picked[it.ID]
					if !it.Multi {
						clear(m)
					}
					m[o.Value] = !m[o.Value] || !it.Multi
				}
			}
		}
	}
	filters := []event.Filter{
		key.Filter{Focus: &fu.card, Name: key.NameEscape},
		key.Filter{Focus: &fu.card, Name: key.NameReturn},
		key.Filter{Focus: &fu.card, Name: key.NameEnter},
	}
	var order []*widget.Editor // the fields top to bottom, which Tab steps through
	if fu.f != nil {
		for _, it := range fu.f.Items {
			if it.Kind == form.Input {
				order = append(order, fu.edit[it.ID])
			}
		}
	}
	for _, e := range order {
		filters = append(filters, key.Filter{Focus: e, Name: key.NameTab, Optional: key.ModShift})
	}
	for _, e := range fu.edit {
		filters = append(filters, key.Filter{Focus: e, Name: key.NameEscape})
		for {
			ev, ok := e.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok && primary(bs) != nil {
				button = primary(bs)
			}
		}
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch {
		case e.Name == key.NameEscape:
			button = &form.Item{Cancel: true}
		case e.Name == key.NameTab:
			step := 1
			if e.Modifiers.Contain(key.ModShift) {
				step = -1
			}
			for i, ed := range order {
				if gtx.Focused(ed) {
					gtx.Execute(key.FocusCmd{Tag: order[(i+step+len(order))%len(order)]})
					break
				}
			}
		case primary(bs) != nil:
			button = primary(bs)
		}
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &fu.card, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok && f.Waits {
			gtx.Execute(key.FocusCmd{Tag: &fu.card}) // so Escape and Enter work after a click on an option
		}
	}
	for fu.shut.Clicked(gtx) {
		button = &form.Item{Cancel: true}
	}
	return button
}

// sendForm answers form f with a press of button.
func (u *ui) sendForm(f *model.Form, fu *formUI, button *form.Item) {
	a := proto.FormAnswer{ID: f.ID, Cancel: button.Cancel || fu.f == nil}
	if !a.Cancel {
		a.Answers = fu.answer(button.ID)
	}
	if u.send(a) {
		fu.sent = true
	}
}

// layoutAsk draws a form that asks something: a card in the middle of the
// tab over a dim that takes the presses meant for the panes.
func (u *ui) layoutAsk(gtx gl.Context, f *model.Form, fu *formUI) {
	if b := u.formEvents(gtx, f, fu); b != nil {
		u.sendForm(f, fu, b)
		gtx.Execute(op.InvalidateCmd{})
		return
	}
	th, size := u.th, gtx.Constraints.Max
	t := anim.At(gtx, fu.openedAt, anim.Dialog)
	paint.FillShape(gtx.Ops, scrim(th, t), clip.Rect{Max: size}.Op())
	for {
		if _, ok := gtx.Event(pointer.Filter{Target: &fu.back, Kinds: pointer.Press | pointer.Scroll}); !ok {
			break
		}
	}
	bg := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &fu.back)
	bg.Pop()
	u.focusForm(gtx, f, fu)

	pad, margin := gtx.Dp(24), gtx.Dp(16)
	w := min(gtx.Dp(448), size.X-2*margin)
	inner := w - 2*pad
	cg := gtx
	cg.Constraints = gl.Constraints{Max: image.Pt(inner, size.Y)}

	var titleH int
	var title op.CallOp
	if fu.f != nil && fu.f.Title != "" {
		m := op.Record(gtx.Ops)
		titleH = para(cg, th, semibold(th.UIFont), th.Sp(theme.Title), th.Fg, fu.f.Title).Size.Y + gtx.Dp(16)
		title = m.Stop()
	}
	m := op.Record(gtx.Ops)
	bh := u.formButtons(cg, fu).Size.Y
	buttons := m.Stop()
	gap := gtx.Dp(24)
	bodyMax := max(gtx.Dp(48), size.Y-2*margin-2*pad-titleH-bh-gap)
	bg2 := cg
	bg2.Constraints = gl.Constraints{Max: image.Pt(inner, bodyMax)}
	m = op.Record(gtx.Ops)
	bodyH := u.formBody(bg2, fu, false).Size.Y
	body := m.Stop()

	box := image.Pt(w, 2*pad+titleH+bodyH+gap+bh)
	at := image.Pt((size.X-box.X)/2, max(margin, (size.Y-box.Y)/2))
	defer kit.PopIn(gtx, t, image.Rectangle{Min: at, Max: at.Add(box)}, 10)()
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: box}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusCard), kit.Modal, th.BorderSubtle, th.Surface)
	area := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &fu.card)
	area.Pop()
	y := pad
	for _, part := range []struct {
		call op.CallOp
		h    int
	}{{title, titleH}, {body, bodyH + gap}, {buttons, bh}} {
		o := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
		part.call.Add(gtx.Ops)
		o.Pop()
		y += part.h
	}
}

// focusForm moves the keys into form f the first time it shows over the
// tab, unless a dialog, the palette or the switcher has them.
func (u *ui) focusForm(gtx gl.Context, f *model.Form, fu *formUI) {
	if u.formFocus == f.ID || u.modal.kind != modalNone || u.pal.open || u.sw.open || u.nav.switcherVisible() {
		return
	}
	u.formFocus = f.ID
	var tag event.Tag = &fu.card
	if fu.f != nil {
		for _, it := range fu.f.Items {
			if it.Kind == form.Input {
				tag = fu.edit[it.ID]
				break
			}
		}
	}
	gtx.Execute(key.FocusCmd{Tag: tag})
}

// formButtons draws the buttons right-aligned in the order written, the
// first that does not cancel as the primary one.
func (u *ui) formButtons(gtx gl.Context, fu *formUI) gl.Dimensions {
	bs := fu.buttons()
	p := primary(bs)
	var row []dialogButton
	for i := len(bs) - 1; i >= 0; i-- {
		k := kit.Secondary
		if p != nil && &bs[i] == p {
			k = kit.Primary
		}
		row = append(row, dialogButton{fu.click("b" + strconv.Itoa(i)), bs[i].Text, k})
	}
	return u.buttonRow(gtx, row...)
}

// formBody draws the form's items in a list that scrolls when they do not
// fit. A dashboard sets its items closer together.
func (u *ui) formBody(gtx gl.Context, fu *formUI, dash bool) gl.Dimensions {
	th := u.th
	if fu.f == nil {
		return para(gtx, th, th.UIFont, th.Sp(theme.Large), th.Red, "This form does not show in this window: "+fu.bad)
	}
	items := fu.f.Items
	var shown []form.Item
	for _, it := range items {
		if it.Kind != form.Button {
			shown = append(shown, it)
		}
	}
	gtx.Constraints.Min = image.Point{}
	return fu.list.List.Layout(gtx, len(shown), func(gtx gl.Context, i int) gl.Dimensions {
		d := u.formItem(gtx, fu, shown[i], dash)
		if i < len(shown)-1 {
			gap := gtx.Dp(16)
			if dash || shown[i].Kind == form.Status && shown[i+1].Kind == form.Status {
				gap = gtx.Dp(10)
			}
			d.Size.Y += gap
		}
		return d
	})
}

// formItem draws one item at the constraints' width.
func (u *ui) formItem(gtx gl.Context, fu *formUI, it form.Item, dash bool) gl.Dimensions {
	th := u.th
	w := gtx.Constraints.Max.X
	y := 0
	stack := func(d gl.Dimensions, gap int) {
		y += d.Size.Y + gap
	}
	at := func(f func(gtx gl.Context) gl.Dimensions) gl.Dimensions {
		defer op.Offset(image.Pt(0, y)).Push(gtx.Ops).Pop()
		g := gtx
		g.Constraints = gl.Constraints{Max: image.Pt(w, gtx.Constraints.Max.Y)}
		return f(g)
	}
	label := func() {
		if it.Label != "" {
			stack(at(func(gtx gl.Context) gl.Dimensions {
				return para(gtx, th, medium(th.UIFont), th.Sp(theme.Body), th.Fg, it.Label)
			}), gtx.Dp(6))
		}
	}
	switch it.Kind {
	case form.Text:
		size, col := th.Sp(theme.Large), th.Muted
		if dash {
			size, col = th.Sp(theme.Body), theme.Mix(th.Surface, th.Fg, 0.8)
		}
		for i, p := range strings.Split(it.Text, "\n") {
			gap := gtx.Dp(8)
			if i == 0 {
				gap = 0
			}
			y += gap
			stack(at(func(gtx gl.Context) gl.Dimensions { return para(gtx, th, th.UIFont, size, col, p) }), 0)
		}
	case form.Input:
		label()
		e := fu.edit[it.ID]
		h := gtx.Dp(36)
		if it.Lines > 1 {
			line := int(float32(gtx.Sp(th.Sp(theme.Body)))*1.45 + 0.5)
			h = it.Lines*line + gtx.Dp(16)
		}
		stack(at(func(gtx gl.Context) gl.Dimensions { return u.field(gtx, e, th.UIFont, h, it.Placeholder) }), 0)
	case form.Choice:
		label()
		for _, o := range it.Options {
			c := fu.click("o\x00" + it.ID + "\x00" + o.Value)
			on := fu.picked[it.ID][o.Value]
			stack(at(func(gtx gl.Context) gl.Dimensions { return u.formOption(gtx, c, on, !it.Multi, o.Label) }), 0)
		}
	case form.Progress:
		note := it.Text
		if note == "" {
			note = fmt.Sprintf("%.0f%%", 100*it.Value/it.Max)
		}
		call, ns := textCall(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, note)
		g := gtx
		g.Constraints.Max.X = max(0, w-ns.X-gtx.Dp(12))
		lc, ls := textCall(g, th, th.UIFont, th.Sp(theme.Body), th.Fg, it.Label)
		lh := max(ls.Y, ns.Y)
		lc.Add(gtx.Ops)
		o := op.Offset(image.Pt(w-ns.X, (lh-ns.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		y = lh + gtx.Dp(6)
		bar := gtx.Dp(6)
		track := image.Rect(0, y, w, y+bar)
		r := bar / 2
		paint.FillShape(gtx.Ops, theme.Mix(th.Surface, th.Fg, 0.1), clip.UniformRRect(track, r).Op(gtx.Ops))
		if fill := int(float64(w)*it.Value/it.Max + 0.5); fill > 0 {
			paint.FillShape(gtx.Ops, th.Primary, clip.UniformRRect(image.Rect(0, y, max(fill, bar), y+bar), r).Op(gtx.Ops))
		}
		y += bar
	case form.Status:
		h := gtx.Dp(20)
		d := gtx.Dp(8)
		paint.FillShape(gtx.Ops, statusColor(th, it.State), clip.Ellipse{Min: image.Pt(0, (h-d)/2), Max: image.Pt(d, (h+d)/2)}.Op(gtx.Ops))
		x := d + gtx.Dp(10)
		g := gtx
		g.Constraints.Max.X = max(0, w-x)
		if it.Label != "" {
			lc, ls := textCall(g, th, medium(th.UIFont), th.Sp(theme.Body), th.Fg, it.Label)
			o := op.Offset(image.Pt(x, (h-ls.Y)/2)).Push(gtx.Ops)
			lc.Add(gtx.Ops)
			o.Pop()
			x += ls.X + gtx.Dp(8)
		}
		if it.Text != "" {
			g.Constraints.Max.X = max(0, w-x)
			tc, ts := textCall(g, th, th.UIFont, th.Sp(theme.Body), th.Muted, it.Text)
			o := op.Offset(image.Pt(x, (h-ts.Y)/2)).Push(gtx.Ops)
			tc.Add(gtx.Ops)
			o.Pop()
		}
		y = h
	}
	return gl.Dimensions{Size: image.Pt(w, y)}
}

// statusColor is a status line's dot: the sidebar's colors for working,
// done, a question and a failure.
func statusColor(th *theme.Theme, state string) color.NRGBA {
	switch state {
	case form.StateRun:
		return th.Blue
	case form.StateOK:
		return th.Green
	case form.StateWarn:
		return th.Yellow
	case form.StateFail:
		return th.Red
	}
	return theme.Mix(th.Surface, th.Fg, 0.3)
}

// formOption is one option of a choice: a radio dot when round, else a
// checkbox, and its label, 28dp tall.
func (u *ui) formOption(gtx gl.Context, c *widget.Clickable, on, round bool, text string) gl.Dimensions {
	th := u.th
	return c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		h, box := gtx.Dp(28), gtx.Dp(16)
		top := (h - box) / 2
		rect := image.Rect(0, top, box, top+box)
		r := gtx.Dp(4)
		if round {
			r = box / 2
		}
		if gtx.Focused(c) {
			kit.FocusRing(gtx, th, rect, r)
		}
		switch {
		case on && round:
			paint.FillShape(gtx.Ops, th.Primary, clip.UniformRRect(rect, r).Op(gtx.Ops))
			dot := gtx.Dp(6)
			pt := rect.Min.Add(image.Pt((box-dot)/2, (box-dot)/2))
			paint.FillShape(gtx.Ops, th.OnPrimary, clip.Ellipse{Min: pt, Max: pt.Add(image.Pt(dot, dot))}.Op(gtx.Ops))
		case on:
			paint.FillShape(gtx.Ops, th.Primary, clip.UniformRRect(rect, r).Op(gtx.Ops))
			var p clip.Path
			p.Begin(gtx.Ops)
			s := float32(box) / 16
			o := f32.Pt(0, float32(top))
			p.MoveTo(o.Add(f32.Pt(4*s, 8.5*s)))
			p.LineTo(o.Add(f32.Pt(7*s, 11.5*s)))
			p.LineTo(o.Add(f32.Pt(12*s, 5*s)))
			paint.FillShape(gtx.Ops, th.OnPrimary, clip.Stroke{Path: p.End(), Width: 2 * s}.Op())
		default:
			ring := theme.Mix(th.Surface, th.Fg, 0.25)
			if c.Hovered() {
				ring = theme.Mix(th.Surface, th.Fg, 0.45)
			}
			paint.FillShape(gtx.Ops, ring, clip.UniformRRect(rect, r).Op(gtx.Ops))
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(rect.Inset(1), r-1).Op(gtx.Ops))
		}
		x := box + gtx.Dp(8)
		g := gtx
		g.Constraints.Max.X = max(0, gtx.Constraints.Max.X-x)
		call, ts := textCall(g, th, th.UIFont, th.Sp(theme.Large), th.Fg, text)
		o := op.Offset(image.Pt(x, (h-ts.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		size := image.Pt(min(gtx.Constraints.Max.X, x+ts.X), h)
		defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return gl.Dimensions{Size: size}
	})
}

// layoutDashboard draws a dashboard in the tab's top right corner, with
// a close button; the panes keep the keys.
func (u *ui) layoutDashboard(gtx gl.Context, f *model.Form, fu *formUI) {
	if b := u.formEvents(gtx, f, fu); b != nil {
		u.sendForm(f, fu, b)
		gtx.Execute(op.InvalidateCmd{})
		return
	}
	th, size := u.th, gtx.Constraints.Max
	margin, pad := gtx.Dp(12), gtx.Dp(16)
	w := min(gtx.Dp(320), size.X-2*margin)
	inner := w - 2*pad
	bs := gtx.Dp(24)

	title := "Dashboard"
	if fu.f != nil && fu.f.Title != "" {
		title = fu.f.Title
	}
	tg := gtx
	tg.Constraints = gl.Constraints{Max: image.Pt(max(0, inner-bs-gtx.Dp(8)), size.Y)}
	tc, ts := textCall(tg, th, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, title)
	head := max(ts.Y, bs)
	gap := gtx.Dp(12)
	cg := gtx
	cg.Constraints = gl.Constraints{Max: image.Pt(inner, max(gtx.Dp(32), size.Y-2*margin-2*pad-head-gap))}
	m := op.Record(gtx.Ops)
	bodyH := u.formBody(cg, fu, true).Size.Y
	body := m.Stop()

	box := image.Pt(w, 2*pad+head+gap+bodyH)
	at := image.Pt(size.X-margin-w, margin)
	defer kit.PopIn(gtx, anim.At(gtx, fu.openedAt, anim.Dialog), image.Rectangle{Min: at, Max: at.Add(box)}, 6)()
	defer op.Offset(at).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: box}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusCard), kit.Floating, th.BorderSubtle, th.Surface)
	area := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &fu.card)
	area.Pop()
	o := op.Offset(image.Pt(pad, pad+(head-ts.Y)/2)).Push(gtx.Ops)
	tc.Add(gtx.Ops)
	o.Pop()
	o = op.Offset(image.Pt(w-pad-bs+gtx.Dp(4), pad+(head-bs)/2)).Push(gtx.Ops)
	kit.IconButton(gtx, th, &fu.shut, kit.Ghost, kit.Small, func(gtx gl.Context, c color.NRGBA, s int) { sidebar.Icon(gtx, "x", s, c) })
	o.Pop()
	o = op.Offset(image.Pt(pad, pad+head+gap)).Push(gtx.Ops)
	body.Add(gtx.Ops)
	o.Pop()
}
