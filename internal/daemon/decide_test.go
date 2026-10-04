package daemon

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
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

// urgency is a triage answer of level n with a full distribution.
func urgency(n int) decide.Answer {
	p := map[string]float64{"0": 0, "1": 0, "2": 0, "3": 0}
	p[fmt.Sprint(n)] = 1
	return decide.Answer{Type: decide.Score, Score: float64(n), Confidence: 0.9, Probabilities: p}
}

// status is a screen answer with confidence conf on choice.
func status(choice string, conf float64) decide.Answer {
	p := map[string]float64{}
	for _, o := range []string{decide.ScreenWorking, decide.ScreenWaiting, decide.ScreenApproval, decide.ScreenDone, decide.ScreenIdle} {
		p[o] = 0
	}
	p[choice] = 1
	return decide.Answer{Type: decide.Choice, Choice: choice, Confidence: conf, Probabilities: p}
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
		"urgency": urgency(2),
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
	m := &fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.97, 0.02, 0.01), "urgency": urgency(1)}}
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
	m := &fakeModel{answers: map[string]decide.Answer{"review": {Type: decide.Noul, Noul: 0.9}, "urgency": urgency(3)}}
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

func TestScreenReadDue(t *testing.T) {
	defer func(d time.Duration) { screenEvery = d }(screenEvery)
	screenEvery = 2 * time.Second
	a := []vt.Cell{{Content: "a"}}
	b := []vt.Cell{{Content: "b"}}
	t0 := time.Unix(1000, 0)
	r, p := &screenRead{}, &screenPace{}
	if !r.due(p, a, t0) {
		t.Fatal("first screen not due")
	}
	p.last, r.cells = t0, a
	if r.due(p, b, t0.Add(time.Second)) {
		t.Error("due again within 2 s")
	}
	if r.due(p, a, t0.Add(3*time.Second)) {
		t.Error("an unchanged screen is due")
	}
	if !r.due(p, b, t0.Add(2*time.Second)) {
		t.Error("a changed screen after 2 s is not due")
	}
	// Another program in the same pane starts a new record but keeps the
	// pane's pace: still not due within 2 s of the last send.
	if (&screenRead{agent: "other"}).due(p, b, t0.Add(time.Second)) {
		t.Error("a program change reset the pane's pace")
	}
	p.busy = true
	if r.due(p, b, t0.Add(5*time.Second)) {
		t.Error("due while a read is on its way")
	}
}

// TestScreenReading runs the agents feature on the liveness poll: only a
// listed program is read, at most once per screenEvery while its screen
// changes, and a sure answer sets the activity.
func TestScreenReading(t *testing.T) {
	old := screenEvery
	t.Cleanup(func() { screenEvery = old }) // after openLive's cleanup stops the poll
	screenEvery = 100 * time.Millisecond
	d, lp, id := openLive(t, 500) // "sleep", not listed
	m := &fakeModel{answers: map[string]decide.Answer{"status": status(decide.ScreenWaiting, 0.9)}}
	d.mu.Lock()
	d.dec.cur = Decisions{Provider: m, Settings: config.DecideSettings{Provider: "command", Agents: true, AgentThreshold: 0.8,
		Programs: config.HooklessAgents, Timeout: time.Second}}
	d.mu.Unlock()
	lp.show("some build output", false)
	polls()
	if q := m.questions(); len(q) > 0 {
		t.Fatalf("read the screen of an unlisted program: %v", q)
	}
	if s := d.stateOf(id); s != model.StateTerminalRunning {
		t.Fatalf("unlisted program: %q", s)
	}

	lp.fgGroup.Store(700) // gemini
	waitUntil(t, "awaiting input", func() bool { return d.stateOf(id) == model.StateAwaitingInput })
	if a := d.activityOf(id); a.Provider != "gemini" {
		t.Errorf("activity %+v", a)
	}
	// A screen that keeps changing is read at most once per screenEvery.
	n0 := len(m.questions())
	stop := time.Now().Add(500 * time.Millisecond)
	for i := 0; time.Now().Before(stop); i++ {
		lp.show(fmt.Sprintf("Gemini thinking %d", i), false)
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(m.questions()) - n0; n < 2 || n > 7 {
		t.Errorf("%d reads in 500 ms of changes, want 2 to 7", n)
	}
	// A still screen is not read again.
	polls()
	n1 := len(m.questions())
	polls()
	polls()
	if n := len(m.questions()); n != n1 {
		t.Errorf("a still screen was read %d more times", n-n1)
	}
	// An unsure answer changes nothing.
	m.mu.Lock()
	m.answers["status"] = status(decide.ScreenWorking, 0.5)
	m.mu.Unlock()
	lp.show("Gemini maybe working", false)
	time.Sleep(3 * screenEvery)
	if s := d.stateOf(id); s != model.StateAwaitingInput {
		t.Errorf("an unsure answer changed the state to %q", s)
	}
	// Back at the shell, the activity and the record go.
	lp.fgGroup.Store(200)
	waitUntil(t, "cleared", func() bool { return d.stateOf(id) == "" })
	d.mu.Lock()
	_, kept := d.dec.screens[id]
	d.mu.Unlock()
	if kept {
		t.Error("screen record kept after the program left")
	}
}

// gateModel holds every answer until release is closed.
type gateModel struct {
	fakeModel
	asked   chan struct{}
	release chan struct{}
}

func (g *gateModel) Ask(ctx context.Context, r decide.Request) (map[string]decide.Answer, error) {
	select {
	case g.asked <- struct{}{}:
	default:
	}
	<-g.release
	return g.fakeModel.Ask(ctx, r)
}

// An answer that comes back after the tab turned auto off, the settings
// changed, or the approval went away decides nothing.
func TestAutoRevalidatesBeforeAnswering(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, d *Daemon){
		"tab auto off": func(t *testing.T, d *Daemon) {
			must(t, d.handle(context.Background(), proto.SetAutoApprove{WorkspaceID: "w", Off: true}))
		},
		"mode changed": func(t *testing.T, d *Daemon) {
			x := d.o.Decisions()
			x.Settings.Approvals = config.ModeSuggest
			d.o.Decisions = func() Decisions { return x }
			d.refreshDecisions()
		},
		"key changed": func(t *testing.T, d *Daemon) {
			x := d.o.Decisions()
			x.Secrets = []string{"another-key-123"}
			d.o.Decisions = func() Decisions { return x }
			d.refreshDecisions()
		},
		"approval answered": func(t *testing.T, d *Daemon) { hook(t, d, "claude_post_tool_use") },
		// The files changed but no poll has run yet: approve reads them again.
		"mode changed, not polled": func(t *testing.T, d *Daemon) {
			x := d.o.Decisions()
			x.Settings.Approvals = config.ModeSuggest
			d.o.Decisions = func() Decisions { return x }
		},
		"disconnected, not polled": func(t *testing.T, d *Daemon) {
			x := d.o.Decisions()
			x.Provider, x.Secrets = nil, nil
			d.o.Decisions = func() Decisions { return x }
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := &gateModel{fakeModel: fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.99, 0.01, 0)}},
				asked: make(chan struct{}, 1), release: make(chan struct{})}
			d := decisionDaemon(t, &g.fakeModel, config.ModeAuto)
			x := d.o.Decisions()
			x.Provider = g
			d.o.Decisions = func() Decisions { return x }
			d.mu.Lock()
			d.dec.cur = x
			d.mu.Unlock()
			out := make(chan []byte, 1)
			go func() { out <- hookJSON(t, d, gitPushRequest) }()
			<-g.asked
			change(t, d)
			close(g.release)
			if o := <-out; o != nil {
				t.Errorf("decided after the change: %s", o)
			}
		})
	}
}

// Auto mode fails closed: a call pitwall cannot fully check, one too long
// to send whole, and an inconsistent answer all get the normal prompt.
func TestAutoFailsClosed(t *testing.T) {
	for name, tc := range map[string]struct {
		payload string
		answer  decide.Answer
		label   string
	}{
		"pipe": {`{"session_id":"s1","transcript_path":"/t","cwd":"/home/u/repo","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"go test ./... | tee out"}}`,
			verdict(0.99, 0.01, 0), "shell syntax '|'"},
		"unknown tool": {`{"session_id":"s1","transcript_path":"/t","cwd":"/home/u/repo","hook_event_name":"PermissionRequest","tool_name":"WebFetch","tool_input":{"url":"https://example.com"}}`,
			verdict(0.99, 0.01, 0), "not on the allowlist: WebFetch"},
		"too long": {`{"session_id":"s1","transcript_path":"/t","cwd":"/home/u/repo","hook_event_name":"PermissionRequest","tool_name":"Write","tool_input":{"file_path":"/home/u/repo/a.txt","content":"` + strings.Repeat("data ", 20000) + `"}}`,
			verdict(0.99, 0.01, 0), "input too long to check"},
		"inconsistent": {gitPushRequest,
			decide.Answer{Type: decide.Choice, Choice: decide.Deny, Confidence: 1, Probabilities: map[string]float64{decide.Allow: 1, decide.Ask: 0, decide.Deny: 0}}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			m := &fakeModel{answers: map[string]decide.Answer{"verdict": tc.answer}}
			d := decisionDaemon(t, m, config.ModeAuto)
			if out := hookJSON(t, d, tc.payload); out != nil {
				t.Fatalf("decided: %s", out)
			}
			if a := d.activityOf("a"); a.AdviceRule != tc.label {
				t.Errorf("advice rule %q, want %q", a.AdviceRule, tc.label)
			}
		})
	}
}

// A screen captured while its program exits is not sent under its name.
func TestScreenRecheckedAfterCapture(t *testing.T) {
	old := screenEvery
	t.Cleanup(func() { screenEvery = old; commGone.Store(false) })
	screenEvery = 50 * time.Millisecond
	commGone.Store(true)
	d, lp, _ := openLive(t, 700)
	m := &fakeModel{answers: map[string]decide.Answer{"status": status(decide.ScreenWaiting, 0.9)}}
	d.mu.Lock()
	d.dec.cur = Decisions{Provider: m, Settings: config.DecideSettings{Provider: "command", Agents: true, AgentThreshold: 0.8,
		Programs: config.HooklessAgents, Timeout: time.Second}}
	d.mu.Unlock()
	lp.show("$ cat secrets.txt", false)
	polls()
	if q := m.questions(); len(q) > 0 {
		t.Errorf("sent a screen after the program left: %v", q)
	}
}

// Switching between agent CLIs in a pane does not send more often.
func TestScreenPaceAcrossPrograms(t *testing.T) {
	old := screenEvery
	t.Cleanup(func() { screenEvery = old })
	screenEvery = 200 * time.Millisecond
	d, lp, _ := openLive(t, 700)
	m := &fakeModel{answers: map[string]decide.Answer{"status": status(decide.ScreenWorking, 0.9)}}
	d.mu.Lock()
	d.dec.cur = Decisions{Provider: m, Settings: config.DecideSettings{Provider: "command", Agents: true, AgentThreshold: 0.8,
		Programs: config.HooklessAgents, Timeout: time.Second}}
	d.mu.Unlock()
	start := time.Now()
	for i := 0; time.Since(start) < 600*time.Millisecond; i++ {
		lp.fgGroup.Store(int64(700 + i%2))
		lp.show(fmt.Sprint("screen ", i), false)
		time.Sleep(15 * time.Millisecond)
	}
	if n := len(m.questions()); n > 4 {
		t.Errorf("%d reads in 600 ms at one per 200 ms", n)
	}
}

// A turn check whose agent left the foreground between the hook and the
// capture sends nothing: the screen would be the shell's.
func TestTurnCheckAfterAgentExits(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"review": {Type: decide.Noul, Noul: 0.9}, "urgency": urgency(1)}}
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Path: "/home/u/repo", RepoRoot: "/home/u/repo", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w", Cwd: "/home/u/repo"}},
	}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	var lp *livePane
	start := o.StartPane
	o.StartPane = func(c pane.Config) (Pane, error) {
		p, _ := start(c)
		lp = &livePane{fakePane: p.(*fakePane)}
		lp.fgGroup.Store(100) // the agent
		return lp, nil
	}
	o.Decisions = func() Decisions {
		return Decisions{Settings: config.DecideSettings{Provider: "jev", TurnCheck: true, TurnThreshold: 0.8, Timeout: time.Second}, Provider: m}
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	defer func(f func()) { turnCapture = f }(turnCapture)
	turnCapture = func() {
		lp.fgGroup.Store(200) // the agent exited to the shell
		close(done)
	}
	hook(t, d, "claude_stop")
	<-done
	time.Sleep(50 * time.Millisecond)
	for _, q := range m.questions() {
		if q == "review" {
			t.Error("sent a turn check after the agent left")
		}
	}
}
