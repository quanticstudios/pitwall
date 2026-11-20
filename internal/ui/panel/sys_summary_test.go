package panel

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// TestIsSummary: only a reasoning item marked summary draws as one.
func TestIsSummary(t *testing.T) {
	if !isSummary(&session.Item{Kind: session.KindReasoningText, Phase: session.PhaseSummary}) {
		t.Error("a summary block")
	}
	if isSummary(&session.Item{Kind: session.KindReasoningText}) || isSummary(&session.Item{Kind: session.KindSystem, Phase: session.PhaseSummary}) {
		t.Error("thinking, or a system item, is no summary")
	}
}
