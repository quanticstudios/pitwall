package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// fakeModel answers every question it has an answer for, by question id.
type fakeModel struct {
	mu      sync.Mutex
	asked   []decide.Request
	answers map[string]decide.Answer
	err     error
}

func (f *fakeModel) Ask(ctx context.Context, r decide.Request) (map[string]decide.Answer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, r)
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]decide.Answer{}
	for id := range r.Questions {
		if a, ok := f.answers[id]; ok {
			out[id] = a
		}
	}
	return out, nil
}

func (f *fakeModel) questions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.asked {
		for id := range r.Questions {
			out = append(out, id)
		}
	}
	return out
}

func verdict(allow, ask, deny float64) decide.Answer {
	c := decide.Allow
	if ask > allow && ask > deny {
		c = decide.Ask
	} else if deny > allow {
		c = decide.Deny
	}
	return decide.Answer{Type: decide.Choice, Choice: c, Confidence: 0.9, Probabilities: map[string]float64{decide.Allow: allow, decide.Ask: ask, decide.Deny: deny}}
}

func hookFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// decisionDaemon is a daemon with one Claude pane "a" in tab "w", whose
// repo is the fixtures' /home/u/repo, asking m in mode.
func decisionDaemon(t *testing.T, m *fakeModel, mode string) *Daemon {
	t.Helper()
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Label: "fix auth", Path: "/home/u/repo", RepoRoot: "/home/u/repo",
			Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes: []model.Pane{{ID: "a", WorkspaceID: "w", Cwd: "/home/u/repo"}},
	}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	s := config.DecideSettings{Provider: "jev", Approvals: mode, AllowAbove: 0.95, DenyAbove: 0.95, Triage: true,
		TurnCheck: true, TurnThreshold: 0.8, Timeout: time.Second}
	o.Decisions = func() Decisions { return Decisions{Settings: s, Provider: m} }
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func waitActivity(t *testing.T, d *Daemon, what string, ok func(model.Activity) bool) model.Activity {
	t.Helper()
	for start := time.Now(); time.Since(start) < 2*time.Second; time.Sleep(5 * time.Millisecond) {
		if a := d.activityOf("a"); ok(a) {
			return a
		}
	}
	t.Fatalf("%s: activity %+v", what, d.activityOf("a"))
	return model.Activity{}
}

func hook(t *testing.T, d *Daemon, fixture string) []byte {
	t.Helper()
	out, err := d.hookEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: hookFixture(t, fixture), Reply: true})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

const gitPushRequest = `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"go test ./...","description":"Run the tests"}}`

func hookJSON(t *testing.T, d *Daemon, payload string) []byte {
	t.Helper()
	out, err := d.hookEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(payload), Reply: true})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSuggestShowsAdvice(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{
		"verdict": verdict(0.96, 0.03, 0.01),
		"urgency": {Type: decide.Score, Score: 2, Confidence: 0.8},
	}}
	d := decisionDaemon(t, m, config.ModeSuggest)
	if out := hookJSON(t, d, gitPushRequest); out != nil {
		t.Fatalf("suggest mode decided: %s", out)
	}
	if a := d.activityOf("a"); a.State != model.StatePendingApproval {
		t.Fatalf("activity %+v", a)
	}
	a := waitActivity(t, d, "advice and urgency", func(a model.Activity) bool { return a.Advice != "" && a.Urgency != model.UrgencyPending })
	if a.Advice != decide.Allow || a.AdviceP != 0.96 || a.AdviceRule != "" || a.Urgency != "soon" || a.State != model.StatePendingApproval {
		t.Errorf("activity %+v", a)
	}
	d.mu.Lock()
	info := d.snapshot().Decide
	d.mu.Unlock()
	if info.Provider != "jev" || info.Auto || len(info.Audit) != 0 {
		t.Errorf("info %+v", info)
	}
	// The next event replaces the activity and its answers.
	hook(t, d, "claude_post_tool_use")
	if a := d.activityOf("a"); a.Advice != "" || a.Urgency != "" {
		t.Errorf("stale answers on %+v", a)
	}
}

func TestAutoApproves(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.97, 0.02, 0.01), "urgency": {Type: decide.Score, Score: 1}}}
	d := decisionDaemon(t, m, config.ModeAuto)
	out := hookJSON(t, d, gitPushRequest)
	if string(out) != string(agent.PermissionDecision("allow", "")) {
		t.Fatalf("output = %s", out)
	}
	if a := d.activityOf("a"); a.State != model.StateWorking || a.Urgency != "" {
		t.Errorf("an approved call should go on working: %+v", a)
	}
	d.mu.Lock()
	info := d.snapshot().Decide
	d.mu.Unlock()
	if !info.Auto || len(info.Audit) != 1 || info.AutoCount("w") != 1 {
		t.Fatalf("info %+v", info)
	}
	if e := info.Audit[0]; e.Tool != "Bash" || e.Input != "go test ./..." || e.Verdict != "allow" || e.Allow != 0.97 || e.Tab != "repo" || e.Agent != model.ProviderClaude {
		t.Errorf("audit %+v", e)
	}
	time.Sleep(50 * time.Millisecond)
	for _, q := range m.questions() {
		if q == "urgency" {
			t.Error("triage asked about an approval pitwall answered")
		}
	}
}

func TestAutoNeverApprovesHardRule(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.99, 0.01, 0)}}
	d := decisionDaemon(t, m, config.ModeAuto)
	if out := hook(t, d, "claude_permission_request"); out != nil { // rm -rf node_modules
		t.Fatalf("hard rule approved: %s", out)
	}
	a := waitActivity(t, d, "advice", func(a model.Activity) bool { return a.Advice != "" })
	if a.AdviceRule != decide.RuleRmRf || a.State != model.StatePendingApproval {
		t.Errorf("activity %+v", a)
	}
}

func TestAutoDenies(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.01, 0.02, 0.97)}}
	d := decisionDaemon(t, m, config.ModeAuto)
	out := hook(t, d, "claude_permission_request")
	if !strings.Contains(string(out), `"behavior":"deny"`) || !strings.Contains(string(out), "deny 97%") {
		t.Fatalf("output = %s", out)
	}
}

func TestAutoUnsureAsks(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.9, 0.08, 0.02)}}
	d := decisionDaemon(t, m, config.ModeAuto)
	if out := hookJSON(t, d, gitPushRequest); out != nil {
		t.Fatalf("below the threshold decided: %s", out)
	}
	if a := d.activityOf("a"); a.Advice != decide.Allow || a.AdviceP != 0.9 || a.State != model.StatePendingApproval {
		t.Errorf("activity %+v", a)
	}
}

func TestDecisionErrorsFallThrough(t *testing.T) {
	m := &fakeModel{err: errors.New("jev: no answer within the timeout")}
	d := decisionDaemon(t, m, config.ModeAuto)
	if out := hookJSON(t, d, gitPushRequest); out != nil {
		t.Fatalf("an error decided: %s", out)
	}
	if a := d.activityOf("a"); a.State != model.StatePendingApproval || a.Advice != "" {
		t.Errorf("activity %+v", a)
	}
	waitActivity(t, d, "triage gives up", func(a model.Activity) bool { return a.Urgency == "" })
	d.mu.Lock()
	info := d.snapshot().Decide
	d.mu.Unlock()
	errs := 0
	for _, c := range info.Counts {
		errs += c.Errors
	}
	if errs < 1 {
		t.Errorf("counts %+v", info.Counts)
	}
}

func TestTabAutoOff(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.99, 0.01, 0)}}
	d := decisionDaemon(t, m, config.ModeAuto)
	must(t, d.handle(context.Background(), proto.SetAutoApprove{WorkspaceID: "w", Off: true}))
	if out := hookJSON(t, d, gitPushRequest); out != nil {
		t.Fatalf("auto off for the tab decided: %s", out)
	}
	waitActivity(t, d, "advice still shows", func(a model.Activity) bool { return a.Advice == decide.Allow })
}

func TestQuestionsAreNeverAnswered(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.99, 0.01, 0)}}
	d := decisionDaemon(t, m, config.ModeAuto)
	if out := hook(t, d, "claude_permission_request_ask"); out != nil {
		t.Fatalf("answered AskUserQuestion: %s", out)
	}
	time.Sleep(50 * time.Millisecond)
	for _, q := range m.questions() {
		if q == "verdict" {
			t.Error("asked a model to approve AskUserQuestion")
		}
	}
}

func TestTurnCheck(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"review": {Type: decide.Noul, Noul: 0.9}, "urgency": {Type: decide.Score, Score: 3}}}
	d := decisionDaemon(t, m, config.ModeSuggest)
	hook(t, d, "claude_user_prompt_submit")
	hook(t, d, "claude_stop")
	a := waitActivity(t, d, "review", func(a model.Activity) bool { return a.Review && a.Urgency == "now" })
	if a.State != model.StateCompleted || model.PillLabel(a) != "Agent Check" {
		t.Errorf("activity %+v, pill %q", a, model.PillLabel(a))
	}
	// The approval question carries the latest prompt.
	m.answers["verdict"] = verdict(0.5, 0.4, 0.1)
	hookJSON(t, d, gitPushRequest)
	waitActivity(t, d, "advice", func(a model.Activity) bool { return a.Advice != "" })
	m.mu.Lock()
	defer m.mu.Unlock()
	last := m.asked[len(m.asked)-1]
	for _, r := range m.asked {
		if _, ok := r.Questions["verdict"]; ok {
			last = r
		}
	}
	st, _ := last.State.(map[string]any)
	if p, _ := st["user_prompt"].(string); p == "" {
		t.Errorf("approval state has no prompt: %v", last.State)
	}
}

func TestNoProviderAsksNothing(t *testing.T) {
	m := &fakeModel{}
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w"}},
	}
	o := f.options()
	o.Derive = agent.Derive
	o.Decisions = func() Decisions {
		return Decisions{Settings: config.DecideSettings{Provider: "", Approvals: config.ModeAuto, Triage: true, TurnCheck: true}, Provider: m}
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	hookJSON(t, d, gitPushRequest)
	hook(t, d, "claude_stop")
	time.Sleep(50 * time.Millisecond)
	if q := m.questions(); len(q) > 0 {
		t.Errorf("asked %v with no provider set", q)
	}
}

// TestHookReplyOverSocket runs a permission request through the socket
// the way `pitwall hook` sends it.
func TestHookReplyOverSocket(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.97, 0.02, 0.01)}}
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Path: "/home/u/repo", RepoRoot: "/home/u/repo", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w"}},
	}
	sock, stop := runWith(t, f, func(o *Options) {
		o.Derive = agent.Derive
		o.Decisions = func() Decisions {
			return Decisions{Settings: config.DecideSettings{Provider: "jev", Approvals: config.ModeAuto, AllowAbove: 0.95, DenyAbove: 0.95, Timeout: time.Second}, Provider: m}
		}
	})
	defer stop()
	c := dial(t, sock, "hook")
	c.send(proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(gitPushRequest), Reply: true})
	r := c.waitFor("hook reply", func(m any) bool { _, ok := m.(proto.HookReply); return ok }).(proto.HookReply)
	if !strings.Contains(string(r.Output), `"behavior":"allow"`) {
		t.Errorf("reply = %s", r.Output)
	}
}
