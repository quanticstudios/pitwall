package panel

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// codeChat is a chat of n replies, each with a 40-line Go block.
func codeChat(n int) *session.Snapshot {
	var b strings.Builder
	b.WriteString("Here is the change:\n\n```go\n")
	for i := range 40 {
		fmt.Fprintf(&b, "func f%d(x int) (string, error) { return fmt.Sprintf(\"%%d\", x+%d), nil } // line %d\n", i, i, i)
	}
	b.WriteString("```\n\nRun it with:\n\n```sh\ngo test ./...\n```\n")
	s := &session.Snapshot{SessionID: "0123456789abcdef", SessionState: session.StateReady}
	for i := range n {
		s.Content = append(s.Content, session.Item{ID: fmt.Sprint("p", i), Kind: session.KindUserPrompt, Text: "show me"})
		s.Content = append(s.Content, session.Item{ID: fmt.Sprint("a", i), Kind: session.KindAssistantText, Text: b.String()})
	}
	return s
}

// BenchmarkTranscriptCode is one frame of a chat of code replies
// scrolled 3 rows a frame, end to start.
func BenchmarkTranscriptCode(b *testing.B) {
	th := theme.Dark()
	s := codeChat(200)
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	transcriptFrame(&tr, th, s, &ops, now)
	transcriptFrame(&tr, th, s, &ops, now)
	first := len(tr.rows)
	for i := 0; b.Loop(); i++ {
		if first -= 3; first < 0 {
			first = len(tr.rows)
		}
		tr.list.Position.First, tr.list.Position.BeforeEnd = first, true
		transcriptFrame(&tr, th, s, &ops, now.Add(time.Duration(i)*time.Millisecond))
	}
}
