package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// A snoozed tab wakes when its agent asks for the user after the snooze,
// not when it works; a settled tab comes back when its agent starts a turn
// or asks after it settled.
func TestWakeTab(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	act := func(s model.AgentState, at time.Time) model.Activity {
		return model.Activity{WorkspaceID: "w", Provider: model.ProviderClaude, State: s, UpdatedAt: at}
	}
	for _, tc := range []struct {
		name            string
		a               model.Activity
		snoozed, settle bool // still snoozed, still settled
	}{
		{"working wakes no snooze", act(model.StateWorking, t0.Add(time.Minute)), true, false},
		{"approval wakes both", act(model.StatePendingApproval, t0.Add(time.Minute)), false, false},
		{"question wakes both", act(model.StateAwaitingInput, t0.Add(time.Minute)), false, false},
		{"plan wakes both", act(model.StatePlanReady, t0.Add(time.Minute)), false, false},
		{"failure wakes both", act(model.StateError, t0.Add(time.Minute)), false, false},
		{"a finished turn wakes neither", act(model.StateCompleted, t0.Add(time.Minute)), true, true},
		{"an old question wakes neither", act(model.StateAwaitingInput, t0.Add(-time.Minute)), true, true},
		{"another tab's question", model.Activity{WorkspaceID: "x", State: model.StateAwaitingInput, UpdatedAt: t0.Add(time.Minute)}, true, true},
	} {
		w := model.Workspace{ID: "w", Settled: t0, SnoozedUntil: t0.Add(time.Hour), SnoozedAt: t0}
		changed := wakeTab(&w, []model.Activity{tc.a})
		if snoozed, settled := !w.SnoozedUntil.IsZero(), !w.Settled.IsZero(); snoozed != tc.snoozed || settled != tc.settle || changed != (!snoozed || !settled) {
			t.Errorf("%s: snoozed %v settled %v changed %v", tc.name, snoozed, settled, changed)
		}
	}
}

// A tab settles once its agents have been quiet for the days, counting
// from the end of a snooze too, and never while pinned, snoozed, settled,
// busy, waiting for an answer, or without an agent.
func TestIdleSettles(t *testing.T) {
	now := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	old := now.Add(-4 * 24 * time.Hour)
	done := []model.Activity{{WorkspaceID: "w", Provider: model.ProviderCodex, State: model.StateCompleted, UpdatedAt: old}}
	with := func(s model.AgentState) []model.Activity {
		return []model.Activity{{WorkspaceID: "w", Provider: model.ProviderCodex, State: s, UpdatedAt: old}}
	}
	base := model.Workspace{ID: "w", UpdatedAt: old}
	for _, tc := range []struct {
		name  string
		w     model.Workspace
		acts  []model.Activity
		agent bool
		days  int
		want  bool
	}{
		{"idle 4 of 3 days", base, done, false, 3, true},
		{"failed and idle", base, with(model.StateError), false, 3, true},
		{"idle 4 of 5 days", base, done, false, 5, false},
		{"off", base, done, false, 0, false},
		{"pinned", model.Workspace{ID: "w", UpdatedAt: old, Pinned: true}, done, false, 3, false},
		{"settled", model.Workspace{ID: "w", UpdatedAt: old, Settled: old}, done, false, 3, false},
		{"snoozed", model.Workspace{ID: "w", UpdatedAt: old, SnoozedUntil: now.Add(time.Hour)}, done, false, 3, false},
		{"woke a day ago", model.Workspace{ID: "w", UpdatedAt: old, SnoozedUntil: now.Add(-24 * time.Hour)}, done, false, 3, false},
		{"working", base, with(model.StateWorking), false, 3, false},
		{"asking", base, with(model.StatePendingApproval), false, 3, false},
		{"a shell", base, []model.Activity{{WorkspaceID: "w", Provider: model.ProviderTerminal, State: model.StateCompleted}}, false, 3, false},
		{"no activity", base, nil, false, 3, false},
		{"an agent at its first prompt, no activity", base, nil, true, 3, true},
		{"an agent pane, working", base, with(model.StateWorking), true, 3, false},
	} {
		if got := idleSettles(tc.w, tc.acts, tc.agent, now, tc.days); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// SetTabInbox settles and snoozes a tab; unsettling restarts its idle
// time, and a new turn takes it out of Settled.
func TestSetTabInbox(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	ws := d.st.Workspaces[0].ID
	wake := time.Now().Add(time.Hour).Round(0)
	must(t, d.handle(ctx, proto.SetTabInbox{WorkspaceID: ws, Settled: true, SnoozedUntil: wake}))
	w := d.st.Workspace(ws)
	if w.Settled.IsZero() || !w.SnoozedUntil.Equal(wake) || w.SnoozedAt.IsZero() {
		t.Fatalf("after settle and snooze: %+v", w)
	}
	settled := w.Settled
	must(t, d.handle(ctx, proto.SetTabInbox{WorkspaceID: ws, Settled: true}))
	if w := d.st.Workspace(ws); !w.Settled.Equal(settled) || !w.SnoozedUntil.IsZero() || !w.SnoozedAt.IsZero() {
		t.Fatalf("after wake: %+v", w)
	}
	d.mu.Lock()
	d.st.Activities = append(d.st.Activities, model.Activity{WorkspaceID: ws, Provider: model.ProviderClaude, State: model.StateWorking, UpdatedAt: time.Now().Add(time.Second)})
	d.changed()
	d.mu.Unlock()
	if w := d.st.Workspace(ws); !w.Settled.IsZero() {
		t.Fatalf("a new turn left the tab settled: %+v", w)
	}
	d.mu.Lock()
	d.st.Activities = nil
	d.mu.Unlock()
	before := time.Now()
	must(t, d.handle(ctx, proto.SetTabInbox{WorkspaceID: ws, Settled: true}))
	must(t, d.handle(ctx, proto.SetTabInbox{WorkspaceID: ws}))
	if w := d.st.Workspace(ws); !w.Settled.IsZero() || w.UpdatedAt.Before(before) {
		t.Fatalf("unsettle: %+v", w)
	}
	if err := d.handle(ctx, proto.SetTabInbox{WorkspaceID: "nope"}); err == nil {
		t.Fatal("no error for a missing tab")
	}
}

// With [sidebar] settle_on_merge on, a tab whose pull request merges
// settles; a pinned one stays.
func TestSettleOnMerge(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		d, _ := autoSendDaemon(t)
		answer := model.PR{Number: 7, State: model.PROpen}
		d.o.PR = func(context.Context, string) (*model.PR, error) { pr := answer; return &pr, nil }
		d.o.Settle = func() config.SidebarSettings { return config.SidebarSettings{SettleOnMerge: true} }
		d.st.Workspaces[0].Branch, d.st.Workspaces[0].Path, d.st.Workspaces[0].Pinned = "feature", "/r", pinned
		now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
		d.pollPRs(context.Background(), now)
		if !d.st.Workspaces[0].Settled.IsZero() {
			t.Fatal("settled while open")
		}
		answer.State = model.PRMerged
		d.pollPRs(context.Background(), now.Add(time.Hour))
		if settled := !d.st.Workspaces[0].Settled.IsZero(); settled == pinned {
			t.Fatalf("pinned %v: settled %v", pinned, settled)
		}
	}
}

// The daemon's settle loop settles a tab whose agent pane has sat idle
// past settle_after_days, with no activity reported, and leaves a fresh
// tab alone.
func TestSettleLoop(t *testing.T) {
	f := &fakes{statsCalls: map[string]int{}}
	d, err := NewWith(f.options())
	if err != nil {
		t.Fatal(err)
	}
	d.o.Settle = func() config.SidebarSettings { return config.SidebarSettings{SettleAfterDays: 3} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 2 {
		must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	}
	d.mu.Lock()
	old, fresh := d.st.Workspaces[0].ID, d.st.Workspaces[1].ID
	d.st.Workspaces[0].UpdatedAt = time.Now().Add(-4 * 24 * time.Hour)
	for _, id := range []string{old, fresh} {
		d.st.Panes = append(d.st.Panes, model.Pane{ID: "p-" + id, WorkspaceID: id, Provider: model.ProviderCodex})
	}
	d.mu.Unlock()
	done := make(chan struct{})
	go func() { d.settleLoop(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		d.mu.Lock()
		settled := !d.st.Workspace(old).Settled.IsZero()
		d.mu.Unlock()
		if settled {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the idle tab did not settle")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if !d.st.Workspace(fresh).Settled.IsZero() {
		t.Fatal("the fresh tab settled")
	}
}
