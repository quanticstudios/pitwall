package config

import (
	"fmt"
	"slices"
	"strings"
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
	TurnCheck Feature   `toml:"turn_check" doc:"When an agent finishes a turn, asks whether it needs your review (failed tests, errors, unfinished work); the Done pill then reads Check. Sends the agent's last message and its screen."`
}

// Approvals is [decisions.approvals].
type Approvals struct {
	Mode          string   `toml:"mode" enum:"off,suggest,auto" doc:"off; suggest shows the model's recommendation on the approval pill and decides nothing; auto approves only calls on pitwall's allowlist (file tools inside the repo, plain read, build and test commands, see the README), when the model chose allow at allow_above or more, denies when it chose deny at deny_above or more, and leaves everything else to you."`
	AllowAbove    *float64 `toml:"allow_above" min:"0.8" max:"1" doc:"In auto mode, approve when the probability of allow is at least this."`
	DenyAbove     *float64 `toml:"deny_above" min:"0.8" max:"1" doc:"In auto mode, deny when the probability of deny is at least this."`
	AllowPrograms []string `toml:"allow_programs" doc:"More programs auto mode may approve, as bare names (no slashes). Their arguments must still pass the path rules, but their flags are not checked: adding one is at your own risk."`
	NeverAllow    []string `toml:"never_allow" doc:"More hard rules: a tool call whose input contains any of these strings is never approved automatically. Shown as never_allow #N, never the text."`
}

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
	ModeAuto    = "auto"
)

// Decision defaults.
const (
	DefaultDecideTimeout  = 1.5
	DefaultAllowAbove     = 0.95
	DefaultDenyAbove      = 0.95
	DefaultAgentThreshold = 0.8
	DefaultTurnThreshold  = 0.8
)

// DecideSettings is [decisions] with every value filled in.
type DecideSettings struct {
	Provider       string // "" means off
	Command        []string
	Model          string
	Timeout        time.Duration
	Approvals      string // ModeOff, ModeSuggest or ModeAuto
	AllowAbove     float64
	DenyAbove      float64
	NeverAllow     []string
	AllowPrograms  []string // bare program names the user added to the allowlist
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
	t, a, dn, at, tt := DefaultDecideTimeout, DefaultAllowAbove, DefaultDenyAbove, DefaultAgentThreshold, DefaultTurnThreshold
	on, off := true, false
	return Decisions{
		Command: []string{}, Model: "jev-latest", Timeout: &t,
		Approvals: Approvals{Mode: ModeSuggest, AllowAbove: &a, DenyAbove: &dn, AllowPrograms: []string{}, NeverAllow: []string{}},
		Triage:    Toggle{Enabled: &on},
		Agents:    Agents{Enabled: &off, Threshold: &at, Programs: []string{}},
		TurnCheck: Feature{Enabled: &off, Threshold: &tt},
	}
}

// resolveDecisions fills in defaults and reports bad values, which keep
// their defaults.
func resolveDecisions(c Decisions) (DecideSettings, []issue) {
	var issues []issue
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
	case ModeOff, ModeSuggest, ModeAuto:
	default:
		issues = append(issues, issue{"decisions.approvals.mode", fmt.Sprintf("%q is not off, suggest or auto; using suggest", d.Approvals)})
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
	d.AllowAbove = num(c.Approvals.AllowAbove, "decisions.approvals.allow_above", DefaultAllowAbove, 0.8, 1)
	d.DenyAbove = num(c.Approvals.DenyAbove, "decisions.approvals.deny_above", DefaultDenyAbove, 0.8, 1)
	d.AgentThreshold = num(c.Agents.Threshold, "decisions.agents.threshold", DefaultAgentThreshold, 0.5, 1)
	d.TurnThreshold = num(c.TurnCheck.Threshold, "decisions.turn_check.threshold", DefaultTurnThreshold, 0.5, 1)
	for _, name := range c.Approvals.AllowPrograms {
		if name == "" || strings.ContainsAny(name, `/\= `) {
			issues = append(issues, issue{"decisions.approvals.allow_programs", fmt.Sprintf("%q is not a bare program name; it is ignored", name)})
			continue
		}
		d.AllowPrograms = append(d.AllowPrograms, name)
	}
	d.Triage = c.Triage.Enabled == nil || *c.Triage.Enabled
	d.Agents = c.Agents.Enabled != nil && *c.Agents.Enabled
	d.TurnCheck = c.TurnCheck.Enabled != nil && *c.TurnCheck.Enabled
	d.Programs = slices.Clone(HooklessAgents)
	for _, p := range c.Agents.Programs {
		if p != "" && !slices.Contains(d.Programs, p) {
			d.Programs = append(d.Programs, p)
		}
	}
	return d, issues
}

// LoadDecisions reads only [decisions] from the config at path, for the
// daemon. Problems are the GUI's and `config check`'s to report.
func LoadDecisions(path string) DecideSettings {
	s, _ := LoadFile(path)
	return s.Decisions
}
