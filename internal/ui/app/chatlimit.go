package app

import (
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
)

// limitText is what Continue at queues and what the daemon's limit resume
// types: a queued message that says only this stands for the resume.
const limitText = "continue"

// paneLimit gives a pane chat's snapshot s the usage limit its agent
// stopped on, read from the pane's activity as the daemon reads it, when
// the transcript names none: Claude Code's and Codex's TUIs write it on
// screen, not in their transcripts.
func paneLimit(st *model.State, id string, s *session.Snapshot, now time.Time) {
	if s.UsageLimit != nil {
		return
	}
	for _, a := range st.Activities {
		if a.PaneID != id || a.State != model.StateError {
			continue
		}
		if _, resets, ok := agent.UsageLimit(a.Detail, now); ok {
			l := &session.UsageLimit{Provider: a.Provider, LimitID: "usage", Label: "Usage limit"}
			if !resets.IsZero() {
				at := resets.UTC().Format(time.RFC3339)
				l.ResetsAt = &at
			}
			s.UsageLimit = l
		}
		return
	}
}

// queuedContinue is the message in s's queue that continues after the
// limit, nil for none.
func queuedContinue(s *session.Snapshot) *session.QueuedInput {
	for i := range s.QueuedInputs {
		if q := &s.QueuedInputs[i]; strings.EqualFold(strings.TrimSpace(q.Text), limitText) && q.Error == "" {
			return q
		}
	}
	return nil
}

// limitResume is the limit notice's state for chat id in tab ws, whose
// snapshot is s, nil when it shows no limit: when continue goes, the
// daemon's resume or a queued continue, and the tab's snooze. Its
// buttons queue continue for when the limit resets, cancel either, and
// snooze or wake the tab.
func (u *ui) limitResume(st *model.State, id string, ws *model.Workspace, s *session.Snapshot) *panel.LimitResume {
	if s.UsageLimit == nil {
		return nil
	}
	level := u.link().Level
	r := &panel.LimitResume{CanContinue: level >= proto.LimitQueueLevel, CanSnooze: ws != nil && level >= proto.InboxLevel}
	if ws != nil {
		r.Snoozed = ws.SnoozedUntil
	}
	q := queuedContinue(s)
	for _, a := range st.Activities {
		if a.PaneID == id && !a.Resumes.IsZero() {
			r.At = a.Resumes
		}
	}
	if q != nil && s.UsageLimit.ResetsAt != nil {
		if t, err := time.Parse(time.RFC3339, *s.UsageLimit.ResetsAt); err == nil {
			r.At = t.Add(panel.ResumeDelay)
		}
	}
	r.Do = func(action string, at time.Time) {
		if m := limitMessage(action, id, ws, q, at); m != nil {
			u.send(m)
		}
	}
	return r
}

// limitMessage is what the notice's action sends for chat id in tab ws,
// whose queued continue is q (nil for none), at at.
func limitMessage(action, id string, ws *model.Workspace, q *session.QueuedInput, at time.Time) any {
	switch action {
	case panel.LimitContinue:
		m := proto.ChatSend{Chat: id, Text: limitText}
		if !at.IsZero() {
			m.How = "queue" // held until the limit resets, then sent
		}
		return m
	case panel.LimitCancel:
		if q != nil {
			return proto.ChatQueue{Chat: id, Action: "remove", ID: q.ID}
		}
		return proto.CancelResume{Pane: id}
	case panel.LimitSnooze, panel.LimitWake:
		if action == panel.LimitWake {
			at = time.Time{}
		}
		return proto.SetTabInbox{WorkspaceID: ws.ID, Settled: !ws.Settled.IsZero(), SnoozedUntil: at}
	}
	return nil
}
