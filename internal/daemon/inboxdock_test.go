package daemon

import (
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func kinds(in model.Inbox) []model.InboxKind {
	var out []model.InboxKind
	for _, it := range in.Items {
		out = append(out, it.Kind)
	}
	return out
}

// TestInboxOneTab checks one tab's approval: its tab title, the command
// it asks for and the activity's time, which an answer names.
func TestInboxOneTab(t *testing.T) {
	at := time.Unix(100, 0)
	st := model.State{
		Workspaces: []model.Workspace{{ID: "w1", Name: "w-name", Label: "billing"}},
		Activities: []model.Activity{{PaneID: "p1", WorkspaceID: "w1", State: model.StatePendingApproval, Command: "go test ./...", UpdatedAt: at}},
	}
	in := inboxOf(&st)
	want := model.InboxItem{Kind: model.InboxApproval, WorkspaceID: "w1", PaneID: "p1", At: at.UnixNano(), Tab: "billing", Text: "wants to run go test ./...", Command: "go test ./..."}
	if len(in.Items) != 1 || !equalItem(in.Items[0], want) {
		t.Fatalf("inbox %+v", in)
	}
}

// TestInboxSeveralTabs checks the order across tabs: answers oldest
// first, then failed CI, then blocked agents, with working agents counted.
func TestInboxSeveralTabs(t *testing.T) {
	t0 := time.Unix(100, 0)
	st := model.State{
		Workspaces: []model.Workspace{{ID: "a", Name: "a"}, {ID: "b", Name: "b"}, {ID: "c", Name: "c"}, {ID: "d", Name: "d"}, {ID: "e", Name: "e"}},
		Activities: []model.Activity{
			{PaneID: "pa", WorkspaceID: "a", State: model.StateError, Detail: "boom\nmore", UpdatedAt: t0},
			{PaneID: "pb", WorkspaceID: "b", State: model.StateAwaitingInput, Detail: "Where should the card live?", Buttons: []string{"Marker (Recommended)", "Card"}, UpdatedAt: t0.Add(2 * time.Second)},
			{PaneID: "pc", WorkspaceID: "c", State: model.StatePendingApproval, UpdatedAt: t0.Add(time.Second)},
			{PaneID: "pd", WorkspaceID: "d", State: model.StateWorking},
			{PaneID: "pe", WorkspaceID: "e", State: model.StateCompleted},
		},
		Forms: []model.Form{{ID: "f1", WorkspaceID: "e", PaneID: "pe", Title: "Pick a name", Waits: true}, {ID: "f2", WorkspaceID: "e", Title: "dash"}},
		PRs:   map[string]model.PR{"d": {State: model.PROpen, Checks: []model.Check{{Name: "test", State: model.CheckFail}}}, "e": {State: model.PRMerged, Checks: []model.Check{{Name: "x", State: model.CheckFail}}}},
	}
	in := inboxOf(&st)
	want := []model.InboxKind{model.InboxForm, model.InboxApproval, model.InboxQuestion, model.InboxCI, model.InboxBlocked}
	if !slices.Equal(kinds(in), want) {
		t.Fatalf("kinds %v, want %v", kinds(in), want)
	}
	if q := in.Items[2]; q.Text != "Where should the card live?" || !slices.Equal(q.Answers, []string{"Marker (Recommended)", "Card"}) {
		t.Fatalf("question %+v", q)
	}
	if c := in.Items[3]; c.Text != "CI failed: test" || c.WorkspaceID != "d" {
		t.Fatalf("ci %+v", c)
	}
	if b := in.Items[4]; b.Text != "boom" {
		t.Fatalf("blocked %+v", b)
	}
}

// TestInboxAnsweredLeaves checks that an answered approval leaves the
// inbox once its agent works again, as a daemon change recomputes it.
func TestInboxAnsweredLeaves(t *testing.T) {
	d := newDaemon(t, (&fakes{statsCalls: map[string]int{}}).options())
	d.mu.Lock()
	d.st.Workspaces = append(d.st.Workspaces, model.Workspace{ID: "w1", Name: "w1"})
	d.st.Activities = append(d.st.Activities, model.Activity{PaneID: "p1", WorkspaceID: "w1", State: model.StatePendingApproval, UpdatedAt: time.Now()})
	d.changed()
	if n := len(d.st.Inbox.Items); n != 1 {
		d.mu.Unlock()
		t.Fatalf("%d items while asking", n)
	}
	d.st.Activities[len(d.st.Activities)-1].State = model.StateWorking
	d.changed()
	in := d.st.Inbox
	d.mu.Unlock()
	if len(in.Items) != 0 {
		t.Fatalf("inbox %+v after the answer", in)
	}
}

// TestRecommended checks the star's label: the suffix goes, in any case.
func TestRecommended(t *testing.T) {
	for in, want := range map[string]string{"Marker (Recommended)": "Marker", "Card": "Card", "Yes (recommended) ": "Yes"} {
		got, star := model.Recommended(in)
		if got != want || star != (in != "Card") {
			t.Errorf("Recommended(%q) = %q, %v", in, got, star)
		}
	}
}

func equalItem(a, b model.InboxItem) bool {
	return a.Kind == b.Kind && a.WorkspaceID == b.WorkspaceID && a.PaneID == b.PaneID && a.At == b.At && a.Tab == b.Tab && a.Text == b.Text && a.Command == b.Command && slices.Equal(a.Answers, b.Answers) && a.Form == b.Form
}
