package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Decisions is what the daemon needs to ask a decision model: the
// settings, the provider (nil when none is set up or usable) and exact
// strings to scrub from every state, such as the API key.
type Decisions struct {
	Settings config.DecideSettings
	Provider decide.Provider
	Secrets  []string
}

// on reports whether a provider can answer.
func (x Decisions) on() bool { return x.Provider != nil && x.Settings.On() }

func (x Decisions) name() string {
	if !x.on() {
		return ""
	}
	return x.Settings.Provider
}

// decisions is the daemon's decision state. Guarded by d.mu, except counts
// and limit, which lock themselves.
type decisions struct {
	cur     Decisions
	counts  decide.Counters
	limit   decide.Limiter
	prompts map[string]string // pane: the latest user prompt
	screens map[string]*screenRead
	paces   map[string]*screenPace // pane: kept while the pane lives, across programs
}

// loadDecisions reads [decisions] from the config at cfgPath and the key
// from credPath (or $TYPESAFE_API_KEY), again only when either file
// changed. Every key pitwall can see, from the environment and from the
// file, is scrubbed from every state whichever provider answers: a
// screen or an error may show it.
func loadDecisions(cfgPath, credPath string) func() Decisions {
	var mu sync.Mutex
	var stamp string
	var last Decisions
	return func() Decisions {
		mu.Lock()
		defer mu.Unlock()
		st := fileStamp(cfgPath) + "|" + fileStamp(credPath)
		if st == stamp {
			return last
		}
		stamp = st
		s := config.LoadDecisions(cfgPath)
		last = Decisions{Settings: s}
		key, _, err := decide.LoadKey(credPath)
		if err != nil && s.Provider == "jev" {
			log.Printf("pitwall: decisions: %v", err) // never holds the key
		}
		last.Secrets = decide.KnownKeys(credPath)
		switch s.Provider {
		case "jev":
			if key != "" {
				last.Provider = decide.NewJev(key, s.Model)
			}
		case "command":
			last.Provider = decide.Command{Argv: s.Command}
		}
		return last
	}
}

func fileStamp(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fmt.Sprint(fi.ModTime().UnixNano(), fi.Size(), fi.Mode())
}

// refreshDecisions rereads the decision config, on the liveness poll.
func (d *Daemon) refreshDecisions() {
	if d.o.Decisions == nil {
		return
	}
	x := d.o.Decisions()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.applyDecisions(x)
}

// applyDecisions makes x current, pushing state when anything changed.
// Callers hold d.mu.
func (d *Daemon) applyDecisions(x Decisions) {
	old := d.dec.cur
	d.dec.cur = x
	if old.name() != x.name() || !reflect.DeepEqual(old.Settings, x.Settings) || !slices.Equal(old.Secrets, x.Secrets) {
		d.changed()
	}
}

// client is a Client for one call. Callers hold d.mu.
func (d *Daemon) client() *decide.Client {
	x := d.dec.cur
	return &decide.Client{P: x.Provider, Timeout: x.Settings.Timeout, Counts: &d.dec.counts, Limit: &d.dec.limit, Secrets: x.Secrets}
}

// forgetDecisions drops a closed pane's records. Callers hold d.mu.
func (d *Daemon) forgetDecisions(id string) {
	delete(d.dec.prompts, id)
	delete(d.dec.screens, id)
	delete(d.dec.paces, id)
	d.dec.limit.Forget(id)
}

// decideInfo is the status clients show. Callers hold d.mu.
func (d *Daemon) decideInfo() model.DecideInfo {
	x := d.dec.cur
	info := model.DecideInfo{Provider: x.name()}
	for f, n := range d.dec.counts.Today(time.Now()) {
		info.Counts = append(info.Counts, model.DecideCount{Feature: f, Calls: n.Calls, Errors: n.Errors})
	}
	slices.SortFunc(info.Counts, func(a, b model.DecideCount) int {
		if a.Feature < b.Feature {
			return -1
		}
		return 1
	})
	return info
}

// clearDecisions drops answers that belonged to an activity's old state.
func clearDecisions(a *model.Activity) {
	a.Advice, a.AdviceP, a.AdviceRule, a.Urgency, a.Review = "", 0, "", "", false
}

// decideJob is the questions one hook event calls for, gathered under
// d.mu and asked outside it.
type decideJob struct {
	pane  string
	at    time.Time // UpdatedAt of the activity the answers are for
	agent string
	c     *decide.Client
	s     config.DecideSettings

	call   *decide.Call // approvals
	prompt string

	triage      bool
	state, text string

	turn    bool
	lastMsg string
	screen  Pane
}

// planDecisions picks the questions a hook event calls for. It marks the
// activity's urgency pending so notifications wait for triage. Callers
// hold d.mu.
func (d *Daemon) planDecisions(p model.Pane, m proto.AgentEvent, now time.Time) *decideJob {
	if up := agent.UserPrompt(m.Payload); up != "" {
		if d.dec.prompts == nil {
			d.dec.prompts = map[string]string{}
		}
		d.dec.prompts[p.ID] = up
	}
	x := d.dec.cur
	i := d.activityIndex(p.ID)
	if !x.on() || i < 0 || !d.st.Activities[i].UpdatedAt.Equal(now) {
		return nil // no provider, or the event changed nothing
	}
	a := &d.st.Activities[i]
	j := &decideJob{pane: p.ID, at: now, agent: string(m.Provider), c: d.client(), s: x.Settings}
	w := d.workspace(p.WorkspaceID)
	if ev, tool, input, cwd, ok := agent.Request(m.Payload); ok && ev == "PermissionRequest" && a.State == model.StatePendingApproval &&
		!agent.NeedsInteraction(tool) && x.Settings.Approvals != config.ModeOff {
		if cwd == "" {
			cwd = p.Cwd
		}
		root := ""
		if w != nil {
			root = w.RepoRoot
		}
		j.call = &decide.Call{Tool: tool, Input: input, Cwd: cwd, Root: root}
		j.prompt = d.dec.prompts[p.ID]
	}
	if x.Settings.Triage && model.NeedsYou(a.State) && a.Detail != "" {
		j.triage, j.state, j.text = true, string(a.State), a.Detail
		a.Urgency = model.UrgencyPending
	}
	if x.Settings.TurnCheck && a.State == model.StateCompleted {
		j.turn, j.lastMsg, j.screen = true, agent.LastMessage(m.Payload), d.panes[p.ID]
	}
	if j.call == nil && !j.triage && !j.turn {
		return nil
	}
	return j
}

// runDecisions asks job's questions in the background.
func (d *Daemon) runDecisions(ctx context.Context, j *decideJob) {
	if j == nil {
		return
	}
	if j.call != nil {
		go d.approve(ctx, j)
	}
	if j.triage {
		go d.triageJob(ctx, j)
	}
	if j.turn {
		go d.turnCheck(ctx, j)
	}
}

// onActivity runs f on the pane's activity if it is still the one asked
// about, and pushes the state either way: the counters moved.
func (d *Daemon) onActivity(pane string, at time.Time, f func(a *model.Activity)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closing {
		return
	}
	if i := d.activityIndex(pane); i >= 0 && d.st.Activities[i].UpdatedAt.Equal(at) {
		f(&d.st.Activities[i])
	}
	d.changed()
}

// approve asks whether a permission request is safe and shows the answer
// on the approval, with the first risk pitwall sees in the call
// ("Jev: allow 96% · sudo"). It decides nothing: the agent's own prompt
// is the only way a call runs.
func (d *Daemon) approve(ctx context.Context, j *decideJob) {
	flags := decide.Flags(*j.call, j.s.NeverAllow)
	ans, err := j.c.Ask(ctx, decide.FeatureApprovals, j.pane, decide.ApprovalState(j.agent, *j.call, j.prompt), decide.ApprovalQuestions())
	if err != nil {
		log.Printf("pitwall: approvals: %v", err)
	}
	d.onActivity(j.pane, j.at, func(a *model.Activity) {
		if err != nil || a.State != model.StatePendingApproval {
			return
		}
		verdict, probs := decide.Approval(ans)
		a.Advice, a.AdviceP = verdict, probs[verdict]
		if len(flags) > 0 {
			a.AdviceRule = flags[0]
		}
	})
}

// triageJob rates how soon the user should look at the pane.
func (d *Daemon) triageJob(ctx context.Context, j *decideJob) {
	ans, err := j.c.Ask(ctx, decide.FeatureTriage, j.pane, decide.TriageState(j.agent, j.state, j.text), decide.TriageQuestions())
	if err != nil {
		log.Printf("pitwall: triage: %v", err)
	}
	d.onActivity(j.pane, j.at, func(a *model.Activity) {
		if a.Urgency != model.UrgencyPending {
			return
		}
		a.Urgency = ""
		if err == nil {
			a.Urgency = decide.Urgency(ans)
		}
	})
}

// turnCheck asks whether a finished turn needs the user's review.
// turnCapture runs between the turn check's two checks of the pane;
// tests use it to change the pane in between.
var turnCapture = func() {}

// turnCheck asks whether a finished turn needs the user's review. The
// pane is checked before and after its screen is captured: the finished
// turn must still be its activity and the agent whose hook reported it
// must still hold the foreground, so a shell's screen is never sent as
// the agent's.
func (d *Daemon) turnCheck(ctx context.Context, j *decideJob) {
	valid := func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		i := d.activityIndex(j.pane)
		if d.closing || j.screen == nil || d.panes[j.pane] != j.screen || i < 0 ||
			!d.st.Activities[i].UpdatedAt.Equal(j.at) || d.st.Activities[i].State != model.StateCompleted {
			return false
		}
		if fg, ok := d.live.fg[j.pane]; ok {
			if f, ok := j.screen.(foregrounder); ok && f.Foreground() != fg {
				return false
			}
		}
		return true
	}
	if !valid() {
		return
	}
	turnCapture()
	screen := agent.ScreenText(j.screen.Snapshot(), 40)
	if !valid() {
		return
	}
	ans, err := j.c.Ask(ctx, decide.FeatureTurnCheck, j.pane, decide.TurnState(j.agent, j.lastMsg, screen), decide.TurnQuestions())
	if err != nil {
		log.Printf("pitwall: turn check: %v", err)
	}
	d.onActivity(j.pane, j.at, func(a *model.Activity) {
		a.Review = err == nil && decide.Review(ans) >= j.s.TurnThreshold
	})
}

// screenEvery is the least time between two screen reads of one pane;
// tests shorten it.
var screenEvery = 2 * time.Second

// screenRead is the screen reading of one program in a pane, an agent
// CLI without hooks. Guarded by d.mu.
type screenRead struct {
	agent   string
	cells   []vt.Cell // the screen last sent
	applied bool      // an answer set the pane's activity
}

// screenPace is a pane's last screen send, kept across program changes so
// switching or restarting the program cannot send more often.
type screenPace struct {
	last time.Time // when a screen was last sent
	busy bool      // a read is on its way
}

// due reports whether the screen should be read now: it changed since the
// last read of this program, and the pane's last send was screenEvery ago
// or more and has been answered.
func (r *screenRead) due(p *screenPace, cells []vt.Cell, now time.Time) bool {
	return !p.busy && now.Sub(p.last) >= screenEvery && !slices.Equal(cells, r.cells)
}

// screenStates maps the screen question's options to activity states;
// idle is no activity.
var screenStates = map[string]model.AgentState{
	decide.ScreenWorking:  model.StateWorking,
	decide.ScreenWaiting:  model.StateAwaitingInput,
	decide.ScreenApproval: model.StatePendingApproval,
	decide.ScreenDone:     model.StateCompleted,
	decide.ScreenIdle:     "",
}

// readScreen asks what the agent CLI name in pane id shows on g, when a
// read is due, and sets the pane's activity from a sure enough answer.
// Until the first answer the pane shows the program as a running command.
// Callers hold d.mu.
func (d *Daemon) readScreen(ctx context.Context, id, name string, g vt.Grid) {
	if d.dec.screens == nil {
		d.dec.screens = map[string]*screenRead{}
	}
	r := d.dec.screens[id]
	if r == nil || r.agent != name {
		r = &screenRead{agent: name}
		d.dec.screens[id] = r
	}
	if !r.applied {
		d.setActivity(ctx, id, model.ProviderTerminal, model.StateTerminalRunning, name)
	}
	if d.dec.paces == nil {
		d.dec.paces = map[string]*screenPace{}
	}
	pace := d.dec.paces[id]
	if pace == nil {
		pace = &screenPace{}
		d.dec.paces[id] = pace
	}
	now := time.Now()
	if !r.due(pace, g.Cells, now) {
		return
	}
	pace.busy, pace.last, r.cells = true, now, g.Cells
	c, threshold := d.client(), d.dec.cur.Settings.AgentThreshold
	text := agent.ScreenText(g, 40)
	go func() {
		ans, err := c.Ask(ctx, decide.FeatureAgents, id, decide.ScreenState(name, text), decide.ScreenQuestions())
		if err != nil {
			log.Printf("pitwall: agent screen: %v", err)
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		pace.busy = false
		if d.closing || d.dec.screens[id] != r {
			return // the program left the foreground meanwhile
		}
		if err == nil {
			if s, conf := decide.Screen(ans); conf >= threshold {
				r.applied = true
				d.setActivity(ctx, id, model.Provider(name), screenStates[s], "")
			}
		}
		d.changed()
	}()
}
