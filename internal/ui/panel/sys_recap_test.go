package panel

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// TestBlocksRecap: /recap's recap ends its turn and stands outside a work
// log, which would hide it folded.
func TestBlocksRecap(t *testing.T) {
	r := turnItem("r", session.KindSystem, "t1")
	r.Phase = session.PhaseRecap
	got := blocks([]session.Item{turnItem("p", session.KindUserPrompt, "t1"), r}, nil)
	if len(got) != 2 || got[1].kind != blockSystem {
		t.Fatalf("blocks %+v: want the prompt, then the recap on its own", got)
	}
}
