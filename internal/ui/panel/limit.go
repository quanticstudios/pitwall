package panel

import (
	"time"

	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// LimitResume is what the usage limit notice offers, from the window:
// continue once the limit resets, cancel that, and snooze the tab until
// then, as T3 Code's usage limit banner does.
type LimitResume struct {
	// At is when continue goes, the daemon's limit resume or a queued
	// continue; zero for none.
	At time.Time
	// CanContinue shows Continue at, or Continue now once the reset is
	// past: the daemon holds a queued continue until the reset.
	CanContinue bool
	// CanSnooze shows Snooze until then; Snoozed is until when the tab
	// sleeps, zero for awake.
	CanSnooze bool
	Snoozed   time.Time
	// Do runs a button's action: limitContinue at at (zero for now),
	// limitCancel, limitSnooze until at, or limitWake.
	Do func(action string, at time.Time)
}

// The notice's actions, as LimitResume.Do gets them.
const (
	LimitContinue = "continue"
	LimitCancel   = "cancel"
	LimitSnooze   = "snooze"
	LimitWake     = "wake"
)

// ResumeDelay is how long after a limit resets Continue at sends
// continue: when the daemon lets a queue held at the limit go.
const ResumeDelay = time.Minute

// limitAction is one of the notice's buttons.
type limitAction struct {
	act, label string
	at         time.Time
	primary    bool
}

// limitView is the limit notice: its title, its line, and its buttons
// for r at now. agent names who stopped.
func limitView(l *session.UsageLimit, r *LimitResume, agent string, now time.Time) (title, msg string, acts []limitAction) {
	title, msg = limitNotice(l, now)
	if agent != "Claude" && l.ResetsAt == nil {
		msg = agent + " stopped until the limit resets."
	}
	var resets time.Time
	if l.ResetsAt != nil {
		resets = parseTime(*l.ResetsAt)
	}
	if r == nil {
		return title, msg, nil
	}
	upcoming := !resets.IsZero() && resets.After(now)
	if !resets.IsZero() && !upcoming {
		msg = "Reset at " + limitClock(resets, now) + "."
	}
	switch {
	case !r.At.IsZero() && r.At.After(now.Add(-ResumeDelay)):
		msg = "Continues at " + limitClock(r.At, now) + "."
		acts = append(acts, limitAction{act: LimitCancel, label: "Cancel"})
	case !r.CanContinue:
	case upcoming:
		at := resets.Add(ResumeDelay)
		acts = append(acts, limitAction{act: LimitContinue, label: "Continue at " + limitClock(at, now), at: at, primary: true})
	case !resets.IsZero():
		acts = append(acts, limitAction{act: LimitContinue, label: "Continue now", primary: true})
	}
	switch {
	case !r.CanSnooze:
	case r.Snoozed.After(now):
		acts = append(acts, limitAction{act: LimitWake, label: "Wake now"})
	case upcoming:
		acts = append(acts, limitAction{act: LimitSnooze, label: "Snooze until then", at: resets.Add(ResumeDelay)})
	}
	return title, msg, acts
}

// limitClock is at as the notice says it: the time, with the weekday when it
// is more than 20 hours off.
func limitClock(at, now time.Time) string {
	format := "15:04"
	if d := at.Sub(now); d > 20*time.Hour || d < -20*time.Hour {
		format = "Mon 15:04"
	}
	return at.Local().Format(format)
}

// limitCard is the usage limit notice with its buttons under its line.
func (d *cdraw) limitCard(gtx layout.Context, l *session.UsageLimit) layout.Dimensions {
	title, msg, acts := limitView(l, d.t.Limit, d.agent(), d.t.now)
	if len(acts) == 0 {
		return d.notice(gtx, "limit", d.th.Yellow, title, msg)
	}
	for _, a := range acts {
		if d.t.click("limit:"+a.act).Clicked(gtx) && d.t.Limit.Do != nil {
			d.t.Limit.Do(a.act, a.at)
		}
	}
	var ws []layout.Widget
	for _, a := range acts {
		c := d.t.click("limit:" + a.act)
		ws = append(ws, func(gtx layout.Context) layout.Dimensions { return d.keyButton(gtx, c, a.primary, a.label, "", true) })
	}
	return d.notice(gtx, "limit", d.th.Yellow, title, msg, func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: theme.SpaceS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return wrapRow(gtx, gtx.Dp(theme.SpaceS), ws...)
		})
	})
}
