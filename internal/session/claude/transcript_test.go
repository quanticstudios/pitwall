package claude

import (
	"bytes"
	"os"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// apply reduces events into snap as the pane chat does.
func apply(t *testing.T, snap session.Snapshot, es []session.Event) session.Snapshot {
	t.Helper()
	for _, e := range es {
		if e.CreatedAt == "" {
			e.CreatedAt = "2026-11-18T10:00:00.000Z"
		}
		for _, d := range session.Reduce(snap, e) {
			d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = session.DeltaVersion, snap.SessionID, snap.Revision+1, snap.Revision+1, e.CreatedAt
			n, err := session.Apply(&snap, snap.Revision, d)
			if err != nil {
				t.Fatalf("%s: %v", d.Type, err)
			}
			snap = n
		}
	}
	return snap
}

func TestTranscriptSession(t *testing.T) {
	b, err := os.ReadFile("../../flow/testdata/claude/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTranscript()
	snap := base()
	for _, l := range bytes.Split(b, []byte("\n")) {
		snap = apply(t, snap, tr.Line(l))
	}
	var prompts []string
	tools := map[string]string{}
	for _, it := range snap.Content {
		switch {
		case it.Kind == session.KindUserPrompt:
			prompts = append(prompts, it.Text)
		case it.Kind == session.KindToolCall:
			tools[it.Tool.ToolName+":"+it.ID] = it.Tool.Status
		}
	}
	if len(prompts) != 2 || prompts[0] != "Fix the login bug\nIt fails on empty passwords." || prompts[1] != "Now add a regression test" {
		t.Fatalf("prompts %q: want the two the user typed, no command echo or notification", prompts)
	}
	if tools["Bash:tu1"] != "failed" || tools["Read:tu2"] != "completed" || tools["Bash:x1"] != "" {
		t.Errorf("tools %v: want Bash failed, Read completed, no sidechain call", tools)
	}
	if len(snap.Turns) != 2 || snap.Turns[0].State != session.TurnCompleted || snap.Turns[1].State != session.TurnRunning {
		t.Fatalf("turns %+v: want the first completed at its turn_duration, the second running", snap.Turns)
	}
	if !tr.Running() || snap.SessionState != session.StateRunning {
		t.Errorf("running %v, state %s: want the last turn running", tr.Running(), snap.SessionState)
	}
	if snap.Model == nil || *snap.Model != "claude-opus-5-5" {
		t.Errorf("model %v: want the assistant lines' model", snap.Model)
	}
	if snap.Content[0].CreatedAt != "2026-10-05T10:00:00.000Z" {
		t.Errorf("first item at %s: want its line's timestamp", snap.Content[0].CreatedAt)
	}

	// Claude Code asks about its running Grep call: a permission card for
	// that call, withdrawn when the turn ends.
	snap = apply(t, snap, tr.Ask("42", "Grep"))
	if len(snap.PendingApprovals) != 1 || snap.PendingApprovals[0].Request == nil || snap.PendingApprovals[0].Request.ToolName != "Grep" {
		t.Fatalf("approvals %+v: want one for Grep", snap.PendingApprovals)
	}
	if snap = apply(t, snap, tr.Ask("42", "Grep")); len(snap.PendingApprovals) != 1 {
		t.Fatalf("asking again opened %d approvals", len(snap.PendingApprovals))
	}
	snap = apply(t, snap, tr.End(session.TurnCompleted))
	if len(snap.PendingApprovals) != 0 || snap.SessionState != session.StateReady || tr.Running() {
		t.Errorf("after End: %d approvals, state %s: want none, ready", len(snap.PendingApprovals), snap.SessionState)
	}
}

func TestTranscriptInterruptAndQuestion(t *testing.T) {
	tr := NewTranscript()
	snap := base()
	lines := []string{
		`{"type":"user","uuid":"p1","timestamp":"2026-11-18T10:00:00Z","message":{"role":"user","content":"pick one"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-18T10:00:01Z","message":{"id":"m1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"q1","name":"AskUserQuestion","input":{"questions":[{"question":"Which?","header":"Pick","options":[{"label":"A","description":"first"},{"label":"B","description":"second"}]}]}}]}}`,
	}
	for _, l := range lines {
		snap = apply(t, snap, tr.Line([]byte(l)))
	}
	snap = apply(t, snap, tr.Ask("7", ""))
	if len(snap.PendingUserInputs) != 1 || len(snap.PendingUserInputs[0].Questions) != 1 || len(snap.PendingUserInputs[0].Questions[0].Options) != 2 {
		t.Fatalf("inputs %+v: want the question with its two options", snap.PendingUserInputs)
	}
	snap = apply(t, snap, tr.Line([]byte(`{"type":"user","uuid":"i1","timestamp":"2026-11-18T10:00:02Z","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`)))
	if len(snap.PendingUserInputs) != 0 || snap.Turns[0].State != session.TurnInterrupted {
		t.Errorf("after the interrupt: %d inputs, turn %s: want none, interrupted", len(snap.PendingUserInputs), snap.Turns[0].State)
	}
}
