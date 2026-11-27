package model

import (
	"cmp"
	"slices"
	"strconv"
	"time"
)

// WorkGroup is where a worker sits in its orchestrator's work rail, in
// the order the rail lists the groups.
type WorkGroup int

const (
	WorkNeedsYou WorkGroup = iota // waits on the user: a question, an approval, a failed CI run
	WorkBlocked                   // cannot go on alone: an error, conflicts, a usage limit
	WorkMoving                    // working, or its PR waits on CI or review
	WorkDone                      // finished, merged or closed
)

// Name is the group's heading in the rail.
func (g WorkGroup) Name() string {
	return [...]string{"Needs you", "Blocked", "Moving", "Done"}[g]
}

// Worker is one row of a work rail: a tab another tab started.
type Worker struct {
	ID    string    `json:"id"` // the worker's tab
	Name  string    `json:"name"`
	Group WorkGroup `json:"group"`
	// Chip says where it is in a few words: "1 for you", "CI failed",
	// "review", "working". "unknown" when pitwall has no agent state for it.
	Chip  string    `json:"chip"`
	Since time.Time `json:"since,omitzero"` // when its state started, zero for unknown
	Host  string    `json:"host,omitempty"` // its machine, "" for this one
}

// WorkRail is the tabs parent started, grouped Needs you, Blocked,
// Moving, Done, the newest change first in each group. host names a
// tab's machine, nil for all on this one. Nil when parent started none.
func (s *State) WorkRail(parent string, host func(Workspace) string) []Worker {
	var out []Worker
	for _, w := range s.Workspaces {
		if w.Parent != parent || parent == "" || w.Detached {
			continue
		}
		r := s.worker(w)
		if host != nil {
			r.Host = host(w)
		}
		out = append(out, r)
	}
	slices.SortStableFunc(out, func(a, b Worker) int {
		return cmp.Or(cmp.Compare(a.Group, b.Group), b.Since.Compare(a.Since))
	})
	return out
}

// worker is w's row: its most urgent agent activity, overlaid with its
// pull request.
func (s *State) worker(w Workspace) Worker {
	r := Worker{ID: w.ID, Name: cmp.Or(w.Label, w.Name)}
	if w.NameSet {
		r.Name = w.Name
	}
	var acts []Activity
	for _, a := range s.Activities {
		if a.WorkspaceID == w.ID {
			acts = append(acts, s.AwaitPR(a))
		}
	}
	SortActivities(acts)
	pr, hasPR := s.PRs[w.ID]
	if len(acts) == 0 {
		r.Group, r.Chip = WorkMoving, "unknown"
		if hasPR && (pr.State == PRMerged || pr.State == PRClosed) {
			r.Group, r.Chip = WorkDone, string(pr.State)
		}
		return r
	}
	a := acts[0]
	r.Since = a.UpdatedAt
	asks := 0
	for _, x := range acts {
		if x.State == StatePendingApproval || x.State == StateAwaitingInput || x.State == StatePlanReady {
			asks++
		}
	}
	switch {
	case asks > 0:
		r.Group, r.Chip = WorkNeedsYou, strconv.Itoa(asks)+" for you"
	case a.State == StateError:
		r.Group, r.Chip = WorkBlocked, "error"
	case !a.Resumes.IsZero():
		r.Group, r.Chip = WorkBlocked, "usage limit"
	case a.State == StateWorking && a.Repeats > 0:
		r.Group, r.Chip = WorkBlocked, "failing calls"
	case a.State == StateWorking || a.State == StateConnecting || a.State == StateWatching:
		r.Group, r.Chip = WorkMoving, "working"
		if a.Background {
			r.Chip = "background"
		}
	case a.State == StatePROpen:
		r.Group, r.Chip = prWork(pr)
	case a.State == StateParked:
		r.Group, r.Chip = WorkMoving, "parked"
	case hasPR && pr.State == PRClosed:
		r.Group, r.Chip = WorkDone, "closed"
	case hasPR && pr.State == PRMerged:
		r.Group, r.Chip = WorkDone, "merged"
	case a.State == StateCompleted:
		r.Group, r.Chip = WorkDone, "done"
	default:
		r.Group, r.Chip = WorkMoving, "unknown"
	}
	return r
}

// prWork places a worker whose turn ended with its pull request open.
func prWork(pr PR) (WorkGroup, string) {
	switch pr.Reason {
	case ReasonChecksFailing:
		return WorkNeedsYou, "CI failed"
	case ReasonChangesRequested:
		return WorkNeedsYou, "changes asked"
	case ReasonConflicting:
		return WorkBlocked, "conflicts"
	case ReasonChecksPending:
		return WorkMoving, "CI running"
	case ReasonReviewRequired:
		return WorkMoving, "review"
	case ReasonReady:
		return WorkMoving, "ready to merge"
	}
	return WorkMoving, "PR open"
}

// Age is how long the worker has been in its state, "12m", "3h", "2d";
// "" for unknown.
func (w Worker) Age(now time.Time) string {
	d := now.Sub(w.Since)
	switch {
	case w.Since.IsZero():
		return ""
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d"
}
