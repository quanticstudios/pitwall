package web

import (
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestReadUsage: welcome lists readUsage, the answer passes the schema and
// carries the report's numbers, a reset that is unknown is left out, and a
// window out of range is invalid.
func TestReadUsage(t *testing.T) {
	s, hs := newServer(t, fixed(goodToken))
	now := time.Now()
	resets := now.Add(time.Hour).Truncate(time.Second)
	// why: Usage runs on the server's goroutine; a reply read off the socket
	// does not order it before the test, so since comes over a channel.
	sinces := make(chan time.Time, 2)
	s.Usage = func(t time.Time) Usage {
		sinces <- t
		return Usage{
			Limits: []flow.Limit{{Provider: model.ProviderClaude, Seen: now, Windows: []flow.Window{
				{Minutes: 300, Used: 42, Resets: resets}, {Minutes: 10080, Used: 18.5}}}},
			Records: []flow.Record{
				{Time: now, Provider: model.ProviderClaude, Model: "claude-opus-5-5", Session: "a", Tokens: flow.Tokens{Output: 1e6}},
				{Time: now, Provider: model.ProviderCodex, Model: "gpt-mystery", Session: "b", Tokens: flow.Tokens{Input: 500}},
			},
			ShowCost: true,
		}
	}
	c, _ := dial(t, hs, "", "")
	c.send(t, hi(goodToken, Protocol))
	if w := c.json(t); !slices.Contains(w["capabilities"].([]any), any("readUsage")) {
		t.Fatalf("welcome %v", w)
	}
	var v viewer
	c.send(t, map[string]any{"type": "readUsage", "id": "1", "days": 7})
	u := expectResult(t, &v, c, "1", true)["data"].(map[string]any)["usage"].(map[string]any)
	if since := <-sinces; !since.Equal(flow.Since(now, 7)) {
		t.Errorf("since %v, want %v", since, flow.Since(now, 7))
	}
	if u["showCost"] != true || u["pricesAsOf"] != flow.PricesAsOf {
		t.Errorf("usage %v", u)
	}
	days := u["days"].([]any)
	if len(days) != 7 || days[6].(map[string]any)["date"] != now.Format(time.DateOnly) || days[6].(map[string]any)["tokens"] != 1e6+500.0 {
		t.Errorf("days %v", days)
	}
	if tot := u["total"].(map[string]any); tot["sessions"] != 2.0 || tot["tokens"] != 1e6+500.0 || tot["cost"] != 20.0 || tot["unpriced"] != 500.0 {
		t.Errorf("total %v", tot)
	}
	if ss := u["sessions"].([]any); len(ss) != 2 || ss[0].(map[string]any)["sessionId"] != "a" || ss[0].(map[string]any)["cost"] != 20.0 {
		t.Errorf("sessions %v", ss)
	}
	ws := u["limits"].([]any)[0].(map[string]any)["windows"].([]any)
	if r := ws[0].(map[string]any)["resets"]; r != resets.UTC().Format(time.RFC3339) {
		t.Errorf("resets %v, want %v", r, resets.UTC().Format(time.RFC3339))
	}
	if _, ok := ws[1].(map[string]any)["resets"]; ok {
		t.Errorf("unknown reset sent: %v", ws[1])
	}

	c.send(t, map[string]any{"type": "readUsage", "id": "2"})
	if d := expectResult(t, &v, c, "2", true)["data"].(map[string]any)["usage"].(map[string]any)["days"].([]any); len(d) != 30 {
		t.Errorf("default window: %d days", len(d))
	}
	for _, days := range []any{0, 91, 2.5, "7"} {
		c.send(t, map[string]any{"type": "readUsage", "id": "3", "days": days})
		if r := expectResult(t, &v, c, "3", false); r["code"] != "invalid" {
			t.Errorf("days %v: %v", days, r)
		}
	}
}

// TestNoUsage: a server without Usage neither lists nor serves readUsage.
func TestNoUsage(t *testing.T) {
	_, hs := newServer(t, fixed(goodToken))
	c := greet(t, hs)
	c.send(t, map[string]any{"type": "readUsage", "id": "1"})
	if r := c.json(t); r["ok"] != false || r["code"] != "unsupported" {
		t.Fatalf("result %v", r)
	}
}
