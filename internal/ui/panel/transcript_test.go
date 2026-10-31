package panel

import (
	"fmt"
	"image"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

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
	t.Layout(gtx, th, s)
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
	if tr.list.Position.First < 1900 {
		t.Fatalf("shows item %d, want the end", tr.list.Position.First)
	}
	id := s.Content[len(s.Content)-3].ID // a running call
	tr.open[id] = true
	transcriptFrame(&tr, th, s, &ops, now.Add(time.Second))
	if !tr.open[id] || tr.btn[id] == nil {
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
