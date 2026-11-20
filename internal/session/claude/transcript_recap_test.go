package claude

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// TestRecapTranscript reads a live TUI transcript, a /recap, a Monitor
// watch and a background shell in it, with a summary block and a recap
// line added in the shapes Claude Code writes them.
func TestRecapTranscript(t *testing.T) {
	b, err := os.ReadFile("testdata/transcripts/tui-recap.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTranscript()
	snap := base()
	for _, l := range bytes.Split(b, []byte("\n")) {
		snap = apply(t, snap, tr.Line(l))
	}
	n := map[string]int{}
	for _, it := range snap.Content {
		switch {
		case it.Phase == session.PhaseRecap:
			n["recap"]++
		case it.Phase == session.PhaseSummary:
			n["summary"]++
		case it.Kind == session.KindSystem && it.Phase == session.PhaseCommand:
			n["chip:"+it.Text]++
		case it.Tool != nil && it.Tool.ToolName == "Monitor" && it.Tool.Background == "completed" && strings.HasSuffix(it.Tool.Output, "tick 1\ntick 2"):
			n["monitor"]++
		case it.Tool != nil && it.Tool.ToolName == "Bash" && it.Tool.Background == "completed":
			n["shell"]++
		}
	}
	if n["recap"] != 2 || n["summary"] != 1 || n["monitor"] != 1 || n["shell"] != 1 || n["chip:/recap"] != 1 {
		t.Fatalf("got %v: want 2 recaps, a summary, the Monitor and the shell ended, one /recap chip", n)
	}
}
