package panel

import (
	"bytes"
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/antigravity"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/session/codex"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// turnFixture is a provider's transcript fixture and the line count that
// leaves its last turn running.
type turnFixture struct {
	name, path string
	reader     func() interface{ Line([]byte) []session.Event }
	running    int // lines read for the running turn
	finished   int // lines read for the finished one; 0 for all
}

var turnFixtures = []turnFixture{
	{"claude", "testdata/turns/claude.jsonl", func() interface{ Line([]byte) []session.Event } { return claude.NewTranscript() }, 25, 17},
	{"codex", "../../session/codex/testdata/rollout.jsonl", func() interface{ Line([]byte) []session.Event } { return codex.NewRollout() }, 26, 29},
	{"agy", "testdata/turns/agy.jsonl", func() interface{ Line([]byte) []session.Event } { return antigravity.NewTranscript() }, 8, 0},
}

// replay reads the fixture's first n lines (all for 0) as a pane chat
// reads its transcript, into a snapshot; running leaves its session
// running.
func (f turnFixture) replay(t testing.TB, n int, running bool) *session.Snapshot {
	t.Helper()
	b, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	if n > 0 {
		lines = lines[:n]
	}
	snap := session.Snapshot{SessionID: f.name, SessionState: session.StateReady}
	r := f.reader()
	for _, l := range lines {
		for _, e := range r.Line(l) {
			for _, d := range session.Reduce(snap, e) {
				d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = session.DeltaVersion, snap.SessionID, snap.Revision+1, snap.Revision+1, e.CreatedAt
				if s, err := session.Apply(&snap, snap.Revision, d); err == nil {
					snap = s
				}
			}
		}
	}
	if running {
		snap.SessionState, snap.Running = session.StateRunning, true
	}
	return &snap
}

// TestTurnShots writes PNGs of each provider's turn, running and
// finished, to $PITWALL_TOOL_SHOTS, for a look at them; it skips without
// it.
func TestTurnShots(t *testing.T) {
	dir := os.Getenv("PITWALL_TOOL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_TOOL_SHOTS is not set")
	}
	for _, f := range turnFixtures {
		for _, run := range []bool{false, true} {
			n, name := f.finished, f.name+"-finished"
			if run {
				n, name = f.running, f.name+"-running"
			}
			tr := &Transcript{Dir: "/home/user/calc"}
			tr.init()
			shoot(t, tr, f.replay(t, n, run), filepath.Join(dir, name+".png"), image.Pt(820, 900), 1.5)
		}
	}
	// Claude's finished turn with each fold open: thinking stays inside.
	f := turnFixtures[0]
	s := f.replay(t, f.finished, false)
	tr := &Transcript{Dir: "/home/user/calc"}
	var ops op.Ops
	transcriptFrame(tr, theme.Dark(), s, &ops, time.Now())
	for _, r := range tr.rows {
		if r.seg == segHead {
			tr.open[logID(s, r.b)] = true
		}
	}
	shoot(t, tr, s, filepath.Join(dir, "claude-open.png"), image.Pt(820, 1000), 1.5)
}
