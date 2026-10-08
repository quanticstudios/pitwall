package flow

import (
	"math"
	"reflect"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

// TestClaudeUsage: a reply's entries count once, sidechain and synthetic
// entries not at all, the subagents' files add their models, and the
// context is the latest call's input.
func TestClaudeUsage(t *testing.T) {
	u := read(t, model.ProviderClaude, "testdata/claude/session.jsonl").Usage
	want := map[string]Tokens{
		"claude-opus-5-5":           {Input: 10, Output: 180, CacheRead: 26000, CacheWrite: 1500, CacheWrite1h: 1200},
		"claude-haiku-4-5-20251001": {Input: 150, Output: 15},
		"claude-sonnet-5-5":         {Input: 10, Output: 1},
	}
	if !reflect.DeepEqual(u.Models, want) {
		t.Errorf("models:\n got %+v\nwant %+v", u.Models, want)
	}
	if u.Model != "claude-opus-5-5" || u.Context != 4+8000+300 || u.Partial {
		t.Errorf("usage = %+v", u)
	}
	if f, ok := u.Fill(); !ok || !near(f, 8304.0/1e6) {
		t.Errorf("fill = %v %v", f, ok)
	}
	// Opus: 10 in at $4, 180 out at $20, 26000 read at $0.20, 300 5m
	// writes at $5 and 1200 1h writes at $8; then Haiku and Sonnet.
	opus := (10*4 + 180*20 + 26000*0.2 + 300*5 + 1200*8) / 1e6
	if c, ok := u.Cost(); !ok || !near(c, opus+(150*1+15*5)/1e6+(10*2+1*10)/1e6) {
		t.Errorf("cost = %v %v", c, ok)
	}
}

// TestCodexUsage: the parent counts what its running total grew by, a
// repeated total once; the forked child skips its copy of the parent's
// usage and counts its own under its own model.
func TestCodexUsage(t *testing.T) {
	u := read(t, model.ProviderCodex, "testdata/codex/2026/10/05/rollout-2026-10-05T10-00-00-parent-thread.jsonl").Usage
	want := map[string]Tokens{
		"gpt-6.1-sol": {Input: 1000, Output: 250, CacheRead: 2000},
		"gpt-6-luna":  {Input: 500, Output: 20},
	}
	if !reflect.DeepEqual(u.Models, want) {
		t.Errorf("models:\n got %+v\nwant %+v", u.Models, want)
	}
	if u.Model != "gpt-6.1-sol" || u.Context != 2000 || u.Window != 258400 {
		t.Errorf("usage = %+v", u)
	}
	if f, ok := u.Fill(); !ok || !near(f, 2000.0/258400) {
		t.Errorf("fill = %v %v", f, ok)
	}
	if c, ok := u.Cost(); !ok || !near(c, (1000*2+2000*0.1+250*10+500*0.1+20*0.5)/1e6) {
		t.Errorf("cost = %v %v", c, ok)
	}
}

func TestPiUsage(t *testing.T) {
	u := read(t, model.ProviderPi, "testdata/pi/2026-10-05T10-00-00-000Z_s1.jsonl").Usage
	if want := map[string]Tokens{"gpt-6.1-sol": {Input: 175, Output: 53, CacheRead: 370}}; !reflect.DeepEqual(u.Models, want) {
		t.Errorf("models = %+v", u.Models)
	}
	if f, ok := u.Fill(); u.Context != 200 || !ok || !near(f, 200.0/1_050_000) {
		t.Errorf("usage = %+v, fill %v %v", u, f, ok)
	}
}

// TestCost: a model without a price, fast mode included, leaves the whole
// cost unknown; one with no tokens does not count.
func TestCost(t *testing.T) {
	known := Tokens{Input: 1_000_000}
	for _, c := range []struct {
		models map[string]Tokens
		want   float64
		ok     bool
	}{
		{nil, 0, false},
		{map[string]Tokens{"claude-opus-5-5": known}, 4, true},
		{map[string]Tokens{"claude-opus-5-5-20260922": known}, 4, true},
		{map[string]Tokens{"claude-opus-5-5": known, "local-llama": {}}, 4, true},
		{map[string]Tokens{"claude-opus-5-5": known, "local-llama": {Output: 1}}, 0, false},
		{map[string]Tokens{"claude-opus-5-5 (fast)": known}, 0, false},
		{map[string]Tokens{"gpt-6-luna": {CacheRead: 2_000_000}}, 0.02, true},
	} {
		got, ok := Usage{Models: c.models}.Cost()
		if ok != c.ok || !near(got, c.want) {
			t.Errorf("%v: cost = %v %v, want %v %v", c.models, got, ok, c.want, c.ok)
		}
	}
	if w := ContextWindow("claude-opus-5-5 (fast)"); w != 1_000_000 {
		t.Errorf("fast mode's window = %d", w)
	}
	if _, ok := (Usage{Model: "local-llama", Context: 10}).Fill(); ok {
		t.Error("a fill for a model with no known window")
	}
}

// TestUsageAdd: tokens add up by model, and the fuller context stands.
func TestUsageAdd(t *testing.T) {
	u := Usage{Models: map[string]Tokens{"claude-opus-5-5": {Input: 1}}, Model: "claude-opus-5-5", Context: 100_000}
	u.Add(Usage{Models: map[string]Tokens{"claude-opus-5-5": {Input: 2}, "gpt-6-luna": {Output: 3}}, Model: "gpt-6-luna", Context: 10, Partial: true})
	if u.Models["claude-opus-5-5"].Input != 3 || u.Models["gpt-6-luna"].Output != 3 || u.Model != "claude-opus-5-5" || !u.Partial {
		t.Errorf("usage = %+v", u)
	}
	u.Add(Usage{Model: "gpt-6-luna", Context: 600_000})
	if u.Model != "gpt-6-luna" || u.Context != 600_000 {
		t.Errorf("usage = %+v, want the fuller context", u)
	}
}
