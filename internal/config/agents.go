package config

import (
	"fmt"
	"time"
)

// AgentLimits is [agents].
type AgentLimits struct {
	MaxRunning        *float64 `toml:"max_running" min:"0" max:"100" doc:"How many agents may run at once in a session before its queued tasks wait; a queued task starts when one of them finishes (done, failed or closed). 0 sets no limit: a queued task then waits for an agent of its session to finish, or starts at once when none runs"`
	ResumeAfterLimit  *bool    `toml:"resume_after_limit" doc:"Type continue into a Claude Code or Codex agent that stopped on its plan's usage limit, a minute after the limit resets, unless you typed in its pane since"`
	QuietAfter        *float64 `toml:"quiet_after" min:"0" max:"1440" doc:"Minutes a working agent's screen may stay still before its row says quiet. 0 turns it off"`
	ContinueWhenQuiet *bool    `toml:"continue_when_quiet" doc:"Type continue once into a working agent whose screen has stayed still for quiet_after minutes. Only for agents whose prompts pitwall reads from the screen, and never while one shows"`
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

// resolveAgents is c's max_running, 0 when unset or bad, which is reported.
func resolveAgents(c AgentLimits) (int, []issue) {
	switch v := c.MaxRunning; {
	case v == nil:
		return 0, nil
	case *v < 0 || *v > 100 || *v != float64(int(*v)):
		return 0, []issue{{"agents.max_running", fmt.Sprintf("%g is not a whole number from 0 to 100", *v)}}
	default:
		return int(*v), nil
	}
}
