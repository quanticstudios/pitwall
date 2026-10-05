package config

import (
	"fmt"
	"slices"
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
	Approvals Approvals `toml:"approvals" doc:"Permission requests from Claude Code and Codex. Sends the tool, its input, the working directory, the repo root and your latest prompt, with secrets removed."`
	Triage    Toggle    `toml:"triage" doc:"Rates how soon a pane that needs you wants you (fyi, later, soon, now), to order the jump-to-attention key and desktop notifications; fyi sends no notification. Sends the state and the agent's question, approval detail, error or summary."`
	Agents    Agents    `toml:"agents" doc:"Agent status for CLIs without pitwall hooks, read from their screen. Sends the visible screen of those programs only, at most once per pane every 2 seconds while it changes."`
	TurnCheck Feature   `toml:"turn_check" doc:"When an agent finishes a turn, asks whether it needs your review (failed tests, errors, unfinished work); the Done pill then reads Check. Sends the agent's last message only, never the screen."`
}

// Approvals is [decisions.approvals].
type Approvals struct {
	Mode       string   `toml:"mode" enum:"off,suggest" doc:"off, or suggest: show the model's recommendation on the approval pill, with any risk pitwall sees in the call. It never approves or denies anything for you."`
	NeverAllow []string `toml:"never_allow" doc:"More risks to flag: a tool call whose input contains any of these strings gets never_allow #N next to the recommendation (the number, never the text)."`
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

// Agents is [decisions.agents].
type Agents struct {
	Enabled   *bool    `toml:"enabled" doc:"Turn the feature on or off."`
	Threshold *float64 `toml:"threshold" min:"0.5" max:"1" doc:"How sure the model must be before the pane's status changes."`
	Programs  []string `toml:"programs" doc:"More program names to read, besides gemini, opencode, aider, amp, cursor-agent, goose and crush."`
}

// HooklessAgents are the agent CLIs read from their screen by default.
var HooklessAgents = []string{"gemini", "opencode", "aider", "amp", "cursor-agent", "goose", "crush"}

// Approval modes.
const (
	ModeOff     = "off"
	ModeSuggest = "suggest"
)

// Decision defaults.
const (
	DefaultDecideTimeout  = 1.5
	DefaultAgentThreshold = 0.8
	DefaultTurnThreshold  = 0.8
)

// DecideSettings is [decisions] with every value filled in.
type DecideSettings struct {
	Provider       string // "" means off
	Command        []string
	Model          string
	Timeout        time.Duration
	Approvals      string // ModeOff or ModeSuggest
	NeverAllow     []string
	Triage         bool
	Agents         bool
	AgentThreshold float64
	Programs       []string // HooklessAgents and the configured ones
	TurnCheck      bool
	TurnThreshold  float64
}

// On reports whether a provider is set, without which no feature runs.
func (d DecideSettings) On() bool { return d.Provider != "" }

func defaultDecisions() Decisions {
	t, at, tt := DefaultDecideTimeout, DefaultAgentThreshold, DefaultTurnThreshold
	on, off := true, false
	return Decisions{
		Command: []string{}, Model: "jev-latest", Timeout: &t,
		Approvals: Approvals{Mode: ModeSuggest, NeverAllow: []string{}},
		Triage:    Toggle{Enabled: &on},
		Agents:    Agents{Enabled: &off, Threshold: &at, Programs: []string{}},
		TurnCheck: Feature{Enabled: &off, Threshold: &tt},
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
	d.AgentThreshold = num(c.Agents.Threshold, "decisions.agents.threshold", DefaultAgentThreshold, 0.5, 1)
	d.TurnThreshold = num(c.TurnCheck.Threshold, "decisions.turn_check.threshold", DefaultTurnThreshold, 0.5, 1)
	d.Triage = c.Triage.Enabled == nil || *c.Triage.Enabled
	d.Agents = c.Agents.Enabled != nil && *c.Agents.Enabled
	d.TurnCheck = c.TurnCheck.Enabled != nil && *c.TurnCheck.Enabled
	d.Programs = slices.Clone(HooklessAgents)
	for _, p := range c.Agents.Programs {
		if p != "" && !slices.Contains(d.Programs, p) {
			d.Programs = append(d.Programs, p)
		}
	}
	return d, issues, notes
}

// LoadDecisions reads only [decisions] from the config at path, for the
// daemon. Problems are the GUI's and `config check`'s to report.
func LoadDecisions(path string) DecideSettings {
	s, _ := LoadFile(path)
	return s.Decisions
}
