package mobile

import (
	"image"
	"slices"
	"testing"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// inboxApp is an app paired with desktop and box: desktop's billing
// asks to run a command, its since-you-left asks a question with two
// options, box's crisp-borders failed CI, and box has two agents at work.
func inboxApp() *App {
	ms := []phone.Machine{{ID: "m1", Name: "desktop"}, {ID: "m2", Name: "box"}}
	a := &App{th: theme.Dark(), fleet: phone.NewFleet(ms), links: map[string]*link{"m1": {online: true}, "m2": {online: true}}}
	q := &agent.Question{ID: "q", Prompt: "Where should the card live?", Options: []agent.QuestionOption{{Label: "Marker (Recommended)"}, {Label: "Card"}}}
	a.fleet.Set("m1", []remote.Tab{
		{ID: "w1", Item: remote.Item{Pane: "p1", Tab: "billing", State: "pending-approval", Answer: true, At: 5},
			Inbox: []model.InboxItem{{Kind: model.InboxApproval, WorkspaceID: "w1", PaneID: "p1", At: 5, Tab: "billing", Text: "wants to run go test ./..."}}},
		{ID: "w2", Item: remote.Item{Pane: "p2", Tab: "since-you-left", State: "awaiting-input", Detail: "Where should the card live?", Question: q}},
	})
	a.fleet.Set("m2", []remote.Tab{
		{ID: "w3", Item: remote.Item{Pane: "p3", Tab: "crisp-borders", State: "completed"},
			Inbox: []model.InboxItem{{Kind: model.InboxCI, WorkspaceID: "w3", Tab: "crisp-borders", Text: "CI failed: test"}}},
		{ID: "w4", Item: remote.Item{Pane: "p4", Tab: "chat-spinner", State: "working"}},
		{ID: "w5", Item: remote.Item{Pane: "p5", Tab: "question-answer", State: "working"}},
	})
	return a
}

// TestInboxRows checks the section's rows across two machines: the
// answers first in pairing order, a tab from a daemon without the inbox
// read from its state, then the failure, and the agents at work.
func TestInboxRows(t *testing.T) {
	a := inboxApp()
	rows, moving := a.dockRows()
	var got []string
	for _, r := range rows {
		got = append(got, r.m.Name+"/"+r.it.Tab+"/"+string(r.it.Kind))
	}
	want := []string{"desktop/billing/approval", "desktop/since-you-left/question", "box/crisp-borders/ci"}
	if !slices.Equal(got, want) || moving != 2 {
		t.Fatalf("rows %v, moving %d; want %v and 2", got, moving, want)
	}
	if opts := dockOptions(rows[1]); len(opts) != 2 {
		t.Fatalf("question options %+v", opts)
	}
	if opts := dockOptions(rows[0]); opts != nil {
		t.Fatalf("approval options %+v", opts)
	}
}

// TestInboxFolds keeps the section to one line while folded or while
// only agents work, and none at all while nothing waits or works.
func TestInboxFolds(t *testing.T) {
	a := inboxApp()
	height := func() int {
		gtx := layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(360, 2000)}}
		return a.inbox(gtx).Size.Y
	}
	if h := height(); h < 3*int(rowH) {
		t.Fatalf("three items, open, take %d px; want a header and three cards", h)
	}
	a.updates.settings.DockFolded = true
	if h := height(); h >= 2*int(rowH) {
		t.Fatalf("folded, it takes %d px; want one row", h)
	}
	a.updates.settings.DockFolded = false
	a.fleet.Set("m1", nil)
	a.fleet.Set("m2", []remote.Tab{{ID: "w4", Item: remote.Item{Pane: "p4", State: "working"}}})
	if h := height(); h == 0 || h >= 2*int(rowH) {
		t.Fatalf("one agent at work takes %d px; want one line", h)
	}
	a.fleet.Set("m2", nil)
	if h := height(); h != 0 {
		t.Fatalf("nothing waiting takes %d px", h)
	}
}
