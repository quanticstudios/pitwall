package panel

import (
	"fmt"
	"image"
	"reflect"
	"testing"
	"time"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// longChat is a chat of n items: prompts, replies, reasoning and tool
// calls in turn, the last call running, with an approval pending.
func longChat(n int) *session.Snapshot {
	s := &session.Snapshot{SessionID: "0123456789abcdef", SessionState: session.StateWaiting, Running: true}
	kinds := []session.ItemKind{session.KindUserPrompt, session.KindReasoningText, session.KindToolCall, session.KindAssistantText, session.KindSystem}
	for i := range n {
		it := session.Item{ID: fmt.Sprint("i", i), Kind: kinds[i%len(kinds)], Text: fmt.Sprintf("item %d with some words that wrap across the column when it is narrow enough", i)}
		if it.Kind == session.KindToolCall {
			it.Text = ""
			it.Tool = &session.ToolCall{ToolName: "Bash", Command: fmt.Sprint("go test ./... # ", i), Status: "completed", Output: "ok\nok"}
			if i >= n-5 {
				it.Tool.Status = "running"
			}
		}
		s.Content = append(s.Content, it)
	}
	detail := "make test"
	s.PendingApprovals = []session.Approval{{ID: "r1", Detail: &detail, RequestType: session.RequestCommand}}
	return s
}

func transcriptFrame(t *Transcript, th *theme.Theme, s *session.Snapshot, ops *op.Ops, now time.Time) {
	ops.Reset()
	gtx := layout.Context{Ops: ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 800)), Now: now}
	t.Layout(gtx, th, s, false)
}

// TestTranscriptLong: a 2,000-item chat lays out only the items in view,
// follows its end, and an opened call stays open.
func TestTranscriptLong(t *testing.T) {
	th := theme.Dark()
	s := longChat(2000)
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	for i := range 3 {
		transcriptFrame(&tr, th, s, &ops, now.Add(time.Duration(i)*time.Second/30))
	}
	if tr.laid == 0 || tr.laid > 60 {
		t.Fatalf("laid out %d of 2,000 items in an 800px view", tr.laid)
	}
	if tr.list.Position.First < len(tr.rows)-40 {
		t.Fatalf("shows row %d of %d, want the end", tr.list.Position.First, len(tr.rows))
	}
	id := s.Content[len(s.Content)-3].ID // a running call
	tr.open[id] = true
	transcriptFrame(&tr, th, s, &ops, now.Add(time.Second))
	if !tr.open[id] {
		t.Fatalf("call %s: open %v", id, tr.open[id])
	}

	short := longChat(3)
	var top Transcript
	transcriptFrame(&top, th, short, &ops, now)
	transcriptFrame(&top, th, short, &ops, now)
	if top.laid != 3 || top.list.Position.First != 0 {
		t.Fatalf("short chat: laid %d, first %d", top.laid, top.list.Position.First)
	}
}

// BenchmarkTranscript is one frame of a 2,000-item chat.
func BenchmarkTranscript(b *testing.B) {
	th := theme.Dark()
	s := longChat(2000)
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	for i := 0; b.Loop(); i++ {
		transcriptFrame(&tr, th, s, &ops, now.Add(time.Duration(i)*time.Millisecond))
	}
}

// TestTranscriptAnswers: an approval's buttons answer it once and the
// card becomes a line saying so; the number keys answer the newest one;
// Open in aide shows and reports only when asked for.
func TestTranscriptAnswers(t *testing.T) {
	th := theme.Dark()
	s := longChat(3)
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	transcriptFrame(&tr, th, s, &ops, now)
	if tr.answer["r1"] == nil {
		t.Fatal("no buttons for the pending approval")
	}
	tr.answer["r1"][1].Click()
	tr.answer["r1"][2].Click() // a second button in the same frame is not a second answer
	transcriptFrame(&tr, th, s, &ops, now)
	if got := tr.Answers(); len(got) != 1 || got[0] != (Answer{Request: "r1", Decision: "allow-session"}) {
		t.Fatalf("answers %+v", got)
	}
	if len(tr.done) != 1 || tr.done[0].line != "Allowed for this session" || tr.done[0].what != "make test" {
		t.Fatalf("answered line %+v", tr.done)
	}
	if tr.Key("1") {
		t.Fatal("a key answered an approval already answered")
	}
	s.PendingApprovals = append(s.PendingApprovals, session.Approval{ID: "r2", RequestType: session.RequestCommand, Request: &session.ToolRequest{ToolName: "Bash", Command: "git push"}})
	transcriptFrame(&tr, th, s, &ops, now)
	if !tr.Key("3") {
		t.Fatal("3 did not answer the newest approval")
	}
	if got := tr.Answers(); len(got) != 1 || got[0] != (Answer{Request: "r2", Decision: "deny"}) {
		t.Fatalf("key answer %+v", got)
	}
	s.PendingApprovals = nil
	transcriptFrame(&tr, th, s, &ops, now)
	if tr.answer["r1"] != nil || tr.sent["r1"] || len(tr.done) != 2 {
		t.Fatal("a resolved approval keeps its card, or loses its line")
	}

	tr.aideBtn.Click()
	transcriptFrame(&tr, th, s, &ops, now)
	if tr.OpenAide() {
		t.Fatal("Open in aide without aide")
	}
	tr.aideBtn.Click()
	ops.Reset()
	tr.Layout(layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 800)), Now: now}, th, s, true)
	if !tr.OpenAide() || tr.OpenAide() {
		t.Fatal("Open in aide is not reported once")
	}
}

// TestTranscriptQuestion: a question starts on its recommended option,
// number keys pick, Enter sends once, and a typed answer counts.
func TestTranscriptQuestion(t *testing.T) {
	th := theme.Dark()
	s := &session.Snapshot{SessionID: "chat", SessionState: session.StateWaiting, PendingUserInputs: []session.InputRequest{{ID: "q1", Questions: []session.Question{{
		ID: "size", Prompt: "Resize photos?",
		Options: []session.PromptOption{{ID: "a", Label: "Resize (Recommended)"}, {ID: "b", Label: "Keep"}},
	}}}}}
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	transcriptFrame(&tr, th, s, &ops, now)
	a := tr.asks["q1"]
	if a == nil || a.pick[0] != 0 {
		t.Fatalf("recommended option not picked: %+v", a)
	}
	if !tr.Key("2") || a.pick[0] != 1 {
		t.Fatalf("2 picked %d", a.pick[0])
	}
	if !tr.Key(string(key.NameReturn)) {
		t.Fatal("Enter did not answer")
	}
	got := tr.Inputs()
	if len(got) != 1 || got[0].Request != "q1" || !reflect.DeepEqual(got[0].Answers, map[string][]string{"size": {"Keep"}}) {
		t.Fatalf("inputs %+v", got)
	}
	if tr.Key(string(key.NameReturn)); len(tr.Inputs()) != 0 {
		t.Fatal("answered twice")
	}

	typed := &ask{pick: []int{2}, other: make([]widget.Editor, 1)}
	typed.other[0].SetText("  only the big ones ")
	if ans, ok := typed.answers(s.PendingUserInputs[0].Questions); !ok || ans["size"][0] != "only the big ones" {
		t.Fatalf("typed answer %v %v", ans, ok)
	}
	typed.other[0].SetText("")
	if _, ok := typed.answers(s.PendingUserInputs[0].Questions); ok {
		t.Fatal("an empty typed answer sends")
	}
}
