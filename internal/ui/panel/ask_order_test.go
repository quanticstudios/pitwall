package panel

import (
	"image"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// rowKinds names the rows of tr's last frame: prompt, log (running or
// not), reply, res:<line>, card.
func rowKinds(tr *Transcript) []string {
	var out []string
	for _, r := range tr.rows {
		switch {
		case r.tail != nil:
			out = append(out, "card")
		case r.res != nil:
			out = append(out, "res:"+r.res.what)
		case r.b.kind == blockPrompt:
			out = append(out, "prompt")
		case r.seg == segHead && r.running:
			out = append(out, "log running")
		case r.seg == segHead:
			out = append(out, "log")
		case r.seg == segReply:
			out = append(out, "reply")
		}
	}
	return out
}

func layoutOnce(tr *Transcript, s *session.Snapshot) {
	var ops op.Ops
	tr.Layout(layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 800)), Now: time.Now()}, theme.Dark(), s, false)
}

// TestAskOrder: a Claude Code pane chat that asked, got its answer and
// worked on: the Answered line sits where the question was asked, and
// the work after it, running or done, comes below it, so the chat's end
// moves as the agent works. The line opens on the question, its
// options, the pick and the pick's preview.
func TestAskOrder(t *testing.T) {
	for _, tc := range []struct {
		file string
		want []string
	}{
		{"ask-then-work.jsonl", []string{"prompt", "reply", "res:Founder button (Recommended)", "log", "reply"}},
		{"ask-then-work-running.jsonl", []string{"prompt", "reply", "res:Founder button (Recommended)", "log running"}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			s := renderSnap(t, filepath.Join("testdata", tc.file))
			running := tc.file == "ask-then-work-running.jsonl"
			s.SessionState = map[bool]string{true: session.StateRunning, false: session.StateReady}[running]
			for i := range s.Turns {
				s.Turns[i].State = map[bool]string{true: session.TurnRunning, false: session.TurnCompleted}[running]
			}
			var tr Transcript
			layoutOnce(&tr, &s)
			if got := rowKinds(&tr); !slices.Equal(got, tc.want) {
				t.Fatalf("rows %q\nwant %q", got, tc.want)
			}
			i := slices.IndexFunc(tr.rows, func(r trow) bool { return r.res != nil })
			r := tr.rows[i].res
			if len(r.qs) != 1 || len(r.qs[0].options) != 2 || !slices.Equal(r.qs[0].picked, []string{"Founder button (Recommended)"}) || r.qs[0].options[0].Preview == "" || r.when.IsZero() {
				t.Fatalf("answered line %+v", r)
			}
			if tr.rows[i].open {
				t.Fatal("the line starts open")
			}
			tr.click("res:" + r.id).Click()
			layoutOnce(&tr, &s)
			i = slices.IndexFunc(tr.rows, func(r trow) bool { return r.res != nil })
			if !tr.rows[i].open {
				t.Fatal("a click did not open the line")
			}
			if running {
				d := newDesk(t, &s)
				d.shot("order-running")
				d.tr.click("res:" + r.id).Click()
				d.frame()
				d.frame()
				d.shot("order-running-open")
			}
		})
	}
}

// TestResolvedOrder: lines answered in this window go where they were
// answered: an approval allowed mid-turn sits between the work before
// and after it; a question answered here hands its line to the
// transcript's own once the call has its answer, with no second line.
func TestResolvedOrder(t *testing.T) {
	call := func(id, tool, out string) session.Item {
		it := turnItem(id, session.KindToolCall, "t1")
		it.Tool.ToolName, it.Tool.Output = tool, out
		return it
	}
	s := &session.Snapshot{SessionID: "c", SessionState: session.StateRunning, Turns: []session.TurnSummary{{ID: "t1", State: session.TurnRunning}},
		Content:          []session.Item{turnItem("p", session.KindUserPrompt, "t1"), call("c1", "Bash", "ok"), call("c2", "Bash", "")},
		PendingApprovals: []session.Approval{{ID: "a1", RequestType: session.RequestCommand, Request: &session.ToolRequest{ToolName: "Bash", Command: "rm -rf build\nmake"}}}}
	var tr Transcript
	layoutOnce(&tr, s)
	tr.decide("a1", "allow")
	s.PendingApprovals = nil
	s.Content = append(s.Content, call("c3", "Bash", "ok"), call("c4", "Bash", ""))
	layoutOnce(&tr, s)
	if got, want := rowKinds(&tr), []string{"prompt", "log", "res:rm -rf build\nmake", "log running"}; !slices.Equal(got, want) {
		t.Fatalf("approval: rows %q\nwant %q", got, want)
	}
	if r := tr.done[0]; r.cmd != "rm -rf build\nmake" || !r.details() {
		t.Fatalf("approval line %+v", r)
	}

	q := session.Question{ID: "Which?", Prompt: "Which?", Options: []session.PromptOption{{Label: "Red"}, {Label: "Blue"}}}
	ask := call("q1", "AskUserQuestion", "")
	ask.Tool.Input = []byte(`{"questions":[{"question":"Which?","options":[{"label":"Red"},{"label":"Blue"}]}]}`)
	s.Content = append(s.Content, ask)
	s.PendingUserInputs = []session.InputRequest{{ID: "i1", Questions: []session.Question{q}}}
	layoutOnce(&tr, s)
	tr.pick("i1", 0, 1)
	layoutOnce(&tr, s)
	if got := rowKinds(&tr); got[len(got)-1] != "res:Blue" {
		t.Fatalf("before the transcript has it: %q", got)
	}
	s.PendingUserInputs = nil
	s.Content[len(s.Content)-1].Tool.Output = `User has answered your questions: "Which?"="Blue". You can now continue.`
	s.Content = append(s.Content, call("c5", "Bash", ""))
	layoutOnce(&tr, s)
	if got, want := rowKinds(&tr), []string{"prompt", "log", "res:rm -rf build\nmake", "log", "res:Blue", "log running"}; !slices.Equal(got, want) {
		t.Fatalf("after: rows %q\nwant %q", got, want)
	}
	d := newDesk(t, s)
	d.tr.done = slices.Clone(tr.done)
	d.tr.decide("x", "deny") // a denial with its reason, opened below
	d.tr.done[len(d.tr.done)-1] = resolved{id: "x", line: "Denied", what: "use the cache instead", prose: true, denied: true, at: 6,
		cmd: "rm -rf ~/.cache/build", reason: "use the cache instead", when: d.now}
	d.frame()
	d.tr.click("res:a1").Click()
	d.tr.click("res:x").Click()
	d.frame()
	d.frame()
	d.shot("resolved-approvals-open")
}
