package panel

import (
	"bytes"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/antigravity"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestAgySubagentShot draws an Antigravity pane chat with parallel subagents
// offscreen (both mid-turn with one completed and one running, and completed)
// and writes PNGs when PITWALL_RENDER_DIR is set.
func TestAgySubagentShot(t *testing.T) {
	c, _ := config.Builtin("aide-dark")
	th, err := theme.New(c, config.Font{UISize: 13})
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) [][]byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "..", "session", "antigravity", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	}
	mainLines := read("subagent_main.jsonl")
	child1 := read("subagent_child1.jsonl")
	child2 := read("subagent_child2.jsonl")

	build := func(upTo int, child2Lines int) session.Snapshot {
		tr := antigravity.NewTranscript()
		snap := session.Snapshot{SessionID: "agy-sub", Provider: model.ProviderAntigravity, SessionState: session.StateReady, Running: true}
		for i, l := range mainLines[:upTo] {
			snap = reduceAll(t, snap, tr.Line(l))
			if i == 3 {
				for _, cl := range child1 {
					snap = reduceAll(t, snap, tr.Subagent("step-1#0", cl))
				}
				for _, cl := range child2[:child2Lines] {
					snap = reduceAll(t, snap, tr.Subagent("step-1#1", cl))
				}
			}
		}
		return snap
	}

	dir := os.Getenv("PITWALL_RENDER_DIR")
	for _, tc := range []struct {
		name string
		snap session.Snapshot
		h    int
	}{
		{"agy-subagents-running", build(7, 3), 900},
		{"agy-subagents-done", build(len(mainLines), len(child2)), 1000},
	} {
		img := drawSnap(t, th, tc.snap, 920, tc.h, 3)
		if dir == "" {
			continue
		}
		f, err := os.Create(filepath.Join(dir, tc.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatal(err)
		}
		f.Close()
	}
}
