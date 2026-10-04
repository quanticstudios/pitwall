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
	audit   []model.AutoDecision // oldest first, at most auditCap
	prompts map[string]string    // pane: the latest user prompt
}

// auditCap is how many automatic decisions the daemon remembers.
const auditCap = 200

// syncDecideTimeout bounds an approval the hook waits on: the hooks are
// installed with a 5 s timeout, and the hook gives up after 4.5 s.
const syncDecideTimeout = 3500 * time.Millisecond

// loadDecisions reads [decisions] from the config at cfgPath and the key
// from credPath (or $TYPESAFE_API_KEY), again only when either file
// changed.
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
		switch s.Provider {
		case "jev":
			key, _, err := decide.LoadKey(credPath)
			if err != nil {
				log.Printf("pitwall: decisions: %v", err) // never holds the key
			}
			if key != "" {
				last.Provider, last.Secrets = decide.NewJev(key, s.Model), []string{key}
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
	old := d.dec.cur
	d.dec.cur = x
	if old.name() != x.name() || !reflect.DeepEqual(old.Settings, x.Settings) {
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
	d.dec.limit.Forget(id)
}

// decideInfo is the status clients show. Callers hold d.mu.
func (d *Daemon) decideInfo() model.DecideInfo {
	x := d.dec.cur
	info := model.DecideInfo{Provider: x.name(), Auto: x.on() && x.Settings.Approvals == config.ModeAuto, Audit: slices.Clone(d.dec.audit)}
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
	auto   bool
	prompt string
	tab    string
	wsID   string

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
	j := &decideJob{pane: p.ID, at: now, agent: string(m.Provider), c: d.client(), s: x.Settings, wsID: p.WorkspaceID}
	w := d.workspace(p.WorkspaceID)
	if ev, tool, input, cwd, ok := agent.Request(m.Payload); ok && ev == "PermissionRequest" && a.State == model.StatePendingApproval &&
		!agent.NeedsInteraction(tool) && x.Settings.Approvals != config.ModeOff {
		if cwd == "" {
			cwd = p.Cwd
		}
		root := ""
		if w != nil {
			root, j.tab = w.RepoRoot, w.Label
		}
		j.call = &decide.Call{Tool: tool, Input: input, Cwd: cwd, Root: root}
		j.auto = x.Settings.Approvals == config.ModeAuto && (w == nil || !w.AutoOff)
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

// runDecisions asks job's questions. An automatic approval is asked
// before returning, as the hook waits for it; the rest run in the
// background. It returns the hook's output.
func (d *Daemon) runDecisions(ctx context.Context, j *decideJob) []byte {
	if j == nil {
		return nil
	}
	var out []byte
	switch {
	case j.call != nil && j.auto:
		sctx, cancel := context.WithTimeout(ctx, syncDecideTimeout)
		out = d.approve(sctx, j)
		cancel()
	case j.call != nil:
		go d.approve(ctx, j)
	}
	if j.triage && out == nil {
		go d.triageJob(ctx, j)
	}
	if j.turn {
		go d.turnCheck(ctx, j)
	}
	return out
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

// approve asks whether a permission request is safe, shows the answer on
// the approval, and in auto mode returns the hook's allow or deny when
// the answer clears its threshold. A hard rule blocks allow whatever the
// answer. Any failure decides nothing.
func (d *Daemon) approve(ctx context.Context, j *decideJob) []byte {
	rules := decide.HardRules(*j.call, j.s.NeverAllow)
	ans, err := j.c.Ask(ctx, decide.FeatureApprovals, j.pane, decide.ApprovalState(j.agent, *j.call, j.prompt), decide.ApprovalQuestions())
	if err != nil {
		log.Printf("pitwall: approvals: %v", err)
		d.onActivity(j.pane, j.at, func(*model.Activity) {})
		return nil
	}
	verdict, probs := decide.Approval(ans)
	auto := ""
	if j.auto {
		auto = decide.AutoVerdict(probs, rules, j.s.AllowAbove, j.s.DenyAbove)
	}
	now := time.Now()
	d.onActivity(j.pane, j.at, func(a *model.Activity) {
		if auto != "" {
			// The agent goes on without showing its prompt.
			a.State, a.Detail, a.UpdatedAt = model.StateWorking, "", now
			clearDecisions(a)
			return
		}
		a.Advice, a.AdviceP = verdict, probs[verdict]
		if len(rules) > 0 {
			a.AdviceRule = rules[0]
		}
	})
	if auto == "" {
		return nil
	}
	d.mu.Lock()
	d.dec.audit = append(d.dec.audit, model.AutoDecision{
		At: now, PaneID: j.pane, WorkspaceID: j.wsID, Tab: j.tab, Agent: model.Provider(j.agent), Tool: j.call.Tool,
		Input: decide.Summary(*j.call, j.c.Secrets...), Verdict: auto, Allow: probs[decide.Allow], Ask: probs[decide.Ask], Deny: probs[decide.Deny], Rules: rules,
	})
	if n := len(d.dec.audit); n > auditCap {
		d.dec.audit = slices.Delete(d.dec.audit, 0, n-auditCap)
	}
	d.changed()
	d.mu.Unlock()
	return agent.PermissionDecision(auto, fmt.Sprintf("pitwall denied this automatically: the decision model judged it unsafe (deny %.0f%%). Ask the user if it is really needed.", probs[decide.Deny]*100))
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
func (d *Daemon) turnCheck(ctx context.Context, j *decideJob) {
	screen := ""
	if j.screen != nil {
		screen = agent.ScreenText(j.screen.Snapshot(), 40)
	}
	ans, err := j.c.Ask(ctx, decide.FeatureTurnCheck, j.pane, decide.TurnState(j.agent, j.lastMsg, screen), decide.TurnQuestions())
	if err != nil {
		log.Printf("pitwall: turn check: %v", err)
	}
	d.onActivity(j.pane, j.at, func(a *model.Activity) {
		a.Review = err == nil && decide.Review(ans) >= j.s.TurnThreshold
	})
}
