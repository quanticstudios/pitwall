package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"github.com/quanticstudios/pitwall/internal/decisionlog"
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

// yes is a yes/no answer with probability p of yes.
func yes(p float64) decide.Answer { return decide.Answer{Type: decide.Noul, Noul: p} }

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
		TurnCheck: true, TurnThreshold: 0.8, Check: true, Timeout: time.Second}
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
		"requested": yes(0.96), "irreversible": yes(0),
		"blocked": yes(0.2), "nothing": yes(0.1),
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
	m := &fakeModel{answers: map[string]decide.Answer{"requested": yes(0.99)}}
	d := decisionDaemon(t, m, config.ModeSuggest)
	hook(t, d, "claude_permission_request_ask")
	time.Sleep(50 * time.Millisecond)
	for _, q := range m.questions() {
		if q == "requested" {
			t.Error("asked a model to approve AskUserQuestion")
		}
	}
}

func TestTurnCheck(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"review": yes(0.9), "blocked": yes(0.9), "nothing": yes(0.1)}}
	d := decisionDaemon(t, m, config.ModeSuggest)
	hook(t, d, "claude_user_prompt_submit")
	hook(t, d, "claude_stop")
	a := waitActivity(t, d, "review", func(a model.Activity) bool { return a.Review && a.Urgency == "now" })
	if a.State != model.StateCompleted || model.PillLabel(a) != "Check" {
		t.Errorf("activity %+v, pill %q", a, model.PillLabel(a))
	}
	// The approval question carries the latest prompt.
	m.answers["requested"], m.answers["irreversible"] = yes(0.9), yes(0.1)
	hookJSON(t, d, gitPushRequest)
	waitActivity(t, d, "advice", func(a model.Activity) bool { return a.Advice != "" })
	m.mu.Lock()
	defer m.mu.Unlock()
	last := m.asked[len(m.asked)-1]
	for _, r := range m.asked {
		if _, ok := r.Questions["requested"]; ok {
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

// A call pitwall sees a risk in shows the risk at once, and the model is
// not asked about it.
func TestAdviceFlagsRisk(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"requested": yes(0.99), "irreversible": yes(0)}}
	d := decisionDaemon(t, m, config.ModeSuggest)
	hook(t, d, "claude_permission_request") // rm -rf node_modules
	if a := d.activityOf("a"); a.Advice != "" || a.AdviceRule != decide.RuleRmRf || a.State != model.StatePendingApproval {
		t.Errorf("activity %+v", a)
	}
	time.Sleep(50 * time.Millisecond)
	if slices.Contains(m.questions(), "requested") {
		t.Error("asked the model about a call the rules answered")
	}
}

// Approvals off asks nothing about permission requests.
func TestApprovalsOff(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"requested": yes(0.99), "blocked": yes(0.1)}}
	d := decisionDaemon(t, m, config.ModeOff)
	hookJSON(t, d, gitPushRequest)
	time.Sleep(50 * time.Millisecond)
	for _, q := range m.questions() {
		if q == "requested" {
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
	m := &fakeModel{answers: map[string]decide.Answer{"blocked": yes(0.9), "nothing": yes(0.1)}}
	c := &decide.Client{P: m, Secrets: x.Secrets, Timeout: time.Second}
	if _, err := c.Ask(context.Background(), decide.FeatureTriage, "", decide.TriageState("gemini", "awaiting-input", "$ echo "+key), decide.TriageQuestions()); err != nil {
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

// The daemon scrubs its known keys from a finished turn's whole message
// before the summary becomes Detail.
func TestDetailScrubsKnownKey(t *testing.T) {
	const key = "plainlookingkeyvalue42"
	m := &fakeModel{}
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w"}},
	}
	o := f.options()
	o.Derive = agent.Derive
	o.Decisions = func() Decisions {
		return Decisions{Settings: config.DecideSettings{Provider: "jev", Approvals: config.ModeOff}, Provider: m, Secrets: []string{key}}
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"hook_event_name": "Stop", "session_id": "s", "last_assistant_message": "Set the key to " + key + " for you."})
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: b}))
	if a := d.activityOf("a"); a.State != model.StateCompleted || strings.Contains(a.Detail, key) || !strings.Contains(a.Detail, "Set the key") {
		t.Errorf("Detail %q", a.Detail)
	}
}

// The turn check never sends screen text: not while the agent holds the
// foreground, and not when the Stop hook is handled after the agent has
// exited to the shell (whose group the hook then records).
func TestTurnCheckSendsNoScreen(t *testing.T) {
	for name, fg := range map[string]int64{"agent in the foreground": 100, "agent exited": 200, "no foreground": -1} {
		t.Run(name, func(t *testing.T) {
			m := &fakeModel{answers: map[string]decide.Answer{"review": {Type: decide.Noul, Noul: 0.1}}}
			f := &fakes{statsCalls: map[string]int{}}
			f.saved = model.State{
				Workspaces: []model.Workspace{{ID: "w", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
				Panes:      []model.Pane{{ID: "a", WorkspaceID: "w"}},
			}
			o := f.options()
			o.Derive = agent.Derive
			if fg >= 0 {
				start := o.StartPane
				o.StartPane = func(c pane.Config) (Pane, error) {
					p, _ := start(c)
					lp := &livePane{fakePane: p.(*fakePane)}
					lp.fgGroup.Store(fg)
					lp.show("SCREENTEXT shell prompt", false)
					return lp, nil
				}
			}
			o.Decisions = func() Decisions {
				return Decisions{Settings: config.DecideSettings{Provider: "jev", Approvals: config.ModeOff, TurnCheck: true, TurnThreshold: 0.8, Timeout: time.Second}, Provider: m}
			}
			d, err := NewWith(o)
			if err != nil {
				t.Fatal(err)
			}
			hook(t, d, "claude_stop")
			for start := time.Now(); len(m.questions()) == 0; time.Sleep(5 * time.Millisecond) {
				if time.Since(start) > 2*time.Second {
					t.Fatal("no turn check was asked")
				}
			}
			m.mu.Lock()
			b, _ := json.Marshal(m.asked)
			m.mu.Unlock()
			if strings.Contains(string(b), "SCREENTEXT") || strings.Contains(string(b), `"screen"`) {
				t.Errorf("the turn check sent screen text: %s", b)
			}
			if !strings.Contains(string(b), "Fixed the token check") {
				t.Errorf("the turn check lacks the last message: %s", b)
			}
		})
	}
}

// knownKeyDaemon is a daemon with one pane "a" whose decisions know key,
// and the vt package's emulator so OSC notifications reach it.
func knownKeyDaemon(t *testing.T, key string) *Daemon {
	t.Helper()
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w"}},
	}
	o := f.options()
	o.Derive, o.SessionID, o.NewVT = agent.Derive, agent.SessionID, vt.New
	o.Decisions = func() Decisions {
		return Decisions{Settings: config.DecideSettings{Provider: "jev", Approvals: config.ModeOff}, Provider: &fakeModel{}, Secrets: []string{key}}
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// A known key in the first prompt never names the tab, even where the
// title is cut.
func TestPromptTitleScrubsKnownKey(t *testing.T) {
	const key = "plainlookingkeyvalue42xyz"
	d := knownKeyDaemon(t, key)
	b, _ := json.Marshal(map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "s", "prompt": "use " + key + " for the deploy"})
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: b}))
	d.mu.Lock()
	st := d.snapshot()
	d.mu.Unlock()
	if p := st.Panes[0].Prompt; p == "" || strings.Contains(p, "plainlooking") || strings.Contains(st.Workspaces[0].Label, "plainlooking") {
		t.Errorf("prompt %q, tab %q", p, st.Workspaces[0].Label)
	}
}

// A known key in an OSC notification never reaches the activity that the
// sidebar and desktop notifications show.
func TestNoticeScrubsKnownKey(t *testing.T) {
	const key = "plainlookingkeyvalue42xyz"
	d := knownKeyDaemon(t, key)
	e := d.notifyingVT("a", d.o.NewVT)(40, 2, io.Discard)
	e.Write([]byte("\x1b]9;deployed with " + key + "\x07"))
	for start := time.Now(); ; time.Sleep(5 * time.Millisecond) {
		d.mu.Lock()
		acts := d.snapshot().Activities
		d.mu.Unlock()
		if len(acts) > 0 {
			if strings.Contains(acts[0].Detail, "plainlooking") || !strings.Contains(acts[0].Detail, "deployed with") {
				t.Errorf("notice detail %q", acts[0].Detail)
			}
			return
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("no notice")
		}
	}
}

const goTestRan = `{"session_id":"s1","transcript_path":"/t.jsonl","cwd":"/home/u/repo","hook_event_name":"PostToolUse","tool_name":"Bash",` +
	`"tool_input":{"command":"go test ./..."},"tool_response":{"stdout":"ok  \tx\t0.1s [no tests to run]","stderr":""}}`

// failedCheck reports whether pane a's latest check counts as failed.
func failedCheck(d *Daemon) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.dec.checks["a"]
	return c != nil && c.failed
}

// A check that exits 0 goes to the model, one that failed does not, and a
// turn that ends on a failed check reads Check whatever the turn check
// says. A new prompt starts the checks over.
func TestCheckMarksTurn(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"failed": yes(0.9), "review": yes(0.1)}}
	d := decisionDaemon(t, m, config.ModeOff)
	hook(t, d, "claude_user_prompt_submit")
	hookJSON(t, d, goTestRan)
	waitUntil(t, "check read", func() bool { return failedCheck(d) })
	m.mu.Lock()
	st, _ := m.asked[len(m.asked)-1].State.(map[string]any)
	m.mu.Unlock()
	if st["command"] != "go test ./..." || !strings.Contains(fmt.Sprint(st["output"]), "no tests to run") {
		t.Errorf("check state %v", st)
	}
	hook(t, d, "claude_stop")
	if a := d.activityOf("a"); !a.Review {
		t.Errorf("a turn ending on a failed check: %+v", a)
	}

	hook(t, d, "claude_user_prompt_submit")
	if failedCheck(d) {
		t.Fatal("a new turn kept the old check")
	}
	n := len(m.questions())
	hook(t, d, "claude_post_tool_use_failure") // npm test, exit 1
	hook(t, d, "claude_stop")
	if a := d.activityOf("a"); !a.Review {
		t.Errorf("a turn ending on a failing npm test: %+v", a)
	}
	if q := m.questions()[n:]; slices.Contains(q, "failed") {
		t.Errorf("asked about a check its exit code answered: %v", q)
	}

	m.mu.Lock()
	m.answers["failed"] = yes(0.1)
	m.mu.Unlock()
	hook(t, d, "claude_user_prompt_submit")
	hookJSON(t, d, goTestRan)
	waitUntil(t, "check read", func() bool { return slices.Contains(m.questions()[n:], "failed") })
	time.Sleep(20 * time.Millisecond)
	hook(t, d, "claude_stop")
	time.Sleep(50 * time.Millisecond)
	if a := d.activityOf("a"); a.Review {
		t.Errorf("a turn ending on a passing check: %+v", a)
	}
}

// An agent working 10 minutes without a state change is asked about,
// once per 10 minutes, from its own screen only, and Esc logs the outcome.
func TestStuck(t *testing.T) {
	d, lp, id := liveDaemon(t, model.StateWorking)
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	j, err := decisionlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeModel{answers: map[string]decide.Answer{"stuck": yes(0.9)}}
	d.mu.Lock()
	d.o.Journal = j
	d.dec.cur = Decisions{Provider: m, Settings: config.DecideSettings{Provider: "command", Stuck: true, StuckThreshold: 0.8, Timeout: time.Second}}
	d.mu.Unlock()
	lp.show("npm install: ETIMEDOUT, retrying (attempt 14)", false)
	polls()
	if q := m.questions(); len(q) > 0 {
		t.Fatalf("asked about an agent working for seconds: %v", q)
	}
	d.mu.Lock()
	i := d.activityIndex(id)
	d.st.Activities[i].UpdatedAt = time.Now().Add(-11 * time.Minute)
	since := d.st.Activities[i].UpdatedAt
	d.mu.Unlock()
	waitUntil(t, "stuck", func() bool { return d.activityOf(id).Stuck })
	polls()
	if n := len(m.questions()); n != 1 {
		t.Errorf("asked %d times within 10 minutes", n)
	}
	m.mu.Lock()
	st, _ := m.asked[0].State.(map[string]any)
	m.mu.Unlock()
	if !strings.Contains(fmt.Sprint(st["screen"]), "ETIMEDOUT") || fmt.Sprint(st["minutes"]) != "11" {
		t.Errorf("stuck state %v", st)
	}
	d.noteInput(id, []byte{0x03})
	// Another program in the foreground: its screen is never sent.
	d.mu.Lock()
	d.st.Activities[d.activityIndex(id)].UpdatedAt = since
	d.dec.stuck[id].asked = time.Time{}
	d.mu.Unlock()
	lp.fgGroup.Store(300)
	polls()
	if n := len(m.questions()); n != 1 {
		t.Errorf("asked about another program's screen: %d asks", n)
	}
	j.Close(time.Second)
	evs, _ := decisionlog.Read(path, time.Time{})
	var users []string
	for _, e := range evs {
		if e.Feature == decide.FeatureStuck && e.Kind == decisionlog.Outcome {
			users = append(users, e.User)
		}
	}
	if !slices.Equal(users, []string{decisionlog.Interrupted}) {
		t.Errorf("stuck outcomes %v", users)
	}
}
