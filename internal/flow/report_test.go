package flow

import (
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// TestReport: calls fall on their local day, a day before the window drops
// out, sessions count per agent, unpriced models add tokens and no cost,
// cache savings are the reads at input price less read price, and each
// session adds up its own calls, most tokens first.
func TestReport(t *testing.T) {
	loc := time.FixedZone("UTC-5", -5*3600)
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, loc)
	at := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, loc).UTC() }
	recs := []Record{
		// 23:30 on the 6th locally is the 7th in UTC.
		{Time: at(6, 23).Add(30 * time.Minute), Provider: model.ProviderClaude, Model: "claude-opus-5-5", Session: "a", Tokens: Tokens{Input: 1e6, CacheRead: 1e6}},
		{Time: at(8, 1), Provider: model.ProviderClaude, Model: "claude-opus-5-5", Session: "b", Tokens: Tokens{Output: 1e6}},
		{Time: at(8, 2), Provider: model.ProviderCodex, Model: "gpt-6.1-sol", Session: "a", Tokens: Tokens{Input: 1e6}},
		{Time: at(7, 2), Provider: model.ProviderCodex, Model: "gpt-mystery", Session: "a", Tokens: Tokens{Input: 500, Output: 500}},
		{Time: at(5, 12), Provider: model.ProviderClaude, Model: "claude-opus-5-5", Session: "old", Tokens: Tokens{Output: 1e6}},
	}
	r := NewReport(recs, now, 3)
	if len(r.Days) != 3 || !r.Days[0].Equal(time.Date(2026, 10, 6, 0, 0, 0, 0, loc)) {
		t.Fatalf("days = %v", r.Days)
	}
	// Opus: 1M in at $4, 1M read at $0.20, 1M out at $20. Sol: 1M in at $2.
	if !near(r.Cost, 4+0.2+20+2) || !near(r.Savings, 4-0.2) {
		t.Errorf("cost %v, savings %v", r.Cost, r.Savings)
	}
	if r.Unpriced != 1000 || r.Sessions != 3 || r.Tokens.Total() != 4e6+1000 {
		t.Errorf("unpriced %d, sessions %d, tokens %d", r.Unpriced, r.Sessions, r.Tokens.Total())
	}
	if !near(r.Daily[0].Cost, 4.2) || r.Daily[1].Cost != 0 || r.Daily[1].Tokens.Total() != 1000 || !near(r.Daily[2].Cost, 22) {
		t.Errorf("daily = %+v", r.Daily)
	}
	if len(r.Providers) != 2 {
		t.Fatalf("providers = %+v", r.Providers)
	}
	cl, cx := r.Providers[0], r.Providers[1]
	if cl.Provider != model.ProviderClaude || cl.Sessions != 2 || !near(cl.Cost, 24.2) || !near(cl.Daily[0], 4.2) || !near(cl.Daily[2], 20) {
		t.Errorf("claude = %+v", cl)
	}
	if cx.Provider != model.ProviderCodex || cx.Sessions != 1 || !near(cx.Cost, 2) || cx.Daily[1] != 0 {
		t.Errorf("codex = %+v", cx)
	}
	if len(r.Models) != 3 || r.Models[0].Model != "claude-opus-5-5" || r.Models[1].Model != "gpt-6.1-sol" ||
		r.Models[2].Model != "gpt-mystery" || r.Models[2].Priced || !r.Models[0].Priced {
		t.Errorf("models = %+v", r.Models)
	}
	// Claude's a and Codex's a are two sessions; Codex's a used a priced and an unpriced model.
	if len(r.BySession) != 3 {
		t.Fatalf("sessions = %+v", r.BySession)
	}
	s0, s1, s2 := r.BySession[0], r.BySession[1], r.BySession[2]
	if s0.Provider != model.ProviderClaude || s0.Session != "a" || s0.Tokens.Total() != 2e6 || !near(s0.Cost, 4.2) {
		t.Errorf("first = %+v", s0)
	}
	if s1.Provider != model.ProviderCodex || s1.Session != "a" || s1.Tokens.Total() != 1e6+1000 || !near(s1.Cost, 2) || !s1.Last.Equal(at(8, 2)) {
		t.Errorf("second = %+v", s1)
	}
	if s2.Session != "b" || !near(s2.Cost, 20) {
		t.Errorf("third = %+v", s2)
	}
}
