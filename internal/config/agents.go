package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// AgentLimits is [agents].
type AgentLimits struct {
	ACP               []ACPAgent    `toml:"acp" doc:"ACP v1 agents for managed chats, each a [[agents.acp]] table. See docs/acp.md"`
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

// Efforts are the effort levels agent takes in a preset, nil when pitwall
// does not set its model and effort.
func Efforts(agent string) []string {
	switch filepath.Base(agent) {
	case "claude":
		return []string{"low", "medium", "high", "xhigh", "max"}
	case "codex", "opencode":
		return []string{"none", "low", "medium", "high", "xhigh", "max"}
	case "pi":
		return []string{"low", "medium", "high", "xhigh"}
	}
	return nil
}

const codexEffort = "model_reasoning_effort="

// SplitArgs reads a preset's args as the model, the effort and the rest,
// as PresetArgs writes them. Args it does not know stay in rest.
func SplitArgs(agent string, args []string) (model, effort string, rest []string) {
	efforts := Efforts(agent)
	if efforts == nil {
		return "", "", args
	}
	// suffix splits "x:high" (pi) or "x#high" (OpenCode) at sep.
	suffix := func(v string, sep string) (string, string) {
		if i := strings.LastIndex(v, sep); i > 0 && slices.Contains(efforts, v[i+1:]) {
			return v[:i], v[i+1:]
		}
		return v, ""
	}
	for i := 0; i < len(args); i++ {
		a, next := args[i], ""
		if i+1 < len(args) {
			next = args[i+1]
		}
		switch base := filepath.Base(agent); {
		case model == "" && next != "" && (a == "--model" || base != "claude" && base != "pi" && a == "-m"):
			model = next
			switch base {
			case "pi":
				model, effort = suffix(model, ":")
			case "opencode":
				model, effort = suffix(model, "#")
			}
			i++
		case effort == "" && base == "claude" && a == "--effort" && slices.Contains(efforts, next):
			effort = next
			i++
		case effort == "" && base == "codex" && a == "-c" && strings.HasPrefix(next, codexEffort) && slices.Contains(efforts, strings.Trim(next[len(codexEffort):], `"'`)):
			effort = strings.Trim(next[len(codexEffort):], `"'`)
			i++
		default:
			rest = append(rest, a)
		}
	}
	return model, effort, rest
}

// PresetArgs is the args that run agent on model at effort, in the agent's
// own flags, then rest. Either may be "".
func PresetArgs(agent, model, effort string, rest []string) []string {
	var out []string
	switch filepath.Base(agent) {
	case "claude":
		if model != "" {
			out = append(out, "--model", model)
		}
		if effort != "" {
			out = append(out, "--effort", effort)
		}
	case "codex":
		if model != "" {
			out = append(out, "-m", model)
		}
		if effort != "" {
			out = append(out, "-c", codexEffort+effort)
		}
	case "pi", "opencode":
		sep := ":"
		if filepath.Base(agent) == "opencode" {
			sep = "#"
		}
		if model != "" && effort != "" {
			model += sep + effort
		}
		if model != "" {
			out = append(out, "--model", model)
		}
	}
	return append(out, rest...)
}

// SplitShell splits s into words as a POSIX shell would, with its single
// and double quotes and backslashes, and no expansions.
func SplitShell(s string) ([]string, error) {
	var out []string
	var w strings.Builder
	in, quote := false, rune(0)
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				w.WriteRune(r)
			}
		case r == '\\' && (quote == 0 || i+1 < len(rs) && strings.ContainsRune(`"\$`+"`", rs[i+1])):
			if i+1 < len(rs) {
				i++
				w.WriteRune(rs[i])
			}
			in = true
		case quote == '"':
			if r == '"' {
				quote = 0
			} else {
				w.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if in {
				out, in = append(out, w.String()), false
				w.Reset()
			}
		default:
			w.WriteRune(r)
			in = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("a %c quote is not closed", quote)
	}
	if in {
		out = append(out, w.String())
	}
	return out, nil
}

// JoinShell is args as one line SplitShell reads back.
func JoinShell(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = a
		if a == "" || strings.ContainsAny(a, " \t\n'\"\\$`*?[]#~;&|<>(){}!") {
			q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(q, " ")
}

// ACPAgent is one [[agents.acp]] entry. Its provider id is acp:<name>.
type ACPAgent struct {
	Name    string   `toml:"name" doc:"Unique name using letters, digits, underscores and hyphens. Select with --provider acp:<name>"`
	Command string   `toml:"command" doc:"Executable to run over ACP stdio, without a shell"`
	Args    []string `toml:"args" doc:"Arguments to the executable, such as [\"--acp\"]"`
	Env     []string `toml:"env" doc:"Environment overrides, each NAME=value. Credentials stay out of the provider list and chat log"`
}

var acpName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func resolveACP(agents []ACPAgent) ([]ACPAgent, []issue) {
	var out []ACPAgent
	var issues []issue
	for _, a := range agents {
		msg := ""
		switch {
		case !acpName.MatchString(a.Name):
			msg = "ACP name must use letters, digits, underscores and hyphens, starting with a letter or digit"
		case strings.TrimSpace(a.Command) == "" || strings.ContainsRune(a.Command, 0):
			msg = "ACP agent needs a command"
		case slices.ContainsFunc(out, func(o ACPAgent) bool { return o.Name == a.Name }):
			msg = "duplicate ACP name"
		case slices.ContainsFunc(a.Args, func(v string) bool { return strings.ContainsRune(v, 0) }):
			msg = "ACP args cannot contain NUL"
		case slices.ContainsFunc(a.Env, func(v string) bool {
			k, _, ok := strings.Cut(v, "=")
			return !ok || k == "" || strings.ContainsRune(v, 0)
		}):
			msg = "ACP env takes NAME=value strings"
		}
		if msg != "" {
			issues = append(issues, issue{"agents.acp", fmt.Sprintf("%s: %q", msg, a.Name)})
			continue
		}
		out = append(out, a)
	}
	return out, issues
}
