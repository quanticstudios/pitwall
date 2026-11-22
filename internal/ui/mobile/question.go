package mobile

import (
	"context"
	"errors"
	"fmt"

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
	w := []layout.Widget{cardHead(th, agentName(env.Tab.Agent)+" asks")}
	if len(q.Tabs) > 0 {
		var chips []layout.Widget
		for i, tab := range q.Tabs {
			lbl := tab.Label
			if tab.Answered {
				lbl = "✓ " + lbl
			}
			cur := i == q.Index
			chips = append(chips, func(gtx C) D { return stepPillStatic(gtx, th, lbl, cur, tab.Answered) })
		}
		w = append(w, gap(8), func(gtx C) D { return wrap(gtx, 6, chips) })
	} else if q.Total > 1 {
		stepText := fmt.Sprintf("Question %d of %d", q.Index+1, q.Total)
		w = append(w, gap(4), func(gtx C) D { return label(gtx, th, th.UIFont, 13.5, th.Muted, 1, stepText) })
	}
	w = append(w, gap(8), func(gtx C) D { return label(gtx, th, th.UIFont, 16, th.Fg, 0, q.Prompt) })
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
		w = append(w, gap(8), func(gtx C) D { return cardField(gtx, th, &k.other, &k.otherBox, "Other") })
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
	if q.CanBack || q.CanNext {
		if k.qBack.Clicked(gtx) && q.CanBack {
			answer(-3, "")
		}
		if k.qNext.Clicked(gtx) && q.CanNext {
			answer(-4, "")
		}
		switch {
		case q.CanBack && q.CanNext:
			w = append(w, gap(8), func(gtx C) D { return twoButtons(gtx, th, &k.qBack, "Back", &k.qNext, "Next", !k.busy && env.Online) })
		case q.CanBack:
			w = append(w, gap(8), func(gtx C) D { return cardButton(gtx, th, &k.qBack, false, !k.busy && env.Online, "Back") })
		case q.CanNext:
			w = append(w, gap(8), func(gtx C) D { return cardButton(gtx, th, &k.qNext, false, !k.busy && env.Online, "Next") })
		}
	}
	return w
}
