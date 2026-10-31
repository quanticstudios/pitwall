package fake

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

// Clock controls both timestamps and scheduling. Tests can advance it without
// sleeping. After must return a channel that becomes readable after d.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time                         { return time.Now() }
func (wallClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Options configures a fake independently of the process environment.
type Options struct {
	// NewID supplies a fresh namespace per Start. Nil uses crypto/rand.Text.
	// Tests can inject a stable value for deterministic replay.
	NewID      func() string
	Clock      Clock
	Script     *Script // a caller's JSON fixture; overrides ScriptName and prompt selection
	ScriptName string
	FailStart  bool
	Stream     *StreamConfig
}

// Provider implements session.Provider. It never starts a process.
type Provider struct{ options Options }

func New(options Options) *Provider { return &Provider{options: options} }

// ProvidersFromEnv returns the real registry unchanged unless explicitly opted
// in with PITWALL_FAKE_PROVIDERS=1. There is no build-mode default. Both managed
// providers are replaced, even if their real drivers are not registered yet.
// Call once when constructing the daemon's registry; warn defaults to log.Print.
// Script selection and aide's performance-stream knobs are read at Start.
func ProvidersFromEnv(real map[model.Provider]session.Provider, warn func(string)) map[model.Provider]session.Provider {
	if os.Getenv("PITWALL_FAKE_PROVIDERS") != "1" {
		return real
	}
	if warn == nil {
		warn = func(s string) { log.Print(s) }
	}
	warn("WARNING: PITWALL_FAKE_PROVIDERS=1: Claude and Codex use scripted fake providers; no agents will run")
	out := maps.Clone(real)
	if out == nil {
		out = make(map[model.Provider]session.Provider)
	}
	for _, kind := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		out[kind] = environmentProvider{}
	}
	return out
}

type environmentProvider struct{}

func (environmentProvider) Start(ctx context.Context, cfg session.Config, from session.Origin, emit func(session.Event)) (session.Driver, error) {
	stream, err := streamFromEnv()
	if err != nil {
		return nil, err
	}
	return New(Options{ScriptName: os.Getenv("PITWALL_FAKE_SCRIPT"), Stream: stream}).Start(ctx, cfg, from, emit)
}

func (p *Provider) Start(ctx context.Context, cfg session.Config, from session.Origin, emit func(session.Event)) (session.Driver, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.options.FailStart || p.options.Script == nil && p.options.ScriptName == "fail-start" {
		return nil, &session.ProviderError{Op: "start", Message: "Fake managed provider failed to start"}
	}
	if cfg.Provider != model.ProviderClaude && cfg.Provider != model.ProviderCodex {
		return nil, &session.ProviderError{Op: "start", Message: "unsupported fake provider"}
	}
	if emit == nil {
		return nil, &session.ProviderError{Op: "start", Message: "nil event callback"}
	}
	clock := p.options.Clock
	if clock == nil {
		clock = wallClock{}
	}
	// Own the fixture so a caller cannot mutate a running driver's script.
	options := p.options
	if options.Script != nil {
		b, err := json.Marshal(options.Script)
		if err != nil {
			return nil, err
		}
		options.Script = new(Script)
		if err := json.Unmarshal(b, options.Script); err != nil {
			return nil, err
		}
	}
	if options.Script == nil && options.ScriptName != "" {
		if _, err := Load(options.ScriptName); err != nil {
			return nil, err
		}
	}
	newID := options.NewID
	if newID == nil {
		newID = rand.Text
	}
	prefix := newID()
	resume := from.Resume
	if resume == "" || from.Fork {
		resume = "fake-" + prefix
		if from.Fork {
			resume += "-fork"
		}
	}
	d := &driver{commands: make(chan command), done: make(chan struct{}), clock: clock, emit: emit, cfg: cfg, options: options, prefix: prefix}
	go d.run(resume)
	return d, nil
}

type command struct {
	ctx       context.Context
	op        string
	message   session.Message
	requestID string
	decision  session.Decision
	answers   map[string][]string
	settings  session.Settings
	reply     chan result
}
type result struct {
	id  string
	err error
}
type driver struct {
	commands chan command
	done     chan struct{}
	clock    Clock
	emit     func(session.Event)
	cfg      session.Config
	options  Options
	// All fields below belong to run's goroutine.
	prefix  string
	serial  int
	turn    string
	steps   []Step
	next    int
	gates   []Step // open requests waiting for a response, in open order
	timer   <-chan time.Time
	pending *command
}

var _ session.Provider = (*Provider)(nil)
var _ session.Driver = (*driver)(nil)

// Commands and emissions share one goroutine. The consumer must not call driver
// methods synchronously from emit; enqueue events as the daemon's chat loop does.
func (d *driver) call(ctx context.Context, c command) result {
	if err := ctx.Err(); err != nil {
		return result{err: err}
	}
	c.ctx = ctx
	c.reply = make(chan result, 1)
	select {
	case <-d.done:
		return result{err: session.ErrClosed}
	default:
	}
	select {
	case <-ctx.Done():
		return result{err: ctx.Err()}
	case <-d.done:
		return result{err: session.ErrClosed}
	case d.commands <- c:
	}
	select {
	case r := <-c.reply:
		return r
	case <-ctx.Done():
		return result{err: ctx.Err()}
	}
}
func (d *driver) Send(ctx context.Context, m session.Message) (string, error) {
	r := d.call(ctx, command{op: "send", message: m})
	return r.id, r.err
}
func (d *driver) Steer(ctx context.Context, m session.Message) error {
	return d.call(ctx, command{op: "steer", message: m}).err
}
func (d *driver) Interrupt(ctx context.Context) error {
	return d.call(ctx, command{op: "interrupt"}).err
}
func (d *driver) Approve(ctx context.Context, id string, v session.Decision) error {
	return d.call(ctx, command{op: "approve", requestID: id, decision: v}).err
}
func (d *driver) Answer(ctx context.Context, id string, v map[string][]string) error {
	return d.call(ctx, command{op: "answer", requestID: id, answers: v}).err
}
func (d *driver) Set(ctx context.Context, s session.Settings) error {
	return d.call(ctx, command{op: "set", settings: s}).err
}
func (d *driver) Close(ctx context.Context) error {
	// There is no subprocess to wait for or kill. Always join the event loop,
	// including when the close deadline already expired, so emissions cannot
	// outlive Close. Event consumers must keep the callback nonblocking.
	r := d.call(context.WithoutCancel(ctx), command{op: "close"})
	if r.err == session.ErrClosed {
		return nil
	}
	if r.err != nil {
		return r.err
	}
	<-d.done
	return nil
}

func (d *driver) event(e session.Event) {
	if e.CreatedAt == "" {
		e.CreatedAt = session.Stamp(d.clock.Now())
	}
	if e.Type == session.EventContent && e.Model == "" && e.Tool == nil {
		e.Model = d.cfg.Model
	}
	d.emit(e)
}
func (d *driver) finish(state string) {
	d.complete(session.Event{Type: session.EventTurnCompleted, State: state})
	d.steps, d.timer, d.next = nil, nil, 0
}
func (d *driver) complete(e session.Event) {
	if d.turn == "" {
		return
	}
	for _, gate := range d.gates {
		kind := session.EventRequestResolved
		if gate.Type == session.EventInputRequested {
			kind = session.EventInputResolved
		}
		d.event(session.Event{Type: kind, RequestID: gate.RequestID})
	}
	e.TurnID = d.turn
	if e.Point == "" {
		e.Point = d.turn
	}
	d.event(e)
	d.turn, d.gates = "", nil
}
func (d *driver) schedule() {
	d.timer = nil
	if n := len(d.gates); n > 0 && !d.gates[n-1].Queue || d.next >= len(d.steps) {
		return
	}
	d.timer = d.clock.After(time.Duration(d.steps[d.next].DelayMS) * time.Millisecond)
}
func (d *driver) run(resume string) {
	defer close(d.done)
	d.event(session.Event{Type: session.EventResumeID, ResumeID: resume, Version: "fake"})
	d.event(session.Event{Type: session.EventState, State: session.StateReady})
	for {
		if d.pending != nil && d.next >= len(d.steps) {
			c := *d.pending
			d.pending = nil
			c.reply <- d.send(c)
		}
		select {
		case <-d.timer:
			for {
				step := d.steps[d.next]
				d.next++
				if step.Type == session.EventTurnCompleted {
					d.complete(step.Event)
				} else {
					d.event(step.Event)
					if step.Type == session.EventError && !slices.ContainsFunc(d.steps[d.next:], func(s Step) bool { return s.Type == session.EventTurnCompleted }) {
						d.turn, d.gates = "", nil
					}
					if step.Type == session.EventExited {
						if d.pending != nil {
							d.pending.reply <- result{err: session.ErrClosed}
						}
						return
					}
					if step.Answers != nil {
						d.gates = append(d.gates, step)
					}
				}
				// A queued request opens with the undelayed steps after it, so no response lands in between and replay stays deterministic.
				if !step.Queue || d.next >= len(d.steps) || d.steps[d.next].DelayMS != 0 {
					break
				}
			}
			d.schedule()
		case c := <-d.commands:
			r := result{}
			switch c.op {
			case "send":
				if d.turn != "" || d.pending != nil {
					r.err = session.ErrBusy
					break
				}
				// A turn may have trailing usage/state events. Finish those before a
				// subsequent Send starts, without dropping them or blocking other commands.
				if d.next < len(d.steps) {
					d.pending = &c
					continue
				}
				r = d.send(c)
			case "steer":
				if d.turn == "" {
					r.err = session.ErrIdle
					break
				}
				d.event(session.Event{Type: session.EventContent, ItemID: d.turn + "-steer", Kind: session.KindAssistantText, Text: "Steered: " + c.message.Text})
				d.finish(session.TurnCompleted)
			case "interrupt":
				d.finish(session.TurnInterrupted)
			case "approve", "answer":
				r.err = d.respond(c)
			case "set":
				if c.settings.Model != "" {
					d.cfg.Model = c.settings.Model
				}
				if c.settings.Effort != "" {
					d.cfg.Effort = c.settings.Effort
				}
				if c.settings.Mode != "" {
					d.cfg.Mode = c.settings.Mode
				}
			case "close":
				if d.pending != nil {
					d.pending.reply <- result{err: session.ErrClosed}
					d.pending = nil
				}
				d.finish(session.TurnInterrupted)
				zero := 0
				d.event(session.Event{Type: session.EventExited, ExitCode: &zero})
				c.reply <- r
				return
			}
			c.reply <- r
		}
	}
}

func (d *driver) send(c command) result {
	if err := c.ctx.Err(); err != nil {
		return result{err: err}
	}
	steps, err := d.script(c.message.Text)
	if err != nil {
		return result{err: err}
	}
	d.serial++
	d.turn = fmt.Sprintf("fake-%s-turn-%d", d.prefix, d.serial)
	d.steps = scope(steps, d.turn)
	d.next = 0
	if !slices.ContainsFunc(steps, func(s Step) bool { return s.Type == session.EventTurnStarted }) {
		d.event(session.Event{Type: session.EventTurnStarted, TurnID: d.turn})
	}
	d.schedule()
	return result{id: d.turn}
}

func (d *driver) respond(c command) error {
	i := slices.IndexFunc(d.gates, func(s Step) bool { return s.RequestID == c.requestID })
	if i < 0 {
		return &session.ProviderError{Op: c.op, Message: "no such pending request"}
	}
	gate := d.gates[i]
	input := gate.Type == session.EventInputRequested
	if input != (c.op == "answer") {
		return &session.ProviderError{Op: c.op, Message: "wrong response kind"}
	}
	expected := gate.Answers
	if expected.Allow != nil && *expected.Allow != c.decision.Allow || expected.Input != nil && !reflect.DeepEqual(expected.Input, c.answers) {
		return &session.ProviderError{Op: c.op, Message: "response differs from script answers"}
	}
	kind := session.EventRequestResolved
	if input {
		kind = session.EventInputResolved
	}
	d.event(session.Event{Type: kind, RequestID: c.requestID})
	// aide's fake echoes a denial reason so e2e can see it reached the provider.
	if !input && !c.decision.Allow && c.decision.Reason != "" {
		d.event(session.Event{Type: session.EventContent, ItemID: c.requestID + "/denial", Kind: session.KindAssistantText, Text: string(d.cfg.Provider) + " got the denial reason: " + c.decision.Reason})
	}
	d.gates = slices.Delete(d.gates, i, i+1)
	if len(d.gates) == 0 {
		d.schedule()
	}
	return nil
}

func scope(steps []Step, turn string) []Step {
	for i := range steps {
		e := &steps[i].Event
		if e.ItemID != "" {
			e.ItemID = turn + "/" + e.ItemID
		}
		if e.RequestID != "" {
			e.RequestID = turn + "/" + e.RequestID
		}
		if e.Tool != nil {
			e.Tool.CallID = turn + "/" + e.Tool.CallID
		}
		if e.TurnID != "" || e.Type == session.EventTurnCompleted || e.Type == session.EventTurnStarted {
			e.TurnID = turn
		}
		if e.Report != nil && e.Report.TurnID != nil {
			id := turn
			e.Report.TurnID = &id
		}
	}
	return steps
}

func (d *driver) script(prompt string) ([]Step, error) {
	// aide appends this context to e2e prompts. It is not part of the scenario.
	for _, suffix := range []string{"\n\nQuoted prior conversation,", "\n\nWorkspace evidence.", "\n\nConfigured read-only agents", "\n\nFleet policy."} {
		prompt, _, _ = strings.Cut(prompt, suffix)
	}
	prompt = strings.TrimSpace(prompt)
	name := d.options.ScriptName
	if name == "" {
		name = prompt
	}
	s := Script{}
	if d.options.Script != nil {
		s = *d.options.Script
	} else {
		var err error
		s, err = Load(name)
		if err != nil && d.options.ScriptName == "" {
			first, _, _ := strings.Cut(name, " ")
			s, err = Load(first)
			if err == nil {
				name = first
			}
			if err != nil {
				s = Script{RuntimeEvents: []Step{{Event: session.Event{Type: session.EventContent, ItemID: "reply", Kind: session.KindAssistantText, Text: "Handled: ${prompt}"}}, {Event: session.Event{Type: session.EventTurnCompleted, State: session.TurnCompleted}}}}
			}
		} else if err != nil {
			return nil, err
		}
	}
	if s.Mode != "" && s.Mode != d.cfg.Provider {
		s = Script{RuntimeEvents: []Step{{Event: session.Event{Type: session.EventContent, ItemID: "reply", Kind: session.KindAssistantText, Text: "Handled: ${prompt}"}}, {Event: session.Event{Type: session.EventTurnCompleted, State: session.TurnCompleted}}}}
	}
	if d.options.Script == nil && name == "edit readme" {
		f, err := os.OpenFile(d.cfg.Cwd+string(os.PathSeparator)+"README.md", os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return nil, err
		}
		_, err = f.WriteString("edited by the turn\n")
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	if d.options.Script == nil && name == "perf-stream" {
		s = perfScript(d.options.Stream)
	}
	// "usage limit <unix seconds>" is aide's: a five-hour limit that resets then, and a refused turn.
	if seconds, ok := strings.CutPrefix(name, "usage limit "); d.options.Script == nil && ok {
		unix, err := strconv.ParseInt(seconds, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("fake script %q: %w", name, err)
		}
		resets := time.Unix(unix, 0).UTC()
		s = Script{RuntimeEvents: []Step{
			{Event: session.Event{Type: session.EventLimit, Limit: &session.Limit{Rejected: true, Resets: resets, Windows: []session.LimitWindow{{Minutes: 300, Used: 100, Resets: resets}}}}},
			{Event: session.Event{Type: session.EventTurnCompleted, State: session.TurnFailed, ErrorMessage: "You've hit your usage limit."}},
		}}
	}
	// Decode a new copy on each turn; substitutions only touch JSON string values.
	b, err := json.Marshal(s.RuntimeEvents)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	replaceStrings(raw, strings.NewReplacer("${provider}", string(d.cfg.Provider), "${prompt}", prompt))
	b, err = json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var steps []Step
	if err := json.Unmarshal(b, &steps); err != nil {
		return nil, err
	}
	expanded, err := expand(steps)
	if err != nil {
		return nil, err
	}
	if d.options.Script == nil && (name == "long transcript" || name == "long transcript wait") {
		for i := 0; i < 1000; i++ {
			var lines []string
			for line := 0; line < 1+i%6; line++ {
				lines = append(lines, fmt.Sprintf("Step %d line %d: checked the next file.", i, line))
			}
			sep := "\n\n"
			if i%10 == 0 {
				sep = "\n\n```ts\nconst step = 1\n```\n\n"
			}
			expanded[i*2+1].Text = strings.Join(lines, sep)
		}
	}
	return expanded, nil
}

func replaceStrings(v any, r *strings.Replacer) {
	switch v := v.(type) {
	case map[string]any:
		for k, item := range v {
			if s, ok := item.(string); ok {
				v[k] = r.Replace(s)
			} else {
				replaceStrings(item, r)
			}
		}
	case []any:
		for i, item := range v {
			if s, ok := item.(string); ok {
				v[i] = r.Replace(s)
			} else {
				replaceStrings(item, r)
			}
		}
	}
}
func expand(steps []Step) ([]Step, error) {
	var out []Step
	for _, step := range steps {
		if step.DelayMS < 0 || step.Repeat < 0 {
			return nil, fmt.Errorf("fake script: negative delay or repeat")
		}
		if step.Answers != nil && step.Type != session.EventRequestOpened && step.Type != session.EventInputRequested {
			return nil, fmt.Errorf("fake script: answers requires a request")
		}
		if len(step.Events) == 0 {
			out = append(out, step)
			continue
		}
		for i := 0; i < step.Repeat; i++ {
			b, err := json.Marshal(step.Events)
			if err != nil {
				return nil, err
			}
			var raw any
			if err := json.Unmarshal(b, &raw); err != nil {
				return nil, err
			}
			replaceStrings(raw, strings.NewReplacer("${index}", strconv.Itoa(i), "${paddedIndex}", fmt.Sprintf("%03d", i)))
			b, err = json.Marshal(raw)
			if err != nil {
				return nil, err
			}
			var events []session.Event
			if err := json.Unmarshal(b, &events); err != nil {
				return nil, err
			}
			for _, e := range events {
				out = append(out, Step{Event: e, DelayMS: step.DelayMS})
			}
		}
	}
	return out, nil
}
