package model

import (
	"testing"
	"time"
)

func TestWorkRail(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	st := State{
		Workspaces: []Workspace{
			{ID: "orch", Name: "orchestrator"},
			{ID: "a", Name: "since-you-left", NameSet: true, Parent: "orch"},
			{ID: "b", Label: "crisp-borders", Parent: "orch"},
			{ID: "c", Name: "question-answer", Parent: "orch"},
			{ID: "d", Name: "phone-new-agent", Parent: "orch"},
			{ID: "e", Name: "chat-spinner", Parent: "orch"},
			{ID: "f", Name: "conflicted", Parent: "orch"},
			{ID: "g", Name: "merged", Parent: "orch"},
			{ID: "h", Name: "shell-only", Parent: "orch"},
			{ID: "other", Name: "not ours", Parent: "x"},
		},
		Activities: []Activity{
			{WorkspaceID: "a", PaneID: "a1", State: StateAwaitingInput, UpdatedAt: ago(12)},
			{WorkspaceID: "b", PaneID: "b1", State: StateCompleted, UpdatedAt: ago(4)},
			{WorkspaceID: "c", PaneID: "c1", State: StateWorking, UpdatedAt: ago(1)},
			{WorkspaceID: "d", PaneID: "d1", State: StateCompleted, UpdatedAt: ago(8)},
			{WorkspaceID: "e", PaneID: "e1", State: StateWorking, UpdatedAt: ago(2), Repeats: 5},
			{WorkspaceID: "f", PaneID: "f1", State: StateCompleted, UpdatedAt: ago(30)},
			{WorkspaceID: "g", PaneID: "g1", State: StateCompleted, UpdatedAt: ago(60)},
			{WorkspaceID: "other", PaneID: "o1", State: StateWorking},
		},
		PRs: map[string]PR{
			"b": {State: PROpen, Reason: ReasonChecksFailing},
			"d": {State: PROpen, Reason: ReasonReviewRequired},
			"f": {State: PROpen, Reason: ReasonConflicting},
			"g": {State: PRMerged, Reason: ReasonMerged},
		},
	}
	got := st.WorkRail("orch", func(w Workspace) string {
		if w.ID == "c" {
			return "box"
		}
		return ""
	})
	want := []Worker{
		{ID: "b", Name: "crisp-borders", Group: WorkNeedsYou, Chip: "CI failed", Since: ago(4)},
		{ID: "a", Name: "since-you-left", Group: WorkNeedsYou, Chip: "1 for you", Since: ago(12)},
		{ID: "e", Name: "chat-spinner", Group: WorkBlocked, Chip: "failing calls", Since: ago(2)},
		{ID: "f", Name: "conflicted", Group: WorkBlocked, Chip: "conflicts", Since: ago(30)},
		{ID: "c", Name: "question-answer", Group: WorkMoving, Chip: "working", Since: ago(1), Host: "box"},
		{ID: "d", Name: "phone-new-agent", Group: WorkMoving, Chip: "review", Since: ago(8)},
		{ID: "h", Name: "shell-only", Group: WorkMoving, Chip: "unknown"},
		{ID: "g", Name: "merged", Group: WorkDone, Chip: "merged", Since: ago(60)},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if r := st.WorkRail("a", nil); r != nil {
		t.Errorf("a tab without workers has a rail: %+v", r)
	}
	if r := st.WorkRail("", nil); r != nil {
		t.Errorf("the empty parent has a rail: %+v", r)
	}
}

func TestWorkerAge(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		ago  time.Duration
		want string
	}{{30 * time.Second, "now"}, {12 * time.Minute, "12m"}, {3 * time.Hour, "3h"}, {72 * time.Hour, "3d"}} {
		if got := (Worker{Since: now.Add(-c.ago)}).Age(now); got != c.want {
			t.Errorf("Age(%v) = %q, want %q", c.ago, got, c.want)
		}
	}
	if got := (Worker{}).Age(now); got != "" {
		t.Errorf("unknown Age = %q", got)
	}
}
