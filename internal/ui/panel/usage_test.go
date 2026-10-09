package panel

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestUsageText: the header line and the Session note show tokens and the
// context always, the cost only when asked and priced, and say what is
// missing instead of guessing.
func TestUsageText(t *testing.T) {
	opus := flow.Usage{Model: "claude-opus-5-5", Context: 250_000, Models: map[string]flow.Tokens{
		"claude-opus-5-5":  {Input: 1_000_000, Output: 100_000},
		"claude-haiku-4-5": {Input: 20_000},
	}}
	for _, c := range []struct {
		name         string
		u            flow.Usage
		cost         bool
		header, note string
	}{
		{"tokens only", opus, false,
			"claude-opus-5-5 · 1.1M tokens · 25% context",
			"By model: claude-opus-5-5 1.1M, claude-haiku-4-5 20k. Context: 250k of 1M (25%)."},
		{"with cost", opus, true,
			"claude-opus-5-5 · 1.1M tokens · 25% context · $6.02",
			"By model: claude-opus-5-5 1.1M, claude-haiku-4-5 20k. Context: 250k of 1M (25%). $6.02 at list prices of " + flow.PricesAsOf + "."},
		{"unknown model", flow.Usage{Model: "local-llama", Context: 900, Models: map[string]flow.Tokens{"local-llama": {Input: 900, Output: 40}}}, true,
			"local-llama · 940 tokens",
			"Context: 900. No list price for local-llama, so no cost."},
		{"partial", flow.Usage{Model: "gpt-6.1-sol", Context: 129_200, Window: 258_400, Partial: true, Models: map[string]flow.Tokens{"gpt-6.1-sol": {Input: 2_000_000}}}, true,
			"gpt-6.1-sol · 2M+ tokens · 50% context · $4.00+",
			"Context: 129k of 258k (50%). $4.00 at list prices of " + flow.PricesAsOf + ". The session file is long: these count its last 8 MiB."},
	} {
		if got := headerText(c.u, c.cost); got != c.header {
			t.Errorf("%s: header %q, want %q", c.name, got, c.header)
		}
		if got := usageNote(c.u, c.cost); got != c.note {
			t.Errorf("%s: note %q, want %q", c.name, got, c.note)
		}
	}
	in := Input{Pane: &model.Pane{Provider: model.ProviderClaude}, Feed: &flow.Feed{}}
	if in.usage() != nil {
		t.Error("a feed with no tokens has usage")
	}
	in.Feed.Usage = opus
	if in.usage() == nil {
		t.Error("no usage for a feed with tokens")
	}
}
