package config

import (
	"fmt"
	"time"
)

// Decisions is [decisions]: a decision model such as TypeSafe's Jev
// answering quick questions for pitwall. Nothing is sent anywhere while
// provider is "".
type Decisions struct {
	Provider  string    `toml:"provider" enum:"jev,command," doc:"Which decision model answers: jev (TypeSafe's Jev; connect with pitwall jev login or in Settings), command (your own program), or \"\" for none. Nothing is sent anywhere while this is \"\"."`
	Command   []string  `toml:"command" doc:"With the command provider: the program and its arguments. It gets the request JSON on stdin and prints the reply JSON, in Jev's shape, on stdout."`
	Model     string    `toml:"model" doc:"The Jev model or alias, such as jev-latest or a pinned jev-1.13.0."`
	Timeout   *float64  `toml:"timeout" min:"0.2" max:"10" doc:"Seconds a question may take, 0.2 to 10. pitwall goes on without the answer after that."`
	Approvals Approvals `toml:"approvals" doc:"Permission requests from Claude Code and Codex. A call pitwall sees a risk in shows the risk without asking; any other asks whether you asked for it and whether it is hard to undo. Sends the tool, its input, the working directory, the repo root and your latest prompt, with secrets removed."`
	Triage    Triage    `toml:"triage" doc:"Asks whether a pane that needs you is blocked on you (now) or has nothing for you to do (fyi), to order the jump-to-attention key and desktop notifications; fyi sends no notification. Sends the state and the agent's question, approval detail, error or summary."`
	TurnCheck Feature   `toml:"turn_check" doc:"When an agent finishes a turn, asks whether it needs your review (failed tests, errors, unfinished work); the Done pill then reads Check. Sends the agent's last message only, never the screen."`
	Check     Toggle    `toml:"check" doc:"When an agent's check (pre-commit, tests, lint, a type check, a build, git commit) exits 0, asks whether its output still shows a failure, a skip or a bypass. A check that failed, by its exit code or the answer, marks the turn Check when it ends. Sends the command and the end of its output."`
	Stuck     Feature   `toml:"stuck" doc:"Asks whether an agent that has worked 10 minutes or more without stopping is stuck, from its screen, and again every 10 minutes; the pill then reads Working · stuck. Sends the visible screen of that agent's pane only."`
}

// Triage is [decisions.triage].
type Triage struct {
	Enabled *bool    `toml:"enabled" doc:"Turn the feature on or off."`
	Holdout *float64 `toml:"holdout" min:"0" max:"1" doc:"Share of triaged panes, 0 to 1, picked at random, whose answer is asked and logged but not used, so pitwall jev report can compare how fast you get to them with and without it. 0 uses every answer."`
}

// Approvals is [decisions.approvals].
type Approvals struct {
	Mode       string   `toml:"mode" enum:"off,suggest" doc:"off, or suggest: show the model's recommendation on the approval pill, with any risk pitwall sees in the call. It never approves or denies anything for you."`
	NeverAllow []string `toml:"never_allow" doc:"More risks to flag: a tool call whose input contains any of these strings gets never_allow #N next to the recommendation (the number, never the text)."`
	Holdout    *float64 `toml:"holdout" min:"0" max:"1" doc:"Share of permission prompts, 0 to 1, picked at random, whose recommendation is asked and logged but not shown, so pitwall jev report can compare answering with and without it. Risk flags always show. 0 shows every recommendation."`
}

// RemovedApprovals are [decisions.approvals] keys of automatic approval,
// which left before release. A config that sets them still loads; each
// gets a note.
var RemovedApprovals = []string{"allow_above", "deny_above", "allow_programs"}

// Feature is a table with a switch and a threshold.
type Feature struct {
	Enabled   *bool    `toml:"enabled" doc:"Turn the feature on or off."`
	Threshold *float64 `toml:"threshold" min:"0.5" max:"1" doc:"How sure the model must be before pitwall acts on its answer."`
}

// Toggle is a table with only a switch.
type Toggle struct {
	Enabled *bool `toml:"enabled" doc:"Turn the feature on or off."`
}

// Approval modes.
const (
	ModeOff     = "off"
	ModeSuggest = "suggest"
)

// Decision defaults.
const (
	DefaultDecideTimeout  = 1.5
	DefaultTurnThreshold  = 0.8
	DefaultStuckThreshold = 0.8
	// DefaultHoldout hides half the recommendations: the even split gets
	// both arms of pitwall jev report to a verdict soonest.
	DefaultHoldout = 0.5
	// DefaultTriageHoldout leaves a fifth of triaged panes untriaged:
	// triage runs many times a day, so a fifth fills the held arm soon.
	DefaultTriageHoldout = 0.2
)

// DecideSettings is [decisions] with every value filled in.
type DecideSettings struct {
	Provider       string // "" means off
	Command        []string
	Model          string
	Timeout        time.Duration
	Approvals      string // ModeOff or ModeSuggest
	NeverAllow     []string
	Holdout        float64 // share of approvals whose recommendation is hidden
	Triage         bool
	TriageHoldout  float64 // share of triaged panes whose answer is not used
	TurnCheck      bool
	TurnThreshold  float64
	Check          bool
	Stuck          bool
	StuckThreshold float64
}

// On reports whether a provider is set, without which no feature runs.
func (d DecideSettings) On() bool { return d.Provider != "" }

func defaultDecisions() Decisions {
	t, tt, st, ho, th := DefaultDecideTimeout, DefaultTurnThreshold, DefaultStuckThreshold, DefaultHoldout, DefaultTriageHoldout
	on, off := true, false
	return Decisions{
		Command: []string{}, Model: "jev-latest", Timeout: &t,
		Approvals: Approvals{Mode: ModeSuggest, NeverAllow: []string{}, Holdout: &ho},
		Triage:    Triage{Enabled: &on, Holdout: &th},
		TurnCheck: Feature{Enabled: &off, Threshold: &tt},
		Check:     Toggle{Enabled: &off},
		Stuck:     Feature{Enabled: &off, Threshold: &st},
	}
}

// resolveDecisions fills in defaults and reports bad values, which keep
// their defaults. notes are for settings that work but should go: mode
// "auto", which now means suggest.
func resolveDecisions(c Decisions) (_ DecideSettings, issues, notes []issue) {
	d := DecideSettings{Provider: c.Provider, Command: c.Command, Model: c.Model, Approvals: c.Approvals.Mode, NeverAllow: c.Approvals.NeverAllow}
	switch d.Provider {
	case "", "jev":
	case "command":
		if len(d.Command) == 0 || d.Command[0] == "" {
			issues = append(issues, issue{"decisions.command", `provider = "command" needs a command; decisions stay off`})
			d.Provider = ""
		}
	default:
		issues = append(issues, issue{"decisions.provider", fmt.Sprintf("%q is not jev, command or \"\"%s; decisions stay off", d.Provider, suggest(d.Provider, []string{"jev", "command"}))})
		d.Provider = ""
	}
	if d.Model == "" {
		d.Model = "jev-latest"
	}
	switch d.Approvals {
	case "":
		d.Approvals = ModeSuggest
	case ModeOff, ModeSuggest:
	case "auto":
		notes = append(notes, issue{"decisions.approvals.mode", `"auto" is no longer supported: pitwall only suggests; using suggest`})
		d.Approvals = ModeSuggest
	default:
		issues = append(issues, issue{"decisions.approvals.mode", fmt.Sprintf("%q is not off or suggest; using suggest", d.Approvals)})
		d.Approvals = ModeSuggest
	}
	num := func(p *float64, path string, def, lo, hi float64) float64 {
		if p == nil {
			return def
		}
		if *p < lo || *p > hi {
			issues = append(issues, issue{path, fmt.Sprintf("%g is outside %g-%g", *p, lo, hi)})
			return def
		}
		return *p
	}
	d.Timeout = time.Duration(num(c.Timeout, "decisions.timeout", DefaultDecideTimeout, 0.2, 10) * float64(time.Second))
	d.TurnThreshold = num(c.TurnCheck.Threshold, "decisions.turn_check.threshold", DefaultTurnThreshold, 0.5, 1)
	d.StuckThreshold = num(c.Stuck.Threshold, "decisions.stuck.threshold", DefaultStuckThreshold, 0.5, 1)
	d.Holdout = num(c.Approvals.Holdout, "decisions.approvals.holdout", DefaultHoldout, 0, 1)
	d.TriageHoldout = num(c.Triage.Holdout, "decisions.triage.holdout", DefaultTriageHoldout, 0, 1)
	d.Triage = c.Triage.Enabled == nil || *c.Triage.Enabled
	d.TurnCheck = c.TurnCheck.Enabled != nil && *c.TurnCheck.Enabled
	d.Check = c.Check.Enabled != nil && *c.Check.Enabled
	d.Stuck = c.Stuck.Enabled != nil && *c.Stuck.Enabled
	return d, issues, notes
}

// LoadDecisions reads only [decisions] from the config at path, for the
// daemon. Problems are the GUI's and `config check`'s to report.
func LoadDecisions(path string) DecideSettings {
	s, _ := LoadFile(path)
	return s.Decisions
}
