package session

import "slices"

// Reconcile is the deltas that bring s in line with a provider process
// that no longer runs (docs/managed-sessions.md, Restart reconciliation):
// running turns end interrupted at now, running tool calls fail, pending
// approvals and inputs resolve, and a running, waiting or connecting
// session is ready and not running. Nil when nothing changes. Like
// Reduce it is pure; the chat loop adds the system lines.
func Reconcile(s Snapshot, now string) []Delta {
	n := s
	n.Turns = slices.Clone(s.Turns)
	for i := range n.Turns {
		if n.Turns[i].State == TurnRunning {
			n.Turns[i].State, n.Turns[i].CompletedAt = TurnInterrupted, ptr(now)
		}
	}
	n.Content = slices.Clone(s.Content)
	for i, it := range n.Content {
		if it.Tool != nil && it.Tool.Status == "running" {
			t := *it.Tool
			t.Status = "failed"
			n.Content[i].Tool, n.Content[i].UpdatedAt = &t, now
		}
	}
	n.PendingApprovals, n.PendingUserInputs, n.PlanApproval = nil, nil, nil
	switch s.SessionState {
	case StateRunning, StateWaiting, StateConnecting:
		n.SessionState, n.Running, n.Detail = StateReady, false, nil
	}
	ds, ok := diff(s, n)
	if !ok {
		return []Delta{{Type: DeltaSnapshot, Snapshot: &n}}
	}
	return ds
}
