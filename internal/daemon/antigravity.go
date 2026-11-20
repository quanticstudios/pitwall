package daemon

import (
	"context"
	"slices"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/progstatus"
)

// agyApp is the app name Antigravity CLI gives its OSC 7501 reports.
const agyApp = "antigravity"

// agyProgram makes an Antigravity CLI report pane id's activity where its
// hooks cannot: agy has no hook for a permission prompt or a question, nor
// for the answer to one. Its own OSC 7501 report says each:
//
//	blocked, kind permission    pending-approval, Detail = its message
//	blocked, kind question      awaiting-input, Detail = the question
//
// and, only while one of those waits, what came after it: working once
// answered, done or error with its message, idle (nothing) once the turn
// was cancelled. Every other state stays with the hooks, or with the
// screen for an agy without them. Sign-in (kind auth) flashes at each start
// and is left out. Callers hold d.mu.
func (d *Daemon) agyProgram(id string, r progstatus.Report) {
	if r.App != agyApp || r.ID != "" {
		return
	}
	pi := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if pi < 0 || d.st.Panes[pi].Provider != model.ProviderAntigravity {
		return
	}
	var prev model.AgentState
	if i := d.activityIndex(id); i >= 0 {
		prev = d.st.Activities[i].State
	}
	var state model.AgentState
	msg := agent.Summary(r.Msg)
	switch {
	case r.State == progstatus.Blocked && r.Kind == progstatus.Permission:
		state = model.StatePendingApproval
	case r.State == progstatus.Blocked && r.Kind == progstatus.Question:
		state = model.StateAwaitingInput
	case !waits(prev):
		return
	case r.State == progstatus.Working:
		state, msg = model.StateWorking, ""
	case r.State == progstatus.Done:
		state = model.StateCompleted
	case r.State == progstatus.Error:
		state = model.StateError
	case r.State == progstatus.Idle:
		d.dropActivity(id)
		d.recordExplanation(id, model.ProviderAntigravity, "", "program-status", agent.ScreenTrace{})
		return
	default:
		return
	}
	d.setActivity(context.Background(), id, model.ProviderAntigravity, state, msg)
	d.recordExplanation(id, model.ProviderAntigravity, state, "program-status", agent.ScreenTrace{})
}
