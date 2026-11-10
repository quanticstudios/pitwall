package mobile

import (
	"context"
	"fmt"
	"image"
	"strings"

	"gioui.org/font"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// cardState is the card of what the open tab asks, kept while the tab
// asks the same thing, so typing survives a new list.
type cardState struct {
	key         string
	allow, deny widget.Clickable
	reqs        []*reqState
	busy        bool
	note        string
	bad         bool
}

// reqState is one of a chat tab's requests: an approval, a plan or a
// question, answered by its id.
type reqState struct {
	allow, session, deny, send widget.Clickable
	reason                     widget.Editor
	opts                       [][]widget.Clickable // per question
	picked                     []string
	texts                      []widget.Editor
}

func cardKey(t remote.Tab) string {
	ids := make([]string, len(t.Requests))
	for i, r := range t.Requests {
		ids[i] = r.ID
	}
	return fmt.Sprintf("%s|%d|%s|%s", t.Pane, t.At, t.State, strings.Join(ids, ","))
}

// askCard is the card above the composer while the tab asks something:
// the command in view with Deny and Allow, a chat tab's approvals, plans
// and questions, or where to answer what the phone cannot.
func (c *chatView) askCard(gtx C, th *theme.Theme, env chatEnv) D {
	k, t := &c.card, env.Tab
	if key := cardKey(t); key != k.key {
		*k = cardState{key: key}
		for _, r := range t.Requests {
			rs := &reqState{picked: make([]string, len(r.Questions)), texts: make([]widget.Editor, len(r.Questions)), opts: make([][]widget.Clickable, len(r.Questions))}
			for i, q := range r.Questions {
				rs.opts[i] = make([]widget.Clickable, len(q.Options))
			}
			k.reqs = append(k.reqs, rs)
		}
	}
	done := func(msg string) func(error) {
		return func(err error) {
			k.busy = false
			if err != nil {
				k.note, k.bad = err.Error(), true
				return
			}
			k.note, k.bad = msg, false
		}
	}
	answer := func(allow bool) {
		k.busy = true
		pane, at := t.Pane, t.At
		env.Do(func(ctx context.Context, api chatAPI) error { return api.Answer(ctx, pane, at, allow) },
			done(map[bool]string{true: "Allowed.", false: "Denied."}[allow]))
	}
	if k.allow.Clicked(gtx) && !k.busy {
		answer(true)
	}
	if k.deny.Clicked(gtx) && !k.busy {
		answer(false)
	}

	agent := agentName(t.Agent)
	if agent == "" {
		agent = "The agent"
	}
	var w []layout.Widget
	if len(t.Requests) > 0 {
		for i, r := range t.Requests {
			if i < len(k.reqs) {
				w = append(w, c.request(gtx, th, env, r, k.reqs[i], agent, done)...)
			}
		}
	} else {
		head := agent + " asks"
		switch t.State {
		case "pending-approval":
			head = agent + " wants to run"
		case "plan-ready":
			head = agent + " has a plan"
		case "error":
			head = agent + " stopped"
		}
		w = append(w, cardHead(th, head))
		switch {
		case t.Detail == "":
		case t.State == "pending-approval": // what it wants to run
			w = append(w, gap(10), func(gtx C) D { return pre(gtx, th, t.Detail, 8) })
		default: // a question, a plan or an error, in words
			w = append(w, gap(8), func(gtx C) D { return label(gtx, th, th.UIFont, 16, th.Fg, 8, t.Detail) })
		}
		switch {
		case t.Answer:
			w = append(w, gap(12), func(gtx C) D {
				return twoButtons(gtx, th, &k.deny, "Deny", &k.allow, "Allow", !k.busy)
			})
			if k.note == "" {
				k.note = "Always, and plan answers, are on the desktop sidebar."
			}
		case k.note != "":
		case t.State == "awaiting-input" && t.Reply:
			k.note = "Type your answer below."
		case t.State == "plan-ready":
			k.note = "Answer the plan in its tab or from the sidebar."
		default:
			k.note = "Its screen shows a menu: answer it in its tab."
		}
	}
	if k.note != "" {
		col := th.Muted
		if k.bad {
			col = th.Readable(th.Red, th.Surface)
		}
		w = append(w, gap(10), func(gtx C) D { return label(gtx, th, th.UIFont, 13.5, col, 0, sentence(k.note)) })
	}
	return layout.Inset{Left: 12, Right: 12, Bottom: 8, Top: 4}.Layout(gtx, func(gtx C) D {
		ring := theme.Mix(th.Surface, th.Yellow, 0.4)
		return box(gtx, th.Surface, ring, 14, layout.UniformInset(14), func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			children := make([]layout.FlexChild, len(w))
			for i := range w {
				children[i] = layout.Rigid(w[i])
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	})
}

// cardHead is the card's title with the alert icon in yellow.
func cardHead(th *theme.Theme, s string) layout.Widget {
	return func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D { return glyph(gtx, icAlert, 20, th.Yellow) }),
			layout.Rigid(layout.Spacer{Width: 9}.Layout),
			layout.Flexed(1, func(gtx C) D { return label(gtx, th, weight(th.UIFont, font.SemiBold), 16, th.Fg, 2, s) }),
		)
	}
}

// pre is text the agent wants to run, in mono on the background.
func pre(gtx C, th *theme.Theme, s string, lines int) D {
	return box(gtx, th.Bg, th.BorderSubtle, 9, layout.Inset{Left: 12, Right: 12, Top: 10, Bottom: 10}, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return widget.Label{MaxLines: lines, Truncator: "…", LineHeight: 19.5, LineHeightScale: 1}.Layout(gtx, th.Shaper, th.MonoFont, 13.5, detab(s), colorOp(gtx, th.Fg))
	})
}

// twoButtons is a row of two equal 48dp buttons, the right one primary.
func twoButtons(gtx C, th *theme.Theme, a *widget.Clickable, al string, b *widget.Clickable, bl string, live bool) D {
	return layout.Flex{}.Layout(gtx,
		layout.Flexed(1, func(gtx C) D { return cardButton(gtx, th, a, false, live, al) }),
		layout.Rigid(layout.Spacer{Width: 10}.Layout),
		layout.Flexed(1, func(gtx C) D { return cardButton(gtx, th, b, true, live, bl) }),
	)
}

// cardButton is a 48dp button as wide as it is given: primary blue, or a
// neutral one on the second surface.
func cardButton(gtx C, th *theme.Theme, c *widget.Clickable, primary, live bool, s string) D {
	draw := func(gtx C) D {
		size := image.Pt(gtx.Constraints.Min.X, gtx.Dp(48))
		fill, ring, fg := th.SurfaceSecondary, th.BorderSubtle, th.Fg
		if primary {
			fill, ring, fg = th.Primary, th.Primary, th.OnPrimary
		}
		if c.Pressed() {
			fill = theme.Mix(fill, th.Fg, 0.16)
		}
		r := gtx.Dp(12)
		paint.FillShape(gtx.Ops, ring, clip.UniformRRect(image.Rectangle{Max: size}, r).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: size}.Inset(1), r-1).Op(gtx.Ops))
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints.Min = image.Point{}
		d := label(g, th, weight(th.UIFont, font.SemiBold), 16, fg, 1, s)
		call := m.Stop()
		o := op.Offset(image.Pt((size.X-d.Size.X)/2, (size.Y-d.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		return D{Size: size}
	}
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	if !live {
		defer paint.PushOpacity(gtx.Ops, 0.5).Pop()
		return draw(gtx)
	}
	return c.Layout(gtx, draw)
}

// request is a chat tab's approval, plan or question, as the page shows it.
func (c *chatView) request(gtx C, th *theme.Theme, env chatEnv, r remote.Request, rs *reqState, agent string, done func(string) func(error)) []layout.Widget {
	k := &c.card
	chat := env.Tab.Pane
	head := map[string]string{"plan": agent + " has a plan", "input": agent + " asks"}[r.Kind]
	if head == "" {
		head = agent + " wants to run"
	}
	w := []layout.Widget{cardHead(th, head)}
	if cmd := strings.Trim(strings.Join([]string{r.Tool, r.Command}, ": "), ": "); cmd != "" {
		w = append(w, gap(10), func(gtx C) D { return pre(gtx, th, cmd, 8) })
	}
	if r.Path != "" {
		w = append(w, gap(6), func(gtx C) D { return label(gtx, th, th.MonoFont, 13.5, th.Muted, 2, r.Path) })
	}
	if r.Detail != "" {
		w = append(w, gap(8), func(gtx C) D { return label(gtx, th, th.UIFont, 15, th.Fg, 10, r.Detail) })
	}
	approve := func(decision, msg string) {
		k.busy = true
		reason := ""
		if decision == "deny" {
			reason = strings.TrimSpace(rs.reason.Text())
		}
		id := r.ID
		env.Do(func(ctx context.Context, api chatAPI) error { return api.Approve(ctx, chat, id, decision, reason) }, done(msg))
	}
	if r.Kind != "input" {
		if rs.allow.Clicked(gtx) && !k.busy {
			approve("allow", "Allowed.")
		}
		if rs.session.Clicked(gtx) && !k.busy {
			approve("allow-session", "Allowed for this session.")
		}
		if rs.deny.Clicked(gtx) && !k.busy {
			approve("deny", "Denied.")
		}
		allow, deny := "Allow", "Deny"
		if r.Kind == "plan" {
			allow, deny = "Approve", "Keep planning"
		}
		w = append(w, gap(12), func(gtx C) D { return cardField(gtx, th, &rs.reason, "Reason to deny (optional)") },
			gap(10), func(gtx C) D { return twoButtons(gtx, th, &rs.deny, deny, &rs.allow, allow, !k.busy) })
		if r.Kind != "plan" {
			w = append(w, gap(10), func(gtx C) D { return cardButton(gtx, th, &rs.session, false, !k.busy, "Allow for this session") })
		}
		return w
	}
	for i, q := range r.Questions {
		prompt := q.Prompt
		if prompt == "" {
			prompt = q.Header
		}
		w = append(w, gap(10), func(gtx C) D { return label(gtx, th, th.UIFont, 15, th.Fg, 0, prompt) })
		if len(q.Options) > 0 {
			var btns []layout.Widget
			for j, o := range q.Options {
				if rs.opts[i][j].Clicked(gtx) {
					rs.picked[i] = o.Label
				}
				on := rs.picked[i] == o.Label
				btns = append(btns, func(gtx C) D { return optionChip(gtx, th, &rs.opts[i][j], on, o.Label) })
			}
			w = append(w, gap(8), func(gtx C) D { return wrap(gtx, 8, btns) })
		}
		hint := "Answer"
		if len(q.Options) > 0 {
			hint = "Or type an answer"
		}
		w = append(w, gap(8), func(gtx C) D { return cardField(gtx, th, &rs.texts[i], hint) })
	}
	if rs.send.Clicked(gtx) && !k.busy {
		answers := map[string][]string{}
		ok := true
		for i, q := range r.Questions {
			v := strings.TrimSpace(rs.texts[i].Text())
			if v == "" {
				v = rs.picked[i]
			}
			ok = ok && v != ""
			answers[q.ID] = []string{v}
		}
		if !ok {
			k.note, k.bad = "Answer every question.", true
		} else {
			k.busy = true
			id := r.ID
			env.Do(func(ctx context.Context, api chatAPI) error { return api.Input(ctx, chat, id, answers) }, done("Sent."))
		}
	}
	w = append(w, gap(12), func(gtx C) D { return cardButton(gtx, th, &rs.send, true, !k.busy, "Send") })
	return w
}

// optionChip is one of a question's options, 48dp tall, filled once
// picked.
func optionChip(gtx C, th *theme.Theme, c *widget.Clickable, on bool, s string) D {
	return c.Layout(gtx, func(gtx C) D {
		m := op.Record(gtx.Ops)
		d := layout.Inset{Left: 16, Right: 16}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.Y = gtx.Dp(48)
			return layout.Center.Layout(gtx, func(gtx C) D {
				fg := th.Fg
				if on {
					fg = th.OnPrimary
				}
				return label(gtx, th, weight(th.UIFont, font.SemiBold), 15, fg, 1, s)
			})
		})
		call := m.Stop()
		fill, ring := th.SurfaceSecondary, th.BorderSubtle
		if on {
			fill, ring = th.Primary, th.Primary
		}
		if c.Pressed() {
			fill = theme.Mix(fill, th.Fg, 0.16)
		}
		r := gtx.Dp(12)
		paint.FillShape(gtx.Ops, ring, clip.UniformRRect(image.Rectangle{Max: d.Size}, r).Op(gtx.Ops))
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: d.Size}.Inset(1), r-1).Op(gtx.Ops))
		call.Add(gtx.Ops)
		return d
	})
}

// wrap lays ws out in rows, gap apart, starting a row when one is full.
func wrap(gtx C, gap unit.Dp, ws []layout.Widget) D {
	g := gtx.Dp(gap)
	x, y, rowH, width := 0, 0, 0, 0
	for _, w := range ws {
		m := op.Record(gtx.Ops)
		cg := gtx
		cg.Constraints.Min = image.Point{}
		d := w(cg)
		call := m.Stop()
		if x > 0 && x+d.Size.X > gtx.Constraints.Max.X {
			x, y, rowH = 0, y+rowH+g, 0
		}
		o := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		x += d.Size.X + g
		rowH = max(rowH, d.Size.Y)
		width = max(width, x-g)
	}
	return D{Size: image.Pt(width, y+rowH)}
}

// cardField is a text field in the card, 48dp at one line, focused by a
// tap anywhere in it.
func cardField(gtx C, th *theme.Theme, ed *widget.Editor, hint string) D {
	ring := th.BorderStrong
	if gtx.Focused(ed) {
		ring = theme.Mix(th.Bg, th.Primary, 0.7)
	}
	fs := fields[ed]
	if fs == nil {
		fs = new(widget.Clickable)
		fields[ed] = fs
	}
	if fs.Clicked(gtx) {
		gtx.Execute(key.FocusCmd{Tag: ed})
		gtx.Execute(key.SoftKeyboardCmd{Show: true})
	}
	return fs.Layout(gtx, func(gtx C) D {
		return box(gtx, th.Bg, ring, 12, layout.Inset{Left: 14, Right: 14, Top: 13, Bottom: 13}, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return layout.Stack{}.Layout(gtx,
				layout.Stacked(func(gtx C) D {
					if ed.Len() > 0 {
						return D{}
					}
					return label(gtx, th, th.UIFont, 16, th.TextQuiet, 1, hint)
				}),
				layout.Stacked(func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Constraints.Max.X
					return ed.Layout(gtx, th.Shaper, th.UIFont, 16, colorOp(gtx, th.Fg), colorOp(gtx, theme.Mix(th.Bg, th.Primary, 0.35)))
				}),
			)
		})
	})
}
