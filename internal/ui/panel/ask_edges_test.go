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

// askFrames lays out a transcript of s frame by frame, as the window does.
type askFrames struct {
	tr  Transcript
	s   *session.Snapshot
	ops op.Ops
	r   input.Router
}

func (f *askFrames) frame() {
	f.ops.Reset()
	f.tr.Layout(layout.Context{Ops: &f.ops, Source: f.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 800)), Now: time.Now()}, theme.Dark(), f.s, false)
	f.r.Frame(&f.ops)
}

// cards is how many cards and how many answered lines the last frame drew.
func (f *askFrames) cards() (cards, lines int) { return drawnCards(&f.tr) }

func drawnCards(tr *Transcript) (cards, lines int) {
	for _, r := range tr.rows {
		switch {
		case r.tail != nil:
			cards++
		case r.res != nil:
			lines++
		}
	}
	return cards, lines
}

func oneQuestion(id string) session.InputRequest {
	return session.InputRequest{ID: id, Questions: []session.Question{{ID: "q", Prompt: "Which?", Options: []session.PromptOption{{Label: "Red"}, {Label: "Blue"}}}}}
}

// TestAskEdges: the frame that takes a click draws the answered line in
// the card's place; a double click and a second Enter send nothing more;
// a card that comes right after takes the keys; a card the agent withdrew
// while its answer was on the way goes without a trace; and with an
// approval waiting too, the number keys answer the approval.
func TestAskEdges(t *testing.T) {
	f := &askFrames{s: &session.Snapshot{SessionID: "c", SessionState: session.StateWaiting, PendingUserInputs: []session.InputRequest{oneQuestion("i1")}}}
	f.frame()
	if c, l := f.cards(); c != 1 || l != 0 {
		t.Fatalf("before: %d cards, %d lines", c, l)
	}
	a := f.tr.asks["i1"]
	a.opts[0][1].Click()
	a.opts[0][1].Click() // a double click: the second comes a frame later
	f.frame()
	if c, l := f.cards(); c != 0 || l != 1 || f.tr.done[0].what != "Blue" {
		t.Fatalf("the click's frame: %d cards, %d lines %+v", c, l, f.tr.done)
	}
	if got := f.tr.Inputs(); len(got) != 1 || got[0].Answers["q"][0] != "Blue" {
		t.Fatalf("inputs %+v", got)
	}
	if f.tr.Key(string(key.NameReturn)) || f.tr.Key("1") || f.tr.Answer("Green") {
		t.Fatal("a sent card still takes keys or text")
	}
	f.frame()
	f.frame()
	if len(f.tr.Inputs()) != 0 {
		t.Fatal("a second send")
	}

	// The daemon has not resolved i1 yet when i2 comes.
	f.s.PendingUserInputs = append(f.s.PendingUserInputs, oneQuestion("i2"))
	f.frame()
	if c, _ := f.cards(); c != 1 || f.tr.keyAsk != "i2" {
		t.Fatalf("second card: %d cards, keys on %q", c, f.tr.keyAsk)
	}
	if !f.tr.Key("1") {
		t.Fatal("1 did not pick on the second card")
	}
	if got := f.tr.Inputs(); len(got) != 1 || got[0].Request != "i2" || got[0].Answers["q"][0] != "Red" {
		t.Fatalf("inputs %+v", got)
	}

	// The turn is interrupted: both requests go, and the daemon refuses
	// i2's answer as resolved.
	f.s.PendingUserInputs, f.s.SessionState = nil, session.StateReady
	f.tr.Refused("i2")
	f.frame()
	if c, l := f.cards(); c != 0 || l != 1 || len(f.tr.asks) != 0 || len(f.tr.sent) != 0 || f.tr.keyAsk != "" {
		t.Fatalf("withdrawn: %d cards, %d lines, asks %d, sent %v", c, l, len(f.tr.asks), f.tr.sent)
	}
	if f.tr.Key("1") || f.tr.Answer("x") {
		t.Fatal("keys or text went to a withdrawn card")
	}

	// A question and an approval at once: 1 allows, the card waits.
	f.s.SessionState = session.StateWaiting
	f.s.PendingUserInputs = []session.InputRequest{oneQuestion("i3")}
	f.s.PendingApprovals = []session.Approval{{ID: "a1", RequestType: session.RequestCommand, Request: &session.ToolRequest{ToolName: "Bash", Command: "ls"}}}
	f.frame()
	if !f.tr.Key("1") {
		t.Fatal("1 did nothing")
	}
	if ans, in := f.tr.Answers(), f.tr.Inputs(); len(ans) != 1 || ans[0].Decision != "allow" || len(in) != 0 {
		t.Fatalf("answers %+v, inputs %+v", ans, in)
	}
}
