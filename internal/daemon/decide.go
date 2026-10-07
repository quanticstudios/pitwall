package daemon

import (
	"context"
	"fmt"
	"log"
	"math"
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

	// For the decisions log, per pane: the permission prompt asked about
	// until its answer shows, the last turn checked until the next prompt,
	// and the last triaged attention until the pane is focused.
	asks, turns, triages map[string]*pending
}

// pending is a decision waiting for what the user does about it.
type pending struct {
	id         string
	at         time.Time // when the activity it is about began
	keyAt      time.Time // asks: the last key into the pane while it asked
	tool       string    // asks: the tool asked about
	held, risk bool      // asks: the verdict is hidden; pitwall flags a risk
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
	if t := d.dec.turns[pane]; t != nil && agent.Prompt("", payload) != "" {
		delete(d.dec.turns, pane)
		d.record(decisionlog.Event{T: now, Kind: decisionlog.Outcome, ID: t.id, Feature: decide.FeatureTurnCheck, Pane: pane,
			User: decisionlog.Prompted, WaitMs: now.Sub(t.at).Milliseconds()})
	}
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
			User: decisionlog.Focused, WaitMs: now.Sub(t.at).Milliseconds()})
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
	delete(d.dec.screens, id)
	delete(d.dec.paces, id)
	d.resolveAsk(id, decisionlog.Unknown, time.Now())
	delete(d.dec.turns, id)
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
	flags  []string // the risks pitwall sees in call
	held   bool     // the verdict is not shown (holdout)

	askID, triageID, turnID string // decision ids for the log

	triage      bool
	state, text string

	turn    bool
	lastMsg string
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
		j.flags = decide.Flags(*j.call, x.Settings.NeverAllow)
		j.held = holdoutDraw() < x.Settings.Holdout
		j.askID = newID()
		openPending(&d.dec.asks, p.ID, &pending{id: j.askID, at: now, tool: tool, held: j.held, risk: len(j.flags) > 0})
	}
	if x.Settings.Triage && model.NeedsYou(a.State) && a.Detail != "" {
		j.triage, j.state, j.text = true, string(a.State), a.Detail
		a.Urgency = model.UrgencyPending
		j.triageID = newID()
		openPending(&d.dec.triages, p.ID, &pending{id: j.triageID, at: now})
	}
	if x.Settings.TurnCheck && a.State == model.StateCompleted {
		j.turn, j.lastMsg = true, agent.LastMessage(m.Payload)
		j.turnID = newID()
		openPending(&d.dec.turns, p.ID, &pending{id: j.turnID, at: now})
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
// ("Jev: allow 96% · sudo"). A held-out request shows the risk alone, and
// so does one the model did not answer. It decides nothing: the agent's
// own prompt is the only way a call runs.
func (d *Daemon) approve(ctx context.Context, j *decideJob) {
	ans, meta, err := j.c.AskMeta(ctx, decide.FeatureApprovals, j.pane, decide.ApprovalState(j.agent, *j.call, j.prompt), decide.ApprovalQuestions())
	if err != nil {
		log.Printf("approvals: %q", err)
	}
	verdict, probs := decide.Approval(ans)
	e := callEvent(j.askID, decide.FeatureApprovals, j.pane, meta)
	e.Answer, e.P, e.Conf, e.Held, e.Risk = verdict, probs[verdict], ans["verdict"].Confidence, j.held, len(j.flags) > 0
	d.record(e)
	d.onActivity(j.pane, j.at, func(a *model.Activity) {
		if a.State != model.StatePendingApproval {
			return
		}
		if len(j.flags) > 0 {
			a.AdviceRule = j.flags[0]
		}
		if err == nil && !j.held {
			a.Advice, a.AdviceP = verdict, probs[verdict]
		}
	})
}

// triageJob rates how soon the user should look at the pane.
func (d *Daemon) triageJob(ctx context.Context, j *decideJob) {
	ans, meta, err := j.c.AskMeta(ctx, decide.FeatureTriage, j.pane, decide.TriageState(j.agent, j.state, j.text), decide.TriageQuestions())
	if err != nil {
		log.Printf("triage: %q", err)
	}
	e := callEvent(j.triageID, decide.FeatureTriage, j.pane, meta)
	if err == nil {
		u := ans["urgency"]
		e.Answer, e.P, e.Conf = decide.Urgency(ans), u.Probabilities[fmt.Sprint(int(math.Round(u.Score)))], u.Confidence
	}
	d.record(e)
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

// turnCheck asks whether a finished turn needs the user's review, from
// the turn's last message alone. It never sends the screen: by the time a
// Stop hook is handled the agent may have exited, and a shell's screen
// must not go out as the agent's.
func (d *Daemon) turnCheck(ctx context.Context, j *decideJob) {
	ans, meta, err := j.c.AskMeta(ctx, decide.FeatureTurnCheck, j.pane, decide.TurnState(j.agent, j.lastMsg), decide.TurnQuestions())
	if err != nil {
		log.Printf("turn check: %q", err)
	}
	e := callEvent(j.turnID, decide.FeatureTurnCheck, j.pane, meta)
	if err == nil {
		e.Answer, e.P = "done", decide.Review(ans)
		if e.P >= j.s.TurnThreshold {
			e.Answer = "check"
		}
	}
	d.record(e)
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
		ans, meta, err := c.AskMeta(ctx, decide.FeatureAgents, id, decide.ScreenState(name, text), decide.ScreenQuestions())
		if err != nil {
			log.Printf("agent screen: %q", err)
		}
		e := callEvent(newID(), decide.FeatureAgents, id, meta)
		if err == nil {
			e.Answer, e.Conf = decide.Screen(ans)
			e.P = ans["status"].Probabilities[e.Answer]
		}
		d.record(e)
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
