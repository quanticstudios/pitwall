package panel

import (
	"image"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestDenyReason: Deny or 3 opens the card's reason field and puts the
// keys in it; Enter denies with what it holds, trimmed, and the answered
// line says it; Esc denies without one; a plan's Refine and 2 do the same.
func TestDenyReason(t *testing.T) {
	th := theme.Dark()
	now := time.Now()
	approval := func(id string) session.Approval {
		return session.Approval{ID: id, RequestType: session.RequestCommand, Request: &session.ToolRequest{ToolName: "Bash", Command: "touch x"}}
	}
	s := &session.Snapshot{SessionID: "chat", SessionState: session.StateWaiting, PendingApprovals: []session.Approval{approval("r1")}}
	var tr Transcript
	var ops op.Ops
	var r input.Router
	frame := func() {
		ops.Reset()
		tr.Layout(layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 800)), Now: now}, th, s, false)
		r.Frame(&ops)
	}
	press := func(n key.Name) {
		r.Queue(key.Event{Name: n, State: key.Press}, key.Event{Name: n, State: key.Release})
		frame()
	}
	frame()
	tr.answer["r1"][2].Click()
	frame()
	frame()
	e := tr.reasons["r1"]
	if e == nil || len(tr.Answers()) != 0 {
		t.Fatal("Deny did not open the reason field, or denied at once")
	}
	if !r.Source().Focused(e) || !tr.Typing() {
		t.Fatal("the reason field does not have the keys")
	}
	e.SetText("  use echo instead ")
	press(key.NameReturn)
	if got := tr.Answers(); len(got) != 1 || got[0] != (Answer{Request: "r1", Decision: "deny", Reason: "use echo instead"}) {
		t.Fatalf("Enter answered %+v", got)
	}
	if tr.Typing() {
		t.Fatal("a sent reason still holds the keys")
	}
	if len(tr.done) != 1 || tr.done[0].line != "Denied" || tr.done[0].what != "use echo instead" || !tr.done[0].prose {
		t.Fatalf("answered line %+v", tr.done)
	}
	if resolvedIcon(&tr.done[0]) != "x" {
		t.Errorf("a denial's line is marked %q, want x", resolvedIcon(&tr.done[0]))
	}

	s.PendingApprovals = []session.Approval{approval("r2")}
	frame()
	if tr.reasons["r1"] != nil {
		t.Fatal("a resolved request keeps its reason field")
	}
	if !tr.Key("3") {
		t.Fatal("3 did not open the reason field")
	}
	frame()
	frame()
	tr.reasons["r2"].SetText("never mind")
	press(key.NameEscape)
	if got := tr.Answers(); len(got) != 1 || got[0] != (Answer{Request: "r2", Decision: "deny"}) {
		t.Fatalf("Esc answered %+v: want a denial without a reason", got)
	}
	if tr.done[1].what != "touch x" {
		t.Fatalf("a denial without a reason lost its command: %+v", tr.done[1])
	}

	plan := "# Plan\nadd hello.txt"
	s.PendingApprovals = []session.Approval{{ID: "p1", RequestType: session.RequestPlan, Detail: &plan}}
	s.PlanApproval = &session.PlanApproval{ID: "p1", Detail: &plan}
	frame()
	if !tr.Key("2") || tr.reasons["p1"] == nil || len(tr.Answers()) != 0 {
		t.Fatal("2 on a plan did not open its reason field")
	}
	frame()
	frame()
	tr.reasons["p1"].SetText("name it greeting.txt")
	press(key.NameReturn)
	if got := tr.Answers(); len(got) != 1 || got[0] != (Answer{Request: "p1", Decision: "deny", Reason: "name it greeting.txt"}) {
		t.Fatalf("refine answered %+v", got)
	}
	if l := tr.done[len(tr.done)-1]; l.line != "Sent back to refine" || l.what != "name it greeting.txt" {
		t.Fatalf("refine line %+v", l)
	}
	if l := tr.done[len(tr.done)-1]; resolvedIcon(&l) != "check" {
		t.Errorf("a plan sent back is marked %q, want check", resolvedIcon(&l))
	}
}
