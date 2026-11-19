package panel

import (
	"fmt"
	"testing"
	"time"

	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestTranscriptHistory: near the top the transcript asks for older
// history and says it loads; a page landing above keeps the row in view
// where it was; cut items are read whole once laid out and not before;
// and scrolling through 50,000 items keeps the laid-out text bounded.
func TestTranscriptHistory(t *testing.T) {
	th := theme.Dark()
	all := longChat(1000)
	all.PendingApprovals = nil
	for i := range all.Content {
		all.Content[i].ID = fmt.Sprint("h", i)
	}
	tail := *all
	tail.Content = all.Content[600:]
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	asked, want := 0, map[string]bool{}
	tr.Older = func() bool { asked++; return true }
	tr.Full = func(id string) { want[id] = true }
	transcriptFrame(&tr, th, &tail, &ops, now)
	transcriptFrame(&tr, th, &tail, &ops, now) // the list goes to its end
	if asked != 0 {
		t.Fatal("asked for older history from the end")
	}
	tr.list.Position.First, tr.list.Position.Offset, tr.list.Position.BeforeEnd = 3, 5, true
	transcriptFrame(&tr, th, &tail, &ops, now)
	if asked == 0 || !tr.older {
		t.Fatal("near the top: no read of older history")
	}
	first := tr.anchor
	tr.done = append(tr.done, resolved{id: "r", line: "Allowed", at: 10})
	transcriptFrame(&tr, th, all, &ops, now) // the page landed
	if tr.anchor != first || tr.list.Position.Offset != 5 || tr.done[0].at != 610 {
		t.Fatalf("after the page: first in view %s, want %s; offset %d; answered line at %d", tr.anchor, first, tr.list.Position.Offset, tr.done[0].at)
	}

	all.Content[995].Cut = true // a reply, in view at the end
	all.Content[602].Cut = true // in a folded work log, far up
	tr.list.Position.BeforeEnd = false
	transcriptFrame(&tr, th, all, &ops, now)
	transcriptFrame(&tr, th, all, &ops, now)
	if !want["h995"] || want["h602"] {
		t.Fatalf("read whole: %v; want h995 alone", want)
	}

	big := longChat(50000)
	var long Transcript
	long.list.Position.BeforeEnd = true
	transcriptFrame(&long, th, big, &ops, now)
	most := 0
	for first := 0; first < len(long.rows); first += 25 {
		long.list.Position.First, long.list.Position.BeforeEnd = first, true
		transcriptFrame(&long, th, big, &ops, now)
		most = max(most, len(long.docs)+len(long.diffs)+len(long.sels))
	}
	if most > 2*keepLaid+200 {
		t.Fatalf("held %d laid-out texts scrolling 50,000 items", most)
	}
}

// BenchmarkTranscript50k is one frame of a 50,000-item chat.
func BenchmarkTranscript50k(b *testing.B) {
	th := theme.Dark()
	s := longChat(50000)
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	for i := 0; b.Loop(); i++ {
		transcriptFrame(&tr, th, s, &ops, now.Add(time.Duration(i)*time.Millisecond))
	}
}

// BenchmarkTranscriptScroll is one frame of a 50,000-item chat scrolled
// by 3 rows a frame, end to start.
func BenchmarkTranscriptScroll(b *testing.B) {
	th := theme.Dark()
	s := longChat(50000)
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
