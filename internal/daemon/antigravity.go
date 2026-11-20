package daemon

import (
	"context"
	"fmt"
	"regexp"
	"slices"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/progstatus"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
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

// agyMode is the mode agy's footer shows on g, "" when g shows no footer.
func agyMode(g vt.Grid) string {
	mode, _, _, _ := agent.AgyFooter(g)
	return mode
}

// agyValue is a model id or an effort a pane chat may type after /model
// or /effort: one word, nothing agy's input box would read as more.
var agyValue = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// agySet sets an agy pane chat's model and effort with agy's own /model
// and /effort commands, and its mode with Shift+Tab until the footer
// reads it, as a user would in the TUI.
func (d *Daemon) agySet(id string, p Pane, m proto.ChatSet) error {
	if m.Provider != "" && m.Provider != model.ProviderAntigravity {
		return chatError{"unsupported", "this chat is Antigravity in a pane; it cannot switch provider"}
	}
	for _, c := range []struct{ name, value string }{{"model", m.Model}, {"effort", m.Effort}} {
		if c.value == "" {
			continue
		}
		if !agyValue.MatchString(c.value) {
			return invalid("not an Antigravity %s: %q", c.name, c.value)
		}
		if err := d.agyCommand(id, "/"+c.name+" "+c.value); err != nil {
			return err
		}
	}
	if m.Mode != "" {
		return d.paneMode(id, p, m.Mode, agyMode)
	}
	return nil
}

// agyCommand types command into agy's input box in pane, Enter with it,
// once agy waits there. agy's slash menu opens as the command is typed;
// an Enter in the same write takes the line as typed, while one sent
// after the menu drew would pick the menu's row.
func (d *Daemon) agyCommand(pane, command string) error {
	defer d.paneCommandLock(pane)()
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	p, busy, err := d.ready(pane)
	if err != nil {
		if busy {
			return fmt.Errorf("%w; try again when it waits at its input box", err)
		}
		return err
	}
	d.noteUserKey(pane, nil)
	return d.handle(context.Background(), proto.Input{Pane: pane, Data: append(input.Text(command), press(key.NameReturn, p.Modes())...)})
}
