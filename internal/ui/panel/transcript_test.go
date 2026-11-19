package panel

import (
	"fmt"
	"image"
	"reflect"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
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

	// A number key that picks Other puts the keys in its field.
	s.PendingUserInputs[0].ID = "q2"
	var r input.Router
	frame := func() {
		ops.Reset()
		tr.Layout(layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 800)), Now: now}, th, s, false)
		r.Frame(&ops)
	}
	frame()
	if !tr.Key("3") || tr.asks["q2"].pick[0] != 2 {
		t.Fatal("3 did not pick Other")
	}
	frame()
	frame()
	if !r.Source().Focused(&tr.asks["q2"].other[0]) {
		t.Fatal("picking Other by key did not focus its field")
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

// longTurn is a prompt and one running turn of n calls.
func longTurn(n int) *session.Snapshot {
	turn := "t1"
	s := &session.Snapshot{SessionID: "0123456789abcdef", Running: true, Turns: []session.TurnSummary{{ID: turn, State: session.TurnRunning}}}
	s.Content = append(s.Content, session.Item{ID: "p", Kind: session.KindUserPrompt, Text: "fix the build"})
	for i := range n {
		s.Content = append(s.Content, session.Item{ID: fmt.Sprint("c", i), Kind: session.KindToolCall, TurnID: &turn,
			Tool: &session.ToolCall{ToolName: "Bash", Command: fmt.Sprint("go test ./... # ", i), Status: "completed", Output: "ok\nok"}})
	}
	return s
}

// TestTranscriptTurn: a work log is one header row, running or done,
// until the user opens it. Open, it is a list row per call, so only the
// calls in view are laid out, and it stays open after the turn ends. A
// failed call does not open its log.
func TestTranscriptTurn(t *testing.T) {
	th := theme.Dark()
	s := longTurn(2000)
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	transcriptFrame(&tr, th, s, &ops, now)
	transcriptFrame(&tr, th, s, &ops, now)
	if len(tr.rows) != 2 || tr.rows[1].seg != segHead || !tr.rows[1].running || tr.rows[1].open || !tr.rows[1].last {
		t.Fatalf("running turn: %d rows, want the prompt and one folded header", len(tr.rows))
	}
	tr.clicks[logID(s, tr.rows[1].b)].Click()
	transcriptFrame(&tr, th, s, &ops, now)
	transcriptFrame(&tr, th, s, &ops, now)
	if len(tr.rows) != 2002 || tr.laid == 0 || tr.laid > 60 {
		t.Fatalf("opened log: %d rows, laid out %d of 2,000 calls", len(tr.rows), tr.laid)
	}
	s.Turns[0].State = session.TurnCompleted
	s.Running = false
	transcriptFrame(&tr, th, s, &ops, now.Add(time.Minute))
	transcriptFrame(&tr, th, s, &ops, now.Add(time.Minute))
	if len(tr.rows) != 2002 || tr.rows[1].running {
		t.Fatalf("finished turn the user opened: %d rows", len(tr.rows))
	}

	done := longTurn(3)
	done.Turns[0].State = session.TurnCompleted
	done.Content[2].Tool.Status = "failed"
	var fresh Transcript
	transcriptFrame(&fresh, th, done, &ops, now)
	if len(fresh.rows) != 2 || fresh.rows[1].open {
		t.Fatalf("finished turn with a failed call: %d rows, want one folded header", len(fresh.rows))
	}
}

// TestTranscriptAnsweredLine: an approval answered mid-turn keeps its
// line above the reply that came after it, not under the reply.
func TestTranscriptAnsweredLine(t *testing.T) {
	th := theme.Dark()
	s := longTurn(2)
	s.PendingApprovals = []session.Approval{{ID: "r1", RequestType: session.RequestCommand, Request: &session.ToolRequest{ToolName: "Bash", Command: "make"}}}
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	transcriptFrame(&tr, th, s, &ops, now)
	if !tr.Key("1") {
		t.Fatal("1 did not answer the approval")
	}
	s.PendingApprovals = nil
	turn := "t1"
	s.Content = append(s.Content, session.Item{ID: "c2", Kind: session.KindToolCall, TurnID: &turn,
		Tool: &session.ToolCall{ToolName: "Bash", Command: "make", Status: "completed"}},
		session.Item{ID: "a", Kind: session.KindAssistantText, TurnID: &turn, Text: "Built."})
	s.Running, s.Turns[0].State = false, session.TurnCompleted
	transcriptFrame(&tr, th, s, &ops, now)
	var order []string
	for _, r := range tr.rows {
		switch {
		case r.res != nil:
			order = append(order, "answered")
		case r.seg == segReply:
			order = append(order, "reply")
		}
	}
	if !reflect.DeepEqual(order, []string{"answered", "reply"}) {
		t.Fatalf("rows %v, want the answered line above the reply", order)
	}
}

// BenchmarkTranscriptTurn is one frame of a running turn of 2,000 calls,
// its work log open.
func BenchmarkTranscriptTurn(b *testing.B) {
	th := theme.Dark()
	s := longTurn(2000)
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	for i := 0; b.Loop(); i++ {
		transcriptFrame(&tr, th, s, &ops, now.Add(time.Duration(i)*time.Millisecond))
	}
}

// TestTranscriptFollow: pinned at the end, a growing reply and new rows
// keep the end in view; a wheel scroll up stops following and new output
// leaves the reader where they are; a new prompt, or ToEnd as sending
// does, goes back to the end and follows again.
func TestTranscriptFollow(t *testing.T) {
	th := theme.Dark()
	s := longChat(200)
	s.PendingApprovals = nil
	var tr Transcript
	var ops op.Ops
	var r input.Router
	now := time.Now()
	frame := func() {
		now = now.Add(time.Second / 60)
		ops.Reset()
		tr.Layout(layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1000, 800)), Now: now}, th, s, false)
		r.Frame(&ops)
	}
	atEnd := func(what string) {
		t.Helper()
		if tr.list.Position.BeforeEnd || tr.list.Position.First+tr.list.Position.Count != len(tr.rows)+2 {
			t.Fatalf("%s: rows %d+%d of %d, before end %v", what, tr.list.Position.First, tr.list.Position.Count, len(tr.rows)+2, tr.list.Position.BeforeEnd)
		}
	}
	add := func(it session.Item) { s.Content = append(s.Content, it) }
	for range 3 {
		frame()
	}
	atEnd("opened")

	add(session.Item{ID: "reply", Kind: session.KindAssistantText, Text: "first words"})
	frame()
	for i := range 40 {
		s.Content[len(s.Content)-1].Text += fmt.Sprintf("\n\nline %d of a reply that streams in", i)
		frame()
		atEnd(fmt.Sprint("growing reply, delta ", i))
	}
	add(session.Item{ID: "tool", Kind: session.KindToolCall, Tool: &session.ToolCall{ToolName: "Bash", Command: "go vet ./...", Status: "running"}})
	frame()
	atEnd("tool row")

	r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(500, 400), Scroll: f32.Pt(0, -600)})
	frame()
	frame()
	if !tr.list.Position.BeforeEnd {
		t.Fatal("a scroll up left the transcript at its end")
	}
	first, off := tr.list.Position.First, tr.list.Position.Offset
	s.Content[len(s.Content)-1].Tool.Output = "ok"
	add(session.Item{ID: "more", Kind: session.KindAssistantText, Text: "more output\n\nand more"})
	frame()
	if tr.list.Position.First != first || tr.list.Position.Offset != off || !tr.list.Position.BeforeEnd {
		t.Fatalf("new output moved a scrolled reader: row %d+%d, was %d+%d", tr.list.Position.First, tr.list.Position.Offset, first, off)
	}

	add(session.Item{ID: "p2", Kind: session.KindUserPrompt, Text: "next, please"})
	frame()
	frame()
	atEnd("new prompt")

	r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(500, 400), Scroll: f32.Pt(0, -600)})
	frame()
	frame()
	if !tr.list.Position.BeforeEnd {
		t.Fatal("a second scroll up left the transcript at its end")
	}
	tr.ToEnd()
	frame()
	atEnd("ToEnd")
	s.Content[len(s.Content)-1].Text += "\n\ngrows after the send"
	frame()
	atEnd("growth after ToEnd")
}
