package agent

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestExplainScreenFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/*.txt")
	if err != nil || len(files) == 0 {
		t.Fatalf("screen fixtures: %v, %v", files, err)
	}
	providers := append(slices.Clone(model.Agents), "")
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			g := screenFixture(t, filepath.Base(file))
			for _, p := range providers {
				trace := ExplainScreen(p, g)
				if trace.State != StateOf(p, g) {
					t.Errorf("%s: explained %q, detector %q", p, trace.State, StateOf(p, g))
				}
				if trace.Rule == "" || trace.Checks == nil {
					t.Fatalf("%s: incomplete trace: %+v", p, trace)
				}
				if trace.Matched != nil && (!trace.Matched.Matched || trace.Matched.Name != trace.Rule ||
					trace.Matched.Line == "" || !slices.Contains(trace.Checks, *trace.Matched)) {
					t.Errorf("%s: decision lacks matching evidence: %+v", p, trace)
				}
			}
		})
	}
}

func TestExplainScreenEvidence(t *testing.T) {
	for _, tc := range []struct {
		provider model.Provider
		text     string
		state    model.AgentState
		rule     string
		pattern  string
	}{
		{model.ProviderClaude, "✢ Nesting… (9s · ↓ 232 tokens)", model.StateWorking, "busy/spinner", spinner.String()},
		{model.ProviderCodex, "Do you want to proceed?\nEsc to cancel", model.StatePendingApproval, "form/esc to cancel", "esc to cancel"},
		{model.ProviderPi, "Would you like to proceed?\nEsc to cancel", model.StatePlanReady, "plan/would you like to proceed", "would you like to proceed"},
		{model.ProviderClaude, "Requests your input\nEsc to cancel", model.StateAwaitingInput, "input/requests your input", "requests your input"},
		{model.ProviderGemini, "Action required\nEsc to cancel", model.StatePendingApproval, "pending-approval/action required", "action required"},
		{model.ProviderOpenCode, "Permission required\nesc interrupt", model.StatePendingApproval, "pending-approval/permission required", "permission required"},
		{model.ProviderAider, "Add file? (Y)es/(N)o\n❯", model.StateAwaitingInput, "awaiting-input/(y)es/(n)o", "(y)es/(n)o"},
	} {
		t.Run(string(tc.provider)+"/"+tc.rule, func(t *testing.T) {
			e := vt.New(100, 30, nil)
			e.Write([]byte(strings.ReplaceAll(tc.text, "\n", "\r\n")))
			trace := ExplainScreen(tc.provider, e.Snapshot())
			if trace.State != tc.state || trace.Rule != tc.rule || trace.Matched == nil {
				t.Fatalf("got %+v", trace)
			}
			if trace.Matched.Pattern != tc.pattern || !strings.Contains(tc.text, trace.Matched.Line) {
				t.Fatalf("wrong evidence: %+v", trace.Matched)
			}
			if tc.provider != model.ProviderAider && !slices.ContainsFunc(trace.Checks, func(c ScreenCheck) bool { return !c.Matched }) {
				t.Fatal("missing unsuccessful checks")
			}
		})
	}
}

func TestExplainScreenFallback(t *testing.T) {
	g := screenFixture(t, "claude-idle.txt")
	trace := ExplainScreen(model.ProviderClaude, g)
	if trace.State != "" || trace.Rule != "no-screen-match" || trace.Matched != nil || len(trace.Checks) == 0 {
		t.Fatalf("idle: %+v", trace)
	}
	trace = ExplainScreen(model.ProviderAmp, g)
	if trace.State != "" || trace.Rule != "no-screen-rules" || trace.Matched != nil || len(trace.Checks) != 0 {
		t.Fatalf("unsupported provider: %+v", trace)
	}
}
