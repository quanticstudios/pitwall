package model

// Orchestrator reports whether w is a tab the user opened, not a worker
// that a pane started (Parent is set). The inbox lists only these.
func Orchestrator(w Workspace) bool { return w.Parent == "" }

// InboxDone reports whether a is a finished agent turn the inbox lists.
func InboxDone(a Activity) bool {
	return a.State == StateCompleted || a.State == StatePROpen
}
