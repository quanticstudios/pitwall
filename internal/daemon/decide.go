package daemon

import (
	"context"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/decisionlog"
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
	prompts map[string]string // pane: the latest user prompt
	checks  map[string]*check // pane: the latest check this turn
	stuck   map[string]*stuckAsk

	// For the decisions log, per pane: the permission prompt asked about
	// until its answer shows, the last turn checked and this turn's
	// checks until the next prompt, and the last triaged attention until
	// the pane is focused.
	asks, turns, triages map[string]*pending
	checked              map[string][]*pending
}

// pending is a decision waiting for what the user does about it.
type pending struct {
	id         string
	at         time.Time // when the activity it is about began
	keyAt      time.Time // asks: the last key into the pane while it asked
	tool       string    // asks: the tool asked about
	held, risk bool      // asks, triage: the answer is hidden; asks: pitwall flags a risk
}

// check is a check the agent ran this turn, failed by its exit code or,
// for one that exited 0, by the model's answer when it comes.
type check struct{ failed bool }

// stuckAsk is a pane's stuck question while its agent keeps working:
// since is the Working activity's UpdatedAt, asked the last ask, and id
// the call whose outcome is still open ("" for none).
type stuckAsk struct {
	since, asked time.Time
	busy         bool
	id           string
}

// holdoutDraw picks a prompt's arm: held out when it is below the
// holdout share. Tests replace it.
var holdoutDraw = rand.Float64

// record queues e for the decisions log.
func (d *Daemon) record(e decisionlog.Event) { d.o.Journal.Add(e) }

// callEvent is the log line of one call; the caller adds the answer.
func callEvent(id, feature, pane string, m decide.Meta) decisionlog.Event {
	return decisionlog.Event{T: time.Now(), Kind: decisionlog.Call, ID: id, Feature: feature, Pane: pane,
		Ms: m.Took.Milliseconds(), Err: m.Err, Tokens: m.InputTokens, Estimated: m.Estimated}
}

// openPending starts a pending decision of pane in m. Callers hold d.mu.
func openPending(m *map[string]*pending, pane string, p *pending) {
	if *m == nil {
		*m = map[string]*pending{}
	}
	(*m)[pane] = p
}

// resolveAsk logs what the user answered pane's permission prompt, timed
// by the last key into the pane while it asked, else by now. Callers hold
// d.mu.
func (d *Daemon) resolveAsk(pane, user string, now time.Time) {
	a := d.dec.asks[pane]
	if a == nil {
		return
	}
	delete(d.dec.asks, pane)
	e := decisionlog.Event{T: now, Kind: decisionlog.Outcome, ID: a.id, Feature: decide.FeatureApprovals, Pane: pane,
		Held: a.held, Risk: a.risk, User: user}
	if user != decisionlog.Unknown {
		e.WaitMs, e.Via = now.Sub(a.at).Milliseconds(), "hook"
		if !a.keyAt.IsZero() {
			e.WaitMs, e.Via = a.keyAt.Sub(a.at).Milliseconds(), "key"
		}
	}
	d.record(e)
}

// noteOutcomes reads what a hook event says about decisions waiting on
// the user. A permission prompt is allowed when its tool reports having
// run, and denied when the turn goes on to a prompt or ends without it;
// a new prompt leaves the old one unknown. A prompt to the agent follows
// up its last checked turn. Callers hold d.mu.
func (d *Daemon) noteOutcomes(pane string, payload []byte, now time.Time) {
	ev, tool := agent.HookEvent(payload)
	if a := d.dec.asks[pane]; a != nil {
		switch ev {
		case "PostToolUse", "PostToolUseFailure":
			if tool == a.tool {
				d.resolveAsk(pane, decisionlog.Allowed, now)
			}
		case "UserPromptSubmit", "Stop", "StopFailure", "SessionEnd", "Interrupt":
			d.resolveAsk(pane, decisionlog.Denied, now)
		case "PermissionRequest":
			d.resolveAsk(pane, decisionlog.Unknown, now)
		}
	}
	switch ev {
	case "UserPromptSubmit", "Stop", "StopFailure", "SessionEnd", "Interrupt":
		d.endStuck(pane, decisionlog.Continued, now)
	}
	prompt := agent.Prompt("", payload)
	if prompt == "" {
		return
	}
	user := decisionlog.Prompted
	if decide.Correction(prompt) {
		user = decisionlog.Corrected
	}
	if t := d.dec.turns[pane]; t != nil {
		delete(d.dec.turns, pane)
		d.record(decisionlog.Event{T: now, Kind: decisionlog.Outcome, ID: t.id, Feature: decide.FeatureTurnCheck, Pane: pane,
			User: user, WaitMs: now.Sub(t.at).Milliseconds()})
	}
	for _, c := range d.dec.checked[pane] {
		d.record(decisionlog.Event{T: now, Kind: decisionlog.Outcome, ID: c.id, Feature: decide.FeatureCheck, Pane: pane,
			User: user, WaitMs: now.Sub(c.at).Milliseconds()})
	}
	delete(d.dec.checked, pane)
	delete(d.dec.checks, pane) // a new turn's checks start over
}

// noteInterrupt logs an Esc or Ctrl+C into a working pane as the outcome
// of its open stuck question. Callers hold d.mu.
func (d *Daemon) noteInterrupt(pane string, now time.Time) {
	d.endStuck(pane, decisionlog.Interrupted, now)
}

// endStuck logs the outcome of pane's open stuck question, if any.
// Callers hold d.mu.
func (d *Daemon) endStuck(pane, user string, now time.Time) {
	s := d.dec.stuck[pane]
	if s == nil || s.id == "" {
		return
	}
	d.record(decisionlog.Event{T: now, Kind: decisionlog.Outcome, ID: s.id, Feature: decide.FeatureStuck, Pane: pane,
		User: user, WaitMs: now.Sub(s.asked).Milliseconds()})
	s.id = ""
}

// noteKey times an answer to a permission prompt: a key into the pane
// while it asks. Callers hold d.mu.
func (d *Daemon) noteKey(pane string, s model.AgentState, now time.Time) {
	if a := d.dec.asks[pane]; a != nil && s == model.StatePendingApproval {
		a.keyAt = now
	}
}

// noteSeen logs how long a triaged pane waited to be focused. Callers
// hold d.mu.
func (d *Daemon) noteSeen(pane string, now time.Time) {
	if t := d.dec.triages[pane]; t != nil {
		delete(d.dec.triages, pane)
		d.record(decisionlog.Event{T: now, Kind: decisionlog.Outcome, ID: t.id, Feature: decide.FeatureTriage, Pane: pane,
			Held: t.held, User: decisionlog.Focused, WaitMs: now.Sub(t.at).Milliseconds()})
	}
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
			log.Printf("decisions: %q", err) // never holds the key
		}
		last.Secrets = decide.KnownKeys(credPath)
		switch s.Provider {
		case "jev":
			if key != "" {
				jev := decide.NewJev(key, s.Model)
				jev.Secrets = last.Secrets
				last.Provider = jev
			}
		case "command":
			last.Provider = decide.Command{Argv: s.Command, Secrets: last.Secrets}
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
	delete(d.dec.checks, id)
	d.resolveAsk(id, decisionlog.Unknown, time.Now())
	d.endStuck(id, decisionlog.Continued, time.Now())
	delete(d.dec.stuck, id)
	delete(d.dec.turns, id)
	delete(d.dec.checked, id)
	delete(d.dec.triages, id)
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
	a.Advice, a.AdviceP, a.AdviceRule, a.Urgency, a.Review, a.Stuck = "", 0, "", "", false, false
}

// decideJob is the questions one hook event calls for, gathered under
// d.mu and asked outside it.
type decideJob struct {
	pane  string
	at    time.Time // UpdatedAt of the activity the answers are for
	agent string
	c     *decide.Client
	s     config.DecideSettings

	call   *decide.Call // an approval the rules left to the model
	prompt string
	held   bool // the verdict is not shown (holdout)

	askID, triageID, turnID, checkID string // decision ids for the log

	triage      bool
	triageHeld  bool // the urgency is not used (holdout)
	state, text string

	turn    bool
	lastMsg string

	check           *check // a check that exited 0, for the model to read
	command, output string
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
	if !x.on() {
		return nil
	}
	j := &decideJob{pane: p.ID, at: now, agent: string(m.Provider), c: d.client(), s: x.Settings}
	if x.Settings.Check {
		d.planCheck(p.ID, m.Payload, now, j)
	}
	i := d.activityIndex(p.ID)
	if i < 0 || !d.st.Activities[i].UpdatedAt.Equal(now) {
		return j.orNil() // the event changed no state
	}
	a := &d.st.Activities[i]
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
		c := decide.Call{Tool: tool, Input: input, Cwd: cwd, Root: root}
		flags := decide.Flags(c, x.Settings.NeverAllow)
		j.askID = newID()
		ask := &pending{id: j.askID, at: now, tool: tool, risk: len(flags) > 0}
		openPending(&d.dec.asks, p.ID, ask)
		if len(flags) > 0 {
			// A risk pitwall sees answers by itself: the model is not asked.
			a.AdviceRule = flags[0]
			d.record(decisionlog.Event{T: now, Kind: decisionlog.Call, ID: j.askID, Feature: decide.FeatureApprovals, Pane: p.ID,
				Answer: decide.Ask, P: 1, Risk: true, Base: "rule"})
		} else {
			j.call, j.prompt = &c, d.dec.prompts[p.ID]
			j.held = holdoutDraw() < x.Settings.Holdout
			ask.held = j.held
		}
	}
	d.planTriage(a, j)
	if a.State == model.StateCompleted {
		if c := d.dec.checks[p.ID]; x.Settings.Check && c != nil && c.failed {
			a.Review = true // the turn ends on a failed check
		}
		if x.Settings.TurnCheck {
			j.turn, j.lastMsg = true, agent.LastMessage(m.Payload)
			j.turnID = newID()
			openPending(&d.dec.turns, p.ID, &pending{id: j.turnID, at: now})
		}
	}
	return j.orNil()
}

// planTriage asks triage about a, a pane's or a chat's activity, when it
// needs you. It marks the activity's urgency pending so notifications
// wait for triage. Callers hold d.mu.
func (d *Daemon) planTriage(a *model.Activity, j *decideJob) {
	if !j.s.Triage || !model.NeedsYou(a.State) || a.Detail == "" {
		return
	}
	j.triage, j.state, j.text = true, string(a.State), a.Detail
	j.triageHeld = holdoutDraw() < j.s.TriageHoldout
	a.Urgency = model.UrgencyPending
	j.triageID = newID()
	openPending(&d.dec.triages, a.PaneID, &pending{id: j.triageID, at: a.UpdatedAt, held: j.triageHeld})
}

// orNil is j, or nil when it asks nothing.
func (j *decideJob) orNil() *decideJob {
	if j.call == nil && !j.triage && !j.turn && j.check == nil {
		return nil
	}
	return j
}

// planCheck reads a check the agent just ran: one that failed counts at
// once; one that exited 0 is left for the model to read. Callers hold
// d.mu.
func (d *Daemon) planCheck(pane string, payload []byte, now time.Time, j *decideJob) {
	cmd, out, failed, ok := agent.ToolRun(payload)
	if !ok || !decide.IsCheck(cmd) {
		return
	}
	if d.dec.checks == nil {
		d.dec.checks = map[string]*check{}
	}
	c := &check{failed: failed}
	d.dec.checks[pane] = c
	if failed {
		return
	}
	j.check, j.command, j.output, j.checkID = c, cmd, out, newID()
	if d.dec.checked == nil {
		d.dec.checked = map[string][]*pending{}
	}
	d.dec.checked[pane] = append(d.dec.checked[pane], &pending{id: j.checkID, at: now})
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
	if j.check != nil {
		go d.checkJob(ctx, j)
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

// nouls is each answer's probability of yes, for the log.
func nouls(ans map[string]decide.Answer) map[string]float64 {
	if len(ans) == 0 {
		return nil
	}
	out := map[string]float64{}
	for k, a := range ans {
		out[k] = a.Noul
	}
	return out
}

// approve asks about a permission request the rules left open and shows
// the answer on the approval ("Jev: allow 81%"). A held-out request shows
// nothing. It decides nothing: the agent's own prompt is the only way a
// call runs.
func (d *Daemon) approve(ctx context.Context, j *decideJob) {
	ans, meta, err := j.c.AskMeta(ctx, decide.FeatureApprovals, j.pane, decide.ApprovalState(j.agent, *j.call, j.prompt), decide.ApprovalQuestions())
	if err != nil {
		log.Printf("approvals: %q", err)
	}
	e := callEvent(j.askID, decide.FeatureApprovals, j.pane, meta)
	e.Held = j.held
	var verdict string
	var p float64
	if err == nil {
		verdict, p = decide.Approval(ans)
		e.Answer, e.P, e.Probs = verdict, p, nouls(ans)
	}
	d.record(e)
	d.onActivity(j.pane, j.at, func(a *model.Activity) {
		if a.State == model.StatePendingApproval && err == nil && !j.held {
			a.Advice, a.AdviceP = verdict, p
		}
	})
}

// triageJob asks whether the pane is blocked on the user (now) or has
// nothing for them (fyi). A held-out pane stays untriaged.
func (d *Daemon) triageJob(ctx context.Context, j *decideJob) {
	ans, meta, err := j.c.AskMeta(ctx, decide.FeatureTriage, j.pane, decide.TriageState(j.agent, j.state, j.text), decide.TriageQuestions())
	if err != nil {
		log.Printf("triage: %q", err)
	}
	e := callEvent(j.triageID, decide.FeatureTriage, j.pane, meta)
	e.Held = j.triageHeld
	if err == nil {
		e.Answer, e.P, e.Probs = decide.Urgency(ans), ans["blocked"].Noul, nouls(ans)
	}
	d.record(e)
	d.onActivity(j.pane, j.at, func(a *model.Activity) {
		if a.Urgency != model.UrgencyPending {
			return
		}
		a.Urgency = ""
		if err == nil && !j.triageHeld {
			a.Urgency = decide.Urgency(ans)
		}
	})
}

// turnCheck asks whether a finished turn needs the user's review, from
// the turn's last message alone. It never sends the screen: by the time a
// Stop hook is handled the agent may have exited, and a shell's screen
// must not go out as the agent's. decide.TurnRule's answer is logged
// next to it, as the baseline to beat.
func (d *Daemon) turnCheck(ctx context.Context, j *decideJob) {
	ans, meta, err := j.c.AskMeta(ctx, decide.FeatureTurnCheck, j.pane, decide.TurnState(j.agent, j.lastMsg), decide.TurnQuestions())
	if err != nil {
		log.Printf("turn check: %q", err)
	}
	e := callEvent(j.turnID, decide.FeatureTurnCheck, j.pane, meta)
	e.Base = "done"
	if decide.TurnRule(j.lastMsg) {
		e.Base = "check"
	}
	review := err == nil && decide.Review(ans) >= j.s.TurnThreshold
	if err == nil {
		e.Answer, e.P = "done", decide.Review(ans)
		if review {
			e.Answer = "check"
		}
	}
	d.record(e)
	d.onActivity(j.pane, j.at, func(a *model.Activity) {
		a.Review = a.Review || review
	})
}

// checkJob asks whether a check that exited 0 still shows a problem. A
// yes fails the check, so a turn that ends on it reads Check.
// ponytail: an answer that comes after its turn's Stop is logged but marks
// nothing; a check takes far longer to run than the model to answer.
func (d *Daemon) checkJob(ctx context.Context, j *decideJob) {
	ans, meta, err := j.c.AskMeta(ctx, decide.FeatureCheck, j.pane, decide.CheckState(j.agent, j.command, j.output), decide.CheckQuestions())
	if err != nil {
		log.Printf("check: %q", err)
	}
	e := callEvent(j.checkID, decide.FeatureCheck, j.pane, meta)
	failed := err == nil && decide.CheckFailed(ans) >= 0.5
	if err == nil {
		e.Answer, e.P = "passed", decide.CheckFailed(ans)
		if failed {
			e.Answer = "failed"
		}
	}
	d.record(e)
	d.mu.Lock()
	j.check.failed = j.check.failed || failed
	d.mu.Unlock()
}

// checkStuck asks, on the liveness poll, whether an agent working
// decide.StuckAfter or more without a state change is stuck, and again
// every decide.StuckAfter while it works. Only the screen of the hooked
// agent itself is sent: the pane's foreground must still be the one its
// hooks came from.
func (d *Daemon) checkStuck(ctx context.Context) {
	d.mu.Lock()
	x := d.dec.cur
	if !x.on() || !x.Settings.Stuck {
		d.mu.Unlock()
		return
	}
	now := time.Now()
	type ask struct {
		id, agent string
		since     time.Time
		p         Pane
		fg        int
		s         *stuckAsk
	}
	var asks []ask
	for _, a := range d.st.Activities {
		if a.State != model.StateWorking || now.Sub(a.UpdatedAt) < decide.StuckAfter || d.live.hookAt[a.PaneID].IsZero() {
			continue
		}
		if d.dec.stuck == nil {
			d.dec.stuck = map[string]*stuckAsk{}
		}
		s := d.dec.stuck[a.PaneID]
		if s == nil || !s.since.Equal(a.UpdatedAt) {
			d.endStuck(a.PaneID, decisionlog.Continued, now)
			s = &stuckAsk{since: a.UpdatedAt}
			d.dec.stuck[a.PaneID] = s
		}
		if s.busy || now.Sub(s.asked) < decide.StuckAfter {
			continue
		}
		if p := d.panes[a.PaneID]; p != nil {
			s.busy = true
			asks = append(asks, ask{id: a.PaneID, agent: string(a.Provider), since: a.UpdatedAt, p: p, fg: d.live.fg[a.PaneID], s: s})
		}
	}
	c, threshold := d.client(), x.Settings.StuckThreshold
	d.mu.Unlock()
	for _, k := range asks {
		f, ok := k.p.(foregrounder)
		if !ok || f.Foreground() != k.fg {
			d.mu.Lock()
			k.s.busy = false
			d.mu.Unlock()
			continue
		}
		text := agent.ScreenText(k.p.Snapshot(), 40)
		go func() {
			ans, meta, err := c.AskMeta(ctx, decide.FeatureStuck, k.id, decide.StuckState(k.agent, int(now.Sub(k.since).Minutes()), text), decide.StuckQuestions())
			if err != nil {
				log.Printf("stuck: %q", err)
			}
			id := newID()
			e := callEvent(id, decide.FeatureStuck, k.id, meta)
			stuck := err == nil && decide.Stuck(ans) >= threshold
			if err == nil {
				e.Answer, e.P = "working", decide.Stuck(ans)
				if stuck {
					e.Answer = "stuck"
				}
			}
			d.record(e)
			d.mu.Lock()
			if d.dec.stuck[k.id] == k.s {
				d.endStuck(k.id, decisionlog.Continued, time.Now())
				k.s.asked = time.Now()
				if err == nil {
					k.s.id = id
				}
			}
			k.s.busy = false
			d.mu.Unlock()
			d.onActivity(k.id, k.since, func(a *model.Activity) {
				if a.State == model.StateWorking {
					a.Stuck = stuck
				}
			})
		}()
	}
}
