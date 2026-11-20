package panel

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// cardChat is each command card under its chip, from testdata/cmdcard,
// and an output no card reads.
func cardChat(t *testing.T) *session.Snapshot {
	at := func(s int) string { return time.Date(2026, 11, 19, 14, 0, s, 0, time.UTC).Format(time.RFC3339Nano) }
	s := &session.Snapshot{SessionID: "c1", SessionState: session.StateReady}
	for i, c := range []struct{ cmd, file string }{
		{"/usage", "usage-stream.txt"}, {"/usage", "usage-history.txt"}, {"/cost", "cost-tui.txt"}, {"/context", "context-tui.txt"},
		{"/context", "context-md.txt"}, {"/status", "status.txt"}, {"/doctor", ""},
	} {
		out := "All checks passed"
		if c.file != "" {
			out = cardFixture(t, c.file)
		}
		id := string(rune('a' + i))
		s.Content = append(s.Content,
			session.Item{ID: id + "c", Kind: session.KindSystem, Phase: session.PhaseCommand, Text: c.cmd, CreatedAt: at(2 * i)},
			session.Item{ID: id + "o", Kind: session.KindSystem, Phase: session.PhaseOutput, Text: out, CreatedAt: at(2*i + 1)})
	}
	return s
}

// TestCardShots writes PNGs of the command cards, folded and open, to
// $PITWALL_TOOL_SHOTS; it skips without it.
func TestCardShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TOOL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TOOL_SHOTS is not set")
	}
	for _, open := range []bool{false, true} {
		s := cardChat(t)
		tr := &Transcript{}
		tr.init()
		name := "folded"
		if open {
			name = "open"
			for _, it := range s.Content {
				tr.open["card:"+it.ID] = true
			}
		}
		shoot(t, tr, s, filepath.Join(dir, "cards-"+name+".png"), image.Pt(900, 2200), 1.25)
	}
}
