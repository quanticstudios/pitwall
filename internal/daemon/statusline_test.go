package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

func TestStatuslineFresh(t *testing.T) {
	at := time.Now()
	rec := statuslineRec{agent.StatuslineState{SessionID: "s1", Model: "m"}, at}
	for _, c := range []struct {
		name    string
		session string
		hookAt  time.Time
		want    bool
	}{
		{"no hooks yet", "s1", time.Time{}, true},
		{"idle for a day", "s1", at, true},
		{"working, report just before", "s1", at.Add(time.Minute), true},
		{"hooks for the stale limit", "s1", at.Add(statuslineStale), true},
		{"hooks past the limit with no report", "s1", at.Add(statuslineStale + time.Second), false},
		{"another session", "s2", at, false},
		{"pane session unknown", "", at, true},
	} {
		if got := statuslineFresh(rec, c.session, c.hookAt); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if !statuslineFresh(statuslineRec{agent.StatuslineState{Model: "m"}, at}, "s2", at) {
		t.Error("a report with no session id is rejected for a pane with one")
	}
}

// reportStatusline sends pane a's statusline report the way pitwall
// statusline does.
func reportStatusline(t *testing.T, d *Daemon, st agent.StatuslineState) {
	t.Helper()
	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: "report", Payload: agent.EncodeStatusline(st)}); err != nil {
		t.Fatal(err)
	}
}

// The phone tab shows the statusline's model and effort over the screen's,
// falls back to the screen when the report goes stale, and an old daemon's
// way, an agent report with a name, still works beside it.
func TestStatuslineBeatsScreen(t *testing.T) {
	d, f, _ := phoneDaemon(t)
	setScreen(f.pane(0), []string{"────────", "❯ ", "────────", "Sonnet 5.5 · low · project"})
	if tab := d.phoneTabs()[0]; tab.Model != "Sonnet 5.5" || tab.Effort != "low" {
		t.Fatalf("screen only: %+v", tab)
	}
	d.mu.Lock()
	d.st.Panes[0].Provider = model.ProviderClaude
	d.mu.Unlock()
	reportStatusline(t, d, agent.StatuslineState{SessionID: "sess-1", Model: "claude-opus-5-5", ModelName: "Opus 5.5", Effort: "xhigh", ContextSize: 1_000_000})
	if tab := d.phoneTabs()[0]; tab.Model != "Opus 5.5" || tab.Effort != "xhigh" {
		t.Fatalf("statusline over screen: %+v", tab)
	}
	// An effort the report lacks keeps the screen's.
	reportStatusline(t, d, agent.StatuslineState{SessionID: "sess-1", Model: "claude-opus-5-5"})
	if tab := d.phoneTabs()[0]; tab.Model != "claude-opus-5-5" || tab.Effort != "low" {
		t.Fatalf("sparse report: %+v", tab)
	}
	reportStatusline(t, d, agent.StatuslineState{SessionID: "sess-1", ModelName: "Opus 5.5", Effort: "xhigh"})

	// Stale: the pane's hooks go on for longer than statuslineStale after the report.
	d.mu.Lock()
	rec := d.statuslines["a"]
	rec.at = time.Now().Add(-2 * statuslineStale)
	d.statuslines["a"] = rec
	d.mu.Unlock()
	event(t, d, "a", "working")
	if tab := d.phoneTabs()[0]; tab.Model != "Sonnet 5.5" || tab.Effort != "low" {
		t.Fatalf("stale report still wins: %+v", tab)
	}
	// Another session's report never applies.
	d.mu.Lock()
	d.statuslines["a"] = statuslineRec{agent.StatuslineState{SessionID: "old", ModelName: "Opus 5.5"}, time.Now()}
	d.st.Panes[0].SessionID = "new"
	d.mu.Unlock()
	if tab := d.phoneTabs()[0]; tab.Model != "Sonnet 5.5" {
		t.Fatalf("another session's report applies: %+v", tab)
	}
	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: "report", Payload: []byte(`{"name":"x","statusline":{"model":"m"}}`)}); err != nil {
		t.Fatal(err)
	}
	if rec := d.statuslines["a"]; rec.ModelName != "Opus 5.5" {
		t.Errorf("a named report was taken as a statusline: %+v", rec)
	}
}

// A pane chat's model, window and context come from the statusline, and
// the estimate returns when it goes stale.
func TestPaneChatStatusline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := `{"type":"user","uuid":"p1","timestamp":"2026-11-19T10:00:00Z","message":{"role":"user","content":"hi"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-11-19T10:00:01Z","message":{"id":"m1","model":"claude-opus-4-6","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":2,"cache_creation_input_tokens":4000,"cache_read_input_tokens":36000,"output_tokens":90}}}
`
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	pc := &paneChat{id: "a", path: path, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "a"}}
	sl := statuslineRec{StatuslineState: agent.StatuslineState{ModelName: "Opus 4.6", ContextSize: 500_000, ContextUsed: 41_000}}
	pc.catchUp(paneLive{p: &fakePane{}, tui: phoneSettings{model: "Opus 4.6"}, sl: sl})
	u := pc.snap.Usage
	if u == nil || u.ContextTokens == nil || *u.ContextTokens != 40002 || u.MaxTokens == nil || *u.MaxTokens != 500_000 {
		t.Fatalf("usage %+v: want 40002 of the statusline's 500K, not the estimated 200K", u)
	}
	pc.catchUp(paneLive{p: &fakePane{}, tui: phoneSettings{model: "Opus 4.6"}})
	if u := pc.snap.Usage; *u.MaxTokens != 200_000 {
		t.Errorf("window %d without a good statusline: want the 200K estimate", *u.MaxTokens)
	}

	// Before any transcript line, the statusline's count stands in for it.
	empty := filepath.Join(t.TempDir(), "e.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	pe := &paneChat{id: "b", path: empty, tr: claude.NewTranscript(), snap: session.Snapshot{SessionID: "b"}}
	pe.catchUp(paneLive{p: &fakePane{}, tui: phoneSettings{model: "Opus 5.5"}, sl: sl})
	if u := pe.snap.Usage; u == nil || u.ContextTokens == nil || *u.ContextTokens != 41_000 || *u.MaxTokens != 500_000 {
		t.Fatalf("usage %+v: want the statusline's 41000 of 500K", u)
	}
}
