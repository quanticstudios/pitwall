package config

import "fmt"

// AgentLimits is [agents].
type AgentLimits struct {
	MaxRunning *float64 `toml:"max_running" min:"0" max:"100" doc:"How many agents may run at once in a session before its queued tasks wait; a queued task starts when one of them finishes (done, failed or closed). 0 sets no limit: a queued task then waits for an agent of its session to finish, or starts at once when none runs"`
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
