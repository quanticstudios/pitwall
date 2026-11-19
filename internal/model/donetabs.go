package model

import "strings"

// Orchestrator reports whether w is a tab the user opened, not a worker
// that a pane started (Parent is set). Only these show a finished-turn notice in the window.
func Orchestrator(w Workspace) bool { return w.Parent == "" }

// TurnDone reports whether a is a finished agent turn, which shows a notice in the window.
func TurnDone(a Activity) bool {
	return a.State == StateCompleted || a.State == StatePROpen
}

// ForUser reports whether a on tab w reaches the user at all: a worker's
// finished turn goes to its orchestrator, which reads it, never to the desktop or phone.
func ForUser(w Workspace, a Activity) bool { return Orchestrator(w) || !TurnDone(a) }

// WaitsOnUser reports whether a waits for the user: a question, an
// approval, a plan or an error. Any tab's, worker or not, shows a notice.
func WaitsOnUser(a Activity) bool {
	switch a.State {
	case StateAwaitingInput, StatePendingApproval, StatePlanReady, StateError:
		return true
	}
	return false
}

// NoticeBody is a notice's second line: the state's label and the first
// line of its detail, as "Approval needed · Bash: rm -rf build".
func NoticeBody(a Activity) string {
	label := PillLabel(a)
	switch a.State {
	case StateAwaitingInput:
		label = "Question"
	case StatePendingApproval:
		label = "Approval needed"
	case StatePlanReady:
		label = "Plan ready"
	}
	d, _, _ := strings.Cut(strings.TrimSpace(a.Detail), "\n")
	if r := []rune(d); len(r) > 160 {
		d = string(r[:160]) + "…"
	}
	if d == "" {
		return label
	}
	return label + " · " + d
}
