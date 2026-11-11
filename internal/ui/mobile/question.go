package mobile

import (
	"context"
	"errors"

	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func (c *chatView) terminalQuestion(gtx C, th *theme.Theme, env chatEnv, done func(string) func(error)) []layout.Widget {
	q, k := env.Tab.Question, &c.card
	answer := func(option int, text string) {
		if k.busy || !env.Online {
			return
		}
		k.busy = true
		env.Do(func(ctx context.Context, api chatAPI) error {
			a, ok := api.(interface {
				Question(context.Context, string, string, int, string) error
			})
			if !ok {
				return errors.New("questions are unavailable")
			}
			return a.Question(ctx, env.Tab.Pane, q.ID, option, text)
		}, done(""))
	}
	w := []layout.Widget{cardHead(th, agentName(env.Tab.Agent)+" asks"), gap(8), func(gtx C) D { return label(gtx, th, th.UIFont, 16, th.Fg, 0, q.Prompt) }}
	for i, o := range q.Options {
		if k.questionBtns[i].Clicked(gtx) {
			answer(i, "")
		}
		name := o.Label
		if q.Multi {
			if o.Selected {
				name = "[x] " + name
			} else {
				name = "[ ] " + name
			}
		}
		w = append(w, gap(8), func(gtx C) D { return cardButton(gtx, th, &k.questionBtns[i], o.Selected, !k.busy && env.Online, name) })
	}
	if q.Other {
		w = append(w, gap(8), func(gtx C) D { return cardField(gtx, th, &k.other, "Other") })
		if k.otherSend.Clicked(gtx) {
			answer(-2, k.other.Text())
		}
		w = append(w, gap(8), func(gtx C) D { return cardButton(gtx, th, &k.otherSend, false, !k.busy && env.Online, "Send Other") })
	}
	if q.Submit {
		if k.submit.Clicked(gtx) {
			answer(-1, "")
		}
		w = append(w, gap(8), func(gtx C) D { return cardButton(gtx, th, &k.submit, true, !k.busy && env.Online, "Submit") })
	}
	return w
}
