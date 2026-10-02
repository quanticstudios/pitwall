package model

import (
	"sort"
	"time"
)

// AgentState values match aide's WorkspaceActivityState strings.
type AgentState string

const (
	StatePendingApproval AgentState = "pending-approval"
	StateAwaitingInput   AgentState = "awaiting-input"
	StateWorking         AgentState = "working"
	StateConnecting      AgentState = "connecting"
	StatePlanReady       AgentState = "plan-ready"
	StateCompleted       AgentState = "completed"
	StateError           AgentState = "error"
	StateTerminalRunning AgentState = "terminal-running"
)

type Activity struct {
	PaneID      string
	WorkspaceID string
	Provider    Provider
	SessionID   string
	State       AgentState
	Detail      string // question text, approval detail, or error message
	UpdatedAt   time.Time
}

// Ported from aide src/shared/workspace-activity.ts.
var statePriority = map[AgentState]int{
	StateError:           7,
	StatePendingApproval: 6,
	StateAwaitingInput:   5,
	StateWorking:         4,
	StateConnecting:      4,
	StatePlanReady:       3,
	StateCompleted:       2,
	StateTerminalRunning: 1,
}

var shortLabels = map[AgentState]string{
	StateAwaitingInput:   "Input",
	StateCompleted:       "Done",
	StateConnecting:      "Connecting",
	StateError:           "Error",
	StatePendingApproval: "Approval",
	StatePlanReady:       "Plan Ready",
	StateTerminalRunning: "Running",
	StateWorking:         "Working",
}

// SortActivities orders by state priority, then most recent first.
func SortActivities(a []Activity) {
	sort.SliceStable(a, func(i, j int) bool {
		pi, pj := statePriority[a[i].State], statePriority[a[j].State]
		if pi != pj {
			return pi > pj
		}
		return a[i].UpdatedAt.After(a[j].UpdatedAt)
	})
}

// Aggregate returns the activity a workspace row shows, or nil.
func Aggregate(a []Activity) *Activity {
	if len(a) == 0 {
		return nil
	}
	s := append([]Activity(nil), a...)
	SortActivities(s)
	return &s[0]
}

type AttentionTier int

const (
	TierAttention AttentionTier = iota
	TierActive
	TierIdle
)

func Tier(a *Activity) AttentionTier {
	if a == nil {
		return TierIdle
	}
	switch a.State {
	case StateError, StatePendingApproval, StateAwaitingInput:
		return TierAttention
	case StateWorking, StateConnecting, StateTerminalRunning:
		return TierActive
	}
	return TierIdle
}

func Pulses(a *Activity) bool {
	return a != nil && (a.State == StateWorking || a.State == StateConnecting)
}

func PillLabel(a Activity) string {
	if a.Provider == ProviderTerminal {
		return shortLabels[a.State]
	}
	return "Agent " + shortLabels[a.State]
}
