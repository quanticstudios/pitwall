package decide

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// gateServer is a TypeSafe that answers each question with answer(key)
// and counts its calls; reply, when set, replaces the answer.
func gateServer(t *testing.T, g *Gates, answer func(string) float64, reply func(w http.ResponseWriter) bool) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		if reply != nil && reply(w) {
			return
		}
		var req struct {
			Model     string                     `json:"model"`
			Questions map[string]json.RawMessage `json:"questions"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != gateModel {
			t.Errorf("model %q", req.Model)
		}
		answers := map[string]any{}
		for k := range req.Questions {
			answers[k] = map[string]any{"noul": answer(k), "type": "noul"}
		}
		json.NewEncoder(w).Encode(map[string]any{"answers": answers, "model": gateModel, "usage": map[string]int{"input_tokens": 10, "output_tokens": 1}})
	}))
	t.Cleanup(srv.Close)
	g.url = srv.URL
	return &calls
}

func workspace(t *testing.T) string {
	root := gitRepo(t)
	write(t, filepath.Join(root, "AGENTS.md"), "Use tabs.\n")
	write(t, filepath.Join(root, "timeout.go"), "package x\n\nconst timeout = 30\n")
	return root
}

// TestPrepareConsent: a workspace on the consent list and a missing key
// each refuse with aide's message, before any call to Jev.
func TestPrepareConsent(t *testing.T) {
	root := workspace(t)
	g := &Gates{}
	calls := gateServer(t, g, func(string) float64 { return 0.5 }, nil)
	ctx := context.Background()
	if _, err := g.Prepare(ctx, root, "Check the timeout.", nil); err == nil || err.Error() != CredentialRequired {
		t.Errorf("no key: %v", err)
	}
	g.Set(AideJev{Key: "test-key", DisabledRoots: []string{root + string(filepath.Separator)}})
	if _, err := g.Prepare(ctx, root, "Check the timeout.", nil); err == nil || err.Error() != ConsentRequired {
		t.Errorf("consent refused: %v", err)
	}
	if err := g.Require(root); err == nil || err.Error() != ConsentRequired {
		t.Errorf("Require: %v", err)
	}
	if calls.Load() != 0 {
		t.Errorf("%d calls to Jev without consent", calls.Load())
	}
}

// TestPrepareGiven: with consent and a key the prompt comes back first,
// with the instruction file and the excerpts Jev chose; the same prompt
// within a minute reuses Jev's answer.
func TestPrepareGiven(t *testing.T) {
	root := workspace(t)
	g := &Gates{}
	calls := gateServer(t, g, func(string) float64 { return 0.9 }, nil)
	g.Set(AideJev{Key: "test-key"})
	assistant := "codex"
	history := []HistoryItem{{Provider: &assistant, Role: "assistant", Text: "Earlier fact: the timeout is 30 seconds."}}
	text, err := g.Prepare(context.Background(), root, "Check the timeout.", history)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Workspace evidence.", "Use tabs.", "const timeout = 30"} {
		if !strings.Contains(text, want) {
			t.Errorf("prepared text lacks %q:\n%s", want, text)
		}
	}
	if !strings.HasPrefix(text, "Check the timeout.\n\n") {
		t.Errorf("prepared text does not start with the prompt:\n%s", text)
	}
	if _, err := g.Prepare(context.Background(), root, "Check the timeout.", history); err != nil || calls.Load() != 1 {
		t.Errorf("second prepare: %d calls, %v", calls.Load(), err)
	}
}

// TestPrepareWithdrawn: consent withdrawn while Jev answers refuses the
// turn.
func TestPrepareWithdrawn(t *testing.T) {
	root := workspace(t)
	g := &Gates{}
	gateServer(t, g, func(string) float64 { return 0.5 }, func(http.ResponseWriter) bool {
		g.Set(AideJev{Key: "test-key", DisabledRoots: []string{root}})
		return false
	})
	g.Set(AideJev{Key: "test-key"})
	if _, err := g.Prepare(context.Background(), root, "Check the timeout.", nil); err == nil || err.Error() != ConsentRequired {
		t.Errorf("withdrawn: %v", err)
	}
}

// TestPreparePaused: a refusal from TypeSafe refuses the turn, and its
// Retry-After pauses the next one without a call.
func TestPreparePaused(t *testing.T) {
	root := workspace(t)
	g := &Gates{}
	calls := gateServer(t, g, nil, func(w http.ResponseWriter) bool {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		return true
	})
	g.Set(AideJev{Key: "test-key"})
	_, err := g.Prepare(context.Background(), root, "Check the timeout.", nil)
	if err == nil || err.Error() != "Jev paused this request: TypeSafe rate limited the request. Retry after correcting the problem." {
		t.Errorf("first: %v", err)
	}
	if _, err := g.Prepare(context.Background(), root, "Check the timeout.", nil); err == nil || !strings.Contains(err.Error(), "Retry in 6") {
		t.Errorf("second: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("%d calls, want 1", calls.Load())
	}
}

// TestToolGate: off by default; when on, allow under the allow bar, deny
// at the deny bar, prompt between, and prompt without consent, a key or
// an answer.
func TestToolGate(t *testing.T) {
	root := t.TempDir()
	g := &Gates{}
	p := 0.05
	calls := gateServer(t, g, func(string) float64 { return p }, nil)
	if s := g.ToolGate(root); s != DefaultToolGate || s.Enabled {
		t.Fatalf("default %+v", s)
	}
	on, low, high := true, 0.95, 0.5
	g.Set(AideJev{Key: "test-key", Global: GateOverrides{ToolGate: &ToolGateOverrides{Enabled: &on, AllowBelow: &low}},
		Workspaces: map[string]GateOverrides{root: {ToolGate: &ToolGateOverrides{DenyAbove: &high}}}})
	if s := g.ToolGate(root); !s.Enabled || s.AllowBelow != 0.5 || s.DenyAbove != 0.5 {
		t.Errorf("layers %+v, want on with the allow bar cut to the deny bar", g.ToolGate(root))
	}
	s := ToolGate{Enabled: true, AllowBelow: 0.1, DenyAbove: 0.9}
	r := ToolRequest{Cwd: root, Provider: "claude", RequestType: "command-execution-approval", Detail: "rm -rf build"}
	for _, tc := range []struct {
		p    float64
		want string
	}{{0.05, GateAllow}, {0.9, GateDeny}, {0.5, GatePrompt}} {
		p = tc.p
		if j := g.JudgeTool(context.Background(), r, s); j.Decision != tc.want || !strings.HasPrefix(j.Reason, "Jev "+tc.want+": ") {
			t.Errorf("p %v: %+v, want %s", tc.p, j, tc.want)
		}
	}
	if j := g.JudgeTool(context.Background(), r, s); j.Reason != "Jev prompt: destructive 0.50 (allow < 0.1, deny ≥ 0.9)." {
		t.Errorf("reason %q", j.Reason)
	}
	n := calls.Load()
	g.Set(AideJev{Key: "test-key", DisabledRoots: []string{root}})
	if j := g.JudgeTool(context.Background(), r, s); j.Decision != GatePrompt || calls.Load() != n {
		t.Errorf("without consent: %+v", j)
	}
	g.Set(AideJev{})
	if j := g.JudgeTool(context.Background(), r, s); j.Decision != GatePrompt || calls.Load() != n {
		t.Errorf("without a key: %+v", j)
	}
}

// TestGateStrictReply: a reply from another model or with an extra key
// is no answer.
func TestGateStrictReply(t *testing.T) {
	g := &Gates{}
	body := `{"answers":{},"model":"jev-latest","usage":{"input_tokens":1,"output_tokens":1}}`
	gateServer(t, g, nil, func(w http.ResponseWriter) bool { w.Write([]byte(body)); return true })
	g.Set(AideJev{Key: "test-key"})
	q := map[string]Question{"x": gateNoul("Q?", "y", "n")}
	if _, err := g.ask(context.Background(), "test-key", q, nil); err == nil || err.Error() != "TypeSafe returned an invalid response." {
		t.Errorf("other model: %v", err)
	}
	body = `{"answers":{"x":{"noul":0.2,"type":"noul","extra":1}},"model":"jev-1.13.0","usage":{"input_tokens":1,"output_tokens":1}}`
	if _, err := g.ask(context.Background(), "test-key", q, nil); err == nil {
		t.Error("extra key accepted")
	}
}

// TestFakeGates: the e2e's Jev answers 0.8 to every question.
func TestFakeGates(t *testing.T) {
	g := FakeGates()
	g.Set(AideJev{Key: "synthetic-e2e-key"})
	if j := g.JudgeTool(context.Background(), ToolRequest{Cwd: t.TempDir()}, DefaultToolGate); j.Decision != GatePrompt || !strings.Contains(j.Reason, "0.80") {
		t.Errorf("%+v", j)
	}
}
