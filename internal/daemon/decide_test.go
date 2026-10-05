package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	s := config.DecideSettings{Provider: "jev", Approvals: mode, Triage: true,
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

func hook(t *testing.T, d *Daemon, fixture string) {
	t.Helper()
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: hookFixture(t, fixture)}))
}

const gitPushRequest = `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"go test ./...","description":"Run the tests"}}`

func hookJSON(t *testing.T, d *Daemon, payload string) {
	t.Helper()
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: []byte(payload)}))
}

func TestSuggestShowsAdvice(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{
		"verdict": verdict(0.96, 0.03, 0.01),
		"urgency": urgency(2),
	}}
	d := decisionDaemon(t, m, config.ModeSuggest)
	hookJSON(t, d, gitPushRequest)
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
	if info.Provider != "jev" {
		t.Errorf("info %+v", info)
	}
	// The next event replaces the activity and its answers.
	hook(t, d, "claude_post_tool_use")
	if a := d.activityOf("a"); a.Advice != "" || a.Urgency != "" {
		t.Errorf("stale answers on %+v", a)
	}
}

func TestDecisionErrorsFallThrough(t *testing.T) {
	m := &fakeModel{err: errors.New("jev: no answer within the timeout")}
	d := decisionDaemon(t, m, config.ModeSuggest)
	hookJSON(t, d, gitPushRequest)
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

func TestQuestionsAreNeverAnswered(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.99, 0.01, 0)}}
	d := decisionDaemon(t, m, config.ModeSuggest)
	hook(t, d, "claude_permission_request_ask")
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
		return Decisions{Settings: config.DecideSettings{Provider: "", Approvals: config.ModeSuggest, Triage: true, TurnCheck: true}, Provider: m}
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

// A risky call keeps its flag next to the recommendation, even when the
// model says allow, and nothing is decided.
func TestAdviceFlagsRisk(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.99, 0.01, 0)}}
	d := decisionDaemon(t, m, config.ModeSuggest)
	hook(t, d, "claude_permission_request") // rm -rf node_modules
	a := waitActivity(t, d, "advice", func(a model.Activity) bool { return a.Advice != "" })
	if a.Advice != decide.Allow || a.AdviceRule != decide.RuleRmRf || a.State != model.StatePendingApproval {
		t.Errorf("activity %+v", a)
	}
}

// Approvals off asks nothing about permission requests.
func TestApprovalsOff(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"verdict": verdict(0.99, 0.01, 0), "urgency": urgency(1)}}
	d := decisionDaemon(t, m, config.ModeOff)
	hookJSON(t, d, gitPushRequest)
	time.Sleep(50 * time.Millisecond)
	for _, q := range m.questions() {
		if q == "verdict" {
			t.Error("asked for a recommendation with approvals off")
		}
	}
}

// The known key is scrubbed whichever provider answers: the loader hands
// it over for the command provider too.
func TestLoadDecisionsScrubsKeyForCommand(t *testing.T) {
	t.Setenv(decide.KeyEnv, "")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	cred := decide.CredentialsPath(dir)
	os.WriteFile(cfg, []byte("[decisions]\nprovider = \"command\"\ncommand = [\"my-classifier\"]\n"), 0o644)
	const key = "ts_live_abcdefghijklmnopqrstuvwxyz012345"
	if err := decide.SaveKey(cred, key); err != nil {
		t.Fatal(err)
	}
	x := loadDecisions(cfg, cred)()
	if _, ok := x.Provider.(decide.Command); !ok || !slices.Contains(x.Secrets, key) {
		t.Fatalf("command provider: %T, key scrubbed %v", x.Provider, slices.Contains(x.Secrets, key))
	}
	m := &fakeModel{answers: map[string]decide.Answer{"status": status(decide.ScreenWorking, 0.9)}}
	c := &decide.Client{P: m, Secrets: x.Secrets, Timeout: time.Second}
	if _, err := c.Ask(context.Background(), decide.FeatureAgents, "", decide.ScreenState("gemini", "$ echo "+key), decide.ScreenQuestions()); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(m.asked)
	if strings.Contains(string(b), key) {
		t.Error("the key reached the command provider")
	}
}

// When TYPESAFE_API_KEY overrides the saved key, both are scrubbed, for
// either provider.
func TestLoadDecisionsScrubsBothKeys(t *testing.T) {
	const saved, env = "ts_live_savedkey_abcdefghijklmnopqrstuv", "ts_live_envkey_abcdefghijklmnopqrstuvw"
	for _, provider := range []string{"jev", "command"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv(decide.KeyEnv, "")
			dir := t.TempDir()
			cfg := filepath.Join(dir, "config.toml")
			cred := decide.CredentialsPath(dir)
			os.WriteFile(cfg, []byte("[decisions]\nprovider = \""+provider+"\"\ncommand = [\"my-classifier\"]\n"), 0o644)
			if err := decide.SaveKey(cred, saved); err != nil {
				t.Fatal(err)
			}
			t.Setenv(decide.KeyEnv, env)
			x := loadDecisions(cfg, cred)()
			if !slices.Contains(x.Secrets, saved) || !slices.Contains(x.Secrets, env) {
				t.Errorf("secrets hold saved %v, env %v", slices.Contains(x.Secrets, saved), slices.Contains(x.Secrets, env))
			}
		})
	}
}
