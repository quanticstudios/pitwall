package model

// FinishedWorker reports whether every agent pane in a worker reads Done,
// using the same completed state and PR overlay as the sidebar. Shell panes
// do not count, and an agent without an activity has not finished a turn.
func (s State) FinishedWorker(w Workspace) bool {
	if w.Parent == "" || w.Pinned || w.Chat != "" || w.Monitor || s.PRs[w.ID].State == PROpen {
		return false
	}
	agents := s.AgentPanes(w)
	if len(agents) == 0 {
		return false
	}
	for _, ap := range agents {
		if ap.Activity == nil || s.AwaitPR(*ap.Activity).State != StateCompleted {
			return false
		}
	}
	return true
}
