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
	// Unseen is set while a needs-you activity (NeedsYou) has not been in
	// the focused pane of a focused window since UpdatedAt.
	Unseen bool
	// Advice is a decision model's recommendation for a pending approval:
	// "allow", "ask" or "deny", AdviceP its probability, and AdviceRule a
	// risk pitwall sees in the call ("sudo"), shown next to it. Advice is
	// "" without a recommendation. Nothing is approved or denied for the
	// user.
	Advice     string
	AdviceP    float64
	AdviceRule string
	// Urgency is triage's level for a needs-you activity: "fyi", "later",
	// "soon" or "now"; UrgencyPending while the answer is on its way; ""
	// when untriaged.
	Urgency string
	// Review marks a finished turn the turn check says needs a look; its
	// pill reads Check instead of Done.
	Review bool
}

// UrgencyPending is Activity.Urgency while triage is asking.
const UrgencyPending = "pending"

// UrgencyRank orders activities for the user's attention: now, soon, an
// untriaged one, later, fyi.
func UrgencyRank(a Activity) int {
	switch a.Urgency {
	case "now":
		return 4
	case "soon":
		return 3
	case "later":
		return 1
	case "fyi":
		return 0
	}
	return 2
}

// NeedsYou reports a state that waits on the user: a question, an
// approval, a plan, an error, or a finished turn.
func NeedsYou(s AgentState) bool {
	switch s {
	case StateAwaitingInput, StatePendingApproval, StatePlanReady, StateError, StateCompleted:
		return true
	}
	return false
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
	l := shortLabels[a.State]
	if a.State == StateCompleted && a.Review {
		l = "Check"
	}
	if a.Provider == ProviderTerminal {
		return l
	}
	return "Agent " + l
}
