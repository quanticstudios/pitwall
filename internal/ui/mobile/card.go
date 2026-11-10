package mobile

import (
	"context"
	"fmt"
	"strings"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
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

func (a *App) askCard(gtx C, t remote.Tab) D {
	c, th := &a.chat.card, a.th
	if k := cardKey(t); k != c.key {
		*c = cardState{key: k}
		for _, r := range t.Requests {
			rs := &reqState{picked: make([]string, len(r.Questions)), texts: make([]widget.Editor, len(r.Questions)), opts: make([][]widget.Clickable, len(r.Questions))}
			for i, q := range r.Questions {
				rs.opts[i] = make([]widget.Clickable, len(q.Options))
			}
			c.reqs = append(c.reqs, rs)
		}
	}
	done := func(msg string) func(error) {
		return func(err error) {
			c.busy = false
			if err != nil {
				c.note, c.bad = err.Error(), true
				return
			}
			c.note, c.bad = msg, false
		}
	}
	answer := func(allow bool) {
		c.busy = true
		pane, at := t.Pane, t.At
		a.do(func(ctx context.Context, cl *phone.Client) error { return cl.Answer(ctx, pane, at, allow) },
			done(map[bool]string{true: "Allowed.", false: "Denied."}[allow]))
	}
	if c.allow.Clicked(gtx) && !c.busy {
		answer(true)
	}
	if c.deny.Clicked(gtx) && !c.busy {
		answer(false)
	}

	var w []layout.Widget
	if len(t.Requests) > 0 {
		for i, r := range t.Requests {
			if i < len(c.reqs) {
				w = append(w, a.request(gtx, t.Pane, r, c.reqs[i], done)...)
			}
		}
	} else {
		w = append(w, func(gtx C) D {
			l := t.Label
			if l == "" {
				l = "Waiting"
			}
			return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Small), th.Readable(th.Yellow, th.Surface), 1, l)
		})
		if t.Detail != "" {
			w = append(w, gap(4), func(gtx C) D { return body(gtx, th, th.Fg, t.Detail) })
		}
		switch {
		case t.Answer:
			w = append(w, gap(10), func(gtx C) D {
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return button(gtx, th, &c.allow, kit.Primary.When(!c.busy), "Allow") }),
					layout.Rigid(func(gtx C) D {
						return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D { return button(gtx, th, &c.deny, kit.Secondary.When(!c.busy), "Deny") })
					}),
				)
			})
		case c.note != "":
		case t.State == "awaiting-input" && t.Reply:
			c.note = "Type your answer below."
		case t.State == "pending-approval":
			c.note = "Answer it in its tab."
		case t.State == "plan-ready":
			c.note = "Answer the plan in its tab or from the sidebar."
		default:
			c.note = "Its screen shows a menu: answer it in its tab."
		}
	}
	if c.note != "" {
		col := th.Muted
		if c.bad {
			col = th.Readable(th.Red, th.Surface)
		}
		w = append(w, gap(8), func(gtx C) D { return small(gtx, th, col, sentence(c.note)) })
	}
	return layout.Inset{Left: pad, Right: pad, Top: 6, Bottom: 6}.Layout(gtx, func(gtx C) D {
		ring := theme.Mix(th.Surface, th.Yellow, 0.45)
		return box(gtx, th.Surface, ring, theme.RadiusCard, layout.UniformInset(12), func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			children := make([]layout.FlexChild, len(w))
			for i := range w {
				children[i] = layout.Rigid(w[i])
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
		})
	})
}

// request is a chat tab's approval, plan or question, as the page shows it.
func (a *App) request(gtx C, chat string, r remote.Request, rs *reqState, done func(string) func(error)) []layout.Widget {
	c, th := &a.chat.card, a.th
	kind := map[string]string{"plan": "Plan", "input": "Question"}[r.Kind]
	if kind == "" {
		kind = "Approval"
	}
	w := []layout.Widget{func(gtx C) D {
		return label(gtx, th, weight(th.UIFont, 600), th.Sp(theme.Small), th.Readable(th.Yellow, th.Surface), 1, kind)
	}}
	if cmd := strings.TrimSpace(strings.Join([]string{r.Tool, r.Command}, ": ")); r.Tool != "" || r.Command != "" {
		w = append(w, gap(6), func(gtx C) D {
			return box(gtx, th.Bg, th.BorderSubtle, theme.RadiusControl, layout.UniformInset(8), func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Constraints.Max.X
				return label(gtx, th, th.MonoFont, th.Sp(theme.Small), th.Fg, 12, strings.Trim(cmd, ": "))
			})
		})
	}
	if r.Path != "" {
		w = append(w, gap(4), func(gtx C) D { return small(gtx, th, th.Muted, r.Path) })
	}
	if r.Detail != "" {
		w = append(w, gap(6), func(gtx C) D { return body(gtx, th, th.Fg, r.Detail) })
	}
	approve := func(decision, msg string) {
		c.busy = true
		reason := ""
		if decision == "deny" {
			reason = strings.TrimSpace(rs.reason.Text())
		}
		id := r.ID
		a.do(func(ctx context.Context, cl *phone.Client) error { return cl.Approve(ctx, chat, id, decision, reason) }, done(msg))
	}
	if r.Kind != "input" {
		if rs.allow.Clicked(gtx) && !c.busy {
			approve("allow", "Allowed.")
		}
		if rs.session.Clicked(gtx) && !c.busy {
			approve("allow-session", "Allowed for this session.")
		}
		if rs.deny.Clicked(gtx) && !c.busy {
			approve("deny", "Denied.")
		}
		allow, deny := "Allow", "Deny"
		if r.Kind == "plan" {
			allow, deny = "Approve plan", "Keep planning"
		}
		btns := []layout.Widget{func(gtx C) D { return button(gtx, th, &rs.allow, kit.Primary.When(!c.busy), allow) }}
		if r.Kind != "plan" {
			btns = append(btns, func(gtx C) D { return button(gtx, th, &rs.session, kit.Secondary.When(!c.busy), "Allow for session") })
		}
		btns = append(btns, func(gtx C) D { return button(gtx, th, &rs.deny, kit.Secondary.When(!c.busy), deny) })
		w = append(w, gap(10), func(gtx C) D { return wrap(gtx, 8, btns) }, gap(8), func(gtx C) D {
			return field(gtx, th, &rs.reason, "Reason to deny (optional)", th.UIFont)
		})
		return w
	}
	for i, q := range r.Questions {
		i, q := i, q
		prompt := q.Prompt
		if prompt == "" {
			prompt = q.Header
		}
		w = append(w, gap(8), func(gtx C) D { return body(gtx, th, th.Fg, prompt) })
		if len(q.Options) > 0 {
			var btns []layout.Widget
			for j, o := range q.Options {
				if rs.opts[i][j].Clicked(gtx) {
					rs.picked[i] = o.Label
				}
				kind := kit.Secondary
				if rs.picked[i] == o.Label {
					kind = kit.Primary
				}
				btns = append(btns, func(gtx C) D { return button(gtx, th, &rs.opts[i][j], kind.When(!c.busy), o.Label) })
			}
			w = append(w, gap(8), func(gtx C) D { return wrap(gtx, 8, btns) })
		}
		hint := "Answer"
		if len(q.Options) > 0 {
			hint = "Or type an answer"
		}
		w = append(w, gap(8), func(gtx C) D { return field(gtx, th, &rs.texts[i], hint, th.UIFont) })
	}
	if rs.send.Clicked(gtx) && !c.busy {
		answers := map[string][]string{}
		ok := true
		for i, q := range r.Questions {
			v := strings.TrimSpace(rs.texts[i].Text())
			if v == "" {
				v = rs.picked[i]
			}
			if v == "" {
				ok = false
			}
			answers[q.ID] = []string{v}
		}
		if !ok {
			c.note, c.bad = "Answer every question.", true
		} else {
			c.busy = true
			id := r.ID
			a.do(func(ctx context.Context, cl *phone.Client) error { return cl.Input(ctx, chat, id, answers) }, done("Sent."))
		}
	}
	w = append(w, gap(10), func(gtx C) D {
		return layout.Flex{}.Layout(gtx, layout.Rigid(func(gtx C) D { return button(gtx, th, &rs.send, kit.Primary.When(!c.busy), "Send") }))
	})
	return w
}
