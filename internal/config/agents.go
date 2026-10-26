package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// AgentLimits is [agents].
type AgentLimits struct {
	MaxRunning        *float64      `toml:"max_running" min:"0" max:"100" doc:"How many agents may run at once in a session before its queued tasks wait; a queued task starts when one of them finishes (done, failed or closed). 0 sets no limit: a queued task then waits for an agent of its session to finish, or starts at once when none runs"`
	ResumeAfterLimit  *bool         `toml:"resume_after_limit" doc:"Type continue into a Claude Code or Codex agent that stopped on its plan's usage limit, a minute after the limit resets, unless you typed in its pane since"`
	QuietAfter        *float64      `toml:"quiet_after" min:"0" max:"1440" doc:"Minutes a working agent's screen may stay still before its row says quiet. 0 turns it off"`
	ContinueWhenQuiet *bool         `toml:"continue_when_quiet" doc:"Type continue once into a working agent whose screen has stayed still for quiet_after minutes. Only for agents whose prompts pitwall reads from the screen, and never while one shows"`
	GuardWorkers      *bool         `toml:"guard_workers" doc:"Turn the guard on in every tab an agent opens with pitwall worktree new, as --guard does. See docs/usage.md"`
	GuardDeny         []string      `toml:"guard_deny" doc:"Shell commands the guard denies on top of its own list (git push, gh pr merge, terraform apply and the like), as patterns: * is any run, ? one character, and a trailing \" *\" also matches the bare command, as in \"npm publish *\". It cannot lift a built-in one"`
	Preset            []AgentPreset `toml:"preset" doc:"Named agent setups, each a [[agents.preset]] table, that the New task dialog lists after the plain agents and pitwall new --preset starts"`
}

// DefaultQuietAfter is [agents] quiet_after's default, in minutes.
const DefaultQuietAfter = 20

// resolveQuiet is c's quiet_after, DefaultQuietAfter when unset or bad,
// which is reported.
func resolveQuiet(c AgentLimits) (time.Duration, []issue) {
	switch v := c.QuietAfter; {
	case v == nil:
		return DefaultQuietAfter * time.Minute, nil
	case *v < 0 || *v > 1440:
		return DefaultQuietAfter * time.Minute, []issue{{"agents.quiet_after", fmt.Sprintf("%g is not a number of minutes from 0 to 1440", *v)}}
	default:
		return time.Duration(*v * float64(time.Minute)), nil
	}
}

// AgentPreset is one [[agents.preset]] entry.
type AgentPreset struct {
	Name  string   `toml:"name" doc:"What the New task dialog lists and --preset takes"`
	Agent string   `toml:"agent" doc:"The agent's command: claude, codex, pi, opencode and so on"`
	Args  []string `toml:"args" doc:"Arguments before the prompt, such as [\"--model\", \"opus\", \"--effort\", \"high\"]. A resumed agent keeps the ones its CLI documents"`
	Env   []string `toml:"env" doc:"Environment variables for the agent, each \"NAME=value\""`
	Guard bool     `toml:"guard" doc:"Deny pushes, merges and applies to the agent; see guard_deny"`
}

// resolveAgents is c's max_running, 0 when unset or bad, and its presets,
// leaving out the ones with problems. Both report problems.
func resolveAgents(c AgentLimits) (int, []AgentPreset, []issue) {
	var issues []issue
	n := 0
	switch v := c.MaxRunning; {
	case v == nil:
	case *v < 0 || *v > 100 || *v != float64(int(*v)):
		issues = append(issues, issue{"agents.max_running", fmt.Sprintf("%g is not a whole number from 0 to 100", *v)})
	default:
		n = int(*v)
	}
	var presets []AgentPreset
	for _, p := range c.Preset {
		msg := ""
		switch {
		case strings.TrimSpace(p.Name) == "":
			msg = "a preset needs a name"
		case strings.TrimSpace(p.Agent) == "":
			msg = fmt.Sprintf("preset %q needs an agent", p.Name)
		case slices.ContainsFunc(presets, func(o AgentPreset) bool { return o.Name == p.Name }):
			msg = fmt.Sprintf("a second preset is named %q", p.Name)
		case slices.ContainsFunc(p.Env, func(e string) bool { k, _, ok := strings.Cut(e, "="); return !ok || k == "" }):
			msg = fmt.Sprintf("preset %q: env takes \"NAME=value\" strings", p.Name)
		}
		if msg != "" {
			issues = append(issues, issue{"agents.preset", msg})
			continue
		}
		presets = append(presets, p)
	}
	return n, presets, issues
}

// FindPreset is the preset named name.
func (s Settings) FindPreset(name string) (AgentPreset, bool) {
	i := slices.IndexFunc(s.Presets, func(p AgentPreset) bool { return p.Name == name })
	if i < 0 {
		return AgentPreset{}, false
	}
	return s.Presets[i], true
}
