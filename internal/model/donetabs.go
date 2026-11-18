package model

// Orchestrator reports whether w is a tab the user opened, not a worker
// that a pane started (Parent is set). Only these show a finished-turn notice in the window.
func Orchestrator(w Workspace) bool { return w.Parent == "" }

// TurnDone reports whether a is a finished agent turn, which shows a notice in the window.
func TurnDone(a Activity) bool {
	return a.State == StateCompleted || a.State == StatePROpen
}
