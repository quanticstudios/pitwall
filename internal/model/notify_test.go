package model

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func activity(state AgentState, at time.Time) Activity {
	return Activity{PaneID: "pane", WorkspaceID: "ws", Provider: ProviderCodex, State: state, UpdatedAt: at, Unseen: NeedsYou(state)}
}

func TestDecideNotificationsStates(t *testing.T) {
	now := time.Unix(100, 0)
	for _, tc := range []struct {
		state AgentState
		want  bool
	}{
		{StateAwaitingInput, true},
		{StatePendingApproval, true},
		{StatePlanReady, true},
		{StateError, true},
		{StateCompleted, true},
		{StateWorking, false},
		{StateConnecting, false},
		{StateTerminalRunning, false},
	} {
		for _, focused := range []bool{false, true} {
			for _, active := range []string{"ws", "other"} {
				t.Run(string(tc.state)+"/"+active+"/"+map[bool]string{true: "focused", false: "unfocused"}[focused], func(t *testing.T) {
					prev, _ := DecideNotifications(Notifications{}, []Activity{activity(StateWorking, now)}, false, "", now)
					next := []Activity{activity(tc.state, now.Add(time.Second))}
					if _, snapshot := DecideNotifications(Notifications{}, next, focused, active, now); len(snapshot) != 0 {
						t.Fatal("initial snapshot notified")
					}
					_, got := DecideNotifications(prev, next, focused, active, now.Add(time.Second))
					want := tc.want && !(focused && active == "ws")
					if (len(got) == 1) != want {
						t.Fatalf("got %v, want delivery %v", got, want)
					}
				})
			}
		}
	}
}

func TestDecideNotificationsHistory(t *testing.T) {
	now := time.Unix(100, 0)
	working := activity(StateWorking, now)
	input := activity(StateAwaitingInput, now.Add(time.Second))
	approval := activity(StatePendingApproval, now.Add(2*time.Second))
	errorActivity := activity(StateError, now.Add(3*time.Second))
	done := activity(StateCompleted, now.Add(2*time.Second))
	seenInput, seenApproval := input, approval
	seenInput.Unseen, seenApproval.Unseen = false, false
	type step struct {
		next    []Activity
		at      time.Duration
		focused bool
		active  string
		want    []AgentState
		reset   bool
	}
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"startup snapshot", []step{{next: []Activity{input, approval, errorActivity, done}}}},
		{"duplicates", []step{
			{next: []Activity{working}},
			{next: []Activity{input}, at: time.Second, want: []AgentState{StateAwaitingInput}},
			{next: []Activity{input}, at: 4 * time.Second},
			{next: []Activity{working}, at: 5 * time.Second},
			{next: []Activity{input}, at: 6 * time.Second},
		}},
		{"reconnect snapshot", []step{
			{next: []Activity{working}},
			{next: []Activity{input}, at: time.Second, want: []AgentState{StateAwaitingInput}},
			{next: []Activity{approval}, at: 2 * time.Second, reset: true},
			{next: []Activity{errorActivity}, at: 3 * time.Second},
			{next: []Activity{errorActivity}, at: 4 * time.Second, want: []AgentState{StateError}},
		}},
		{"completion requires working", []step{
			{next: []Activity{input}},
			{next: []Activity{done}, at: 2 * time.Second},
		}},
		{"same state newer timestamp", []step{
			{next: []Activity{input}},
			{next: []Activity{activity(StateAwaitingInput, now.Add(4*time.Second))}, at: 4 * time.Second},
		}},
		{"seen while focused", []step{
			{next: []Activity{working}},
			{next: []Activity{input}, at: time.Second, focused: true, active: "ws"},
			{next: []Activity{input}, at: 4 * time.Second},
		}},
		{"rate limit and latest wins", []step{
			{next: []Activity{working}},
			{next: []Activity{input}, at: time.Second, want: []AgentState{StateAwaitingInput}},
			{next: []Activity{approval}, at: 2 * time.Second},
			{next: []Activity{errorActivity}, at: 3 * time.Second},
			{next: []Activity{errorActivity}, at: 4*time.Second - time.Nanosecond},
			{next: []Activity{errorActivity}, at: 4 * time.Second, want: []AgentState{StateError}},
			{next: []Activity{errorActivity}, at: 7 * time.Second},
		}},
		{"pending expires on working", []step{
			{next: []Activity{working}},
			{next: []Activity{input}, at: time.Second, want: []AgentState{StateAwaitingInput}},
			{next: []Activity{approval}, at: 2 * time.Second},
			{next: []Activity{working}, at: 3 * time.Second},
			{next: []Activity{working}, at: 4 * time.Second},
		}},
		{"pending expires on removal", []step{
			{next: []Activity{working}},
			{next: []Activity{input}, at: time.Second, want: []AgentState{StateAwaitingInput}},
			{next: []Activity{approval}, at: 2 * time.Second},
			{at: 4 * time.Second},
		}},
		{"seen never notifies", []step{
			{next: []Activity{working}},
			{next: []Activity{seenInput}, at: time.Second},
			{next: []Activity{seenInput}, at: 5 * time.Second},
		}},
		{"pending expires once seen", []step{
			{next: []Activity{working}},
			{next: []Activity{input}, at: time.Second, want: []AgentState{StateAwaitingInput}},
			{next: []Activity{approval}, at: 2 * time.Second},
			{next: []Activity{seenApproval}, at: 3 * time.Second},
			{next: []Activity{seenApproval}, at: 5 * time.Second},
		}},
		{"pending consumed on focus", []step{
			{next: []Activity{working}},
			{next: []Activity{input}, at: time.Second, want: []AgentState{StateAwaitingInput}},
			{next: []Activity{approval}, at: 2 * time.Second},
			{next: []Activity{approval}, at: 3 * time.Second, focused: true, active: "ws"},
			{next: []Activity{approval}, at: 4 * time.Second},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var h Notifications
			for i, s := range tc.steps {
				if s.reset {
					h.Rebase()
				}
				before := h
				var got []Activity
				h, got = DecideNotifications(h, s.next, s.focused, s.active, now.Add(s.at))
				var states []AgentState
				for _, a := range got {
					states = append(states, a.State)
				}
				if !reflect.DeepEqual(states, s.want) {
					t.Fatalf("step %d: got %v, want %v", i, states, s.want)
				}
				// Repeating the call with the same inputs must give the same result.
				again, repeated := DecideNotifications(before, s.next, s.focused, s.active, now.Add(s.at))
				if !reflect.DeepEqual(h, again) || !reflect.DeepEqual(got, repeated) {
					t.Fatal("decision mutated its inputs")
				}
			}
		})
	}
}

func TestDecideNotificationsWorkspaces(t *testing.T) {
	now := time.Unix(100, 0)
	a := activity(StateWorking, now)
	b := a
	b.PaneID = "second"
	c := a
	c.PaneID, c.WorkspaceID = "third", "other"
	h, _ := DecideNotifications(Notifications{}, []Activity{a, b, c}, false, "", now)
	a.State, a.UpdatedAt = StateError, now.Add(time.Second)
	b.State, b.UpdatedAt = StateAwaitingInput, now.Add(2*time.Second)
	c.State, c.UpdatedAt = StatePlanReady, now.Add(time.Second)
	a.Unseen, b.Unseen, c.Unseen = true, true, true
	for _, next := range [][]Activity{{a, b, c}, {c, b, a}} {
		_, got := DecideNotifications(h, next, false, "", now.Add(2*time.Second))
		if len(got) != 2 || got[0].PaneID != "third" || got[1].PaneID != "second" {
			t.Fatalf("coalescing depends on slice order: %v", got)
		}
	}
}

// TestTriagedNotifications: fyi sends nothing, a pending triage holds the
// notification up to TriageWait, and the most urgent goes first.
func TestTriagedNotifications(t *testing.T) {
	now := time.Unix(100, 0)
	in := func(ws, urgency string) Activity {
		return Activity{PaneID: "p" + ws, WorkspaceID: ws, Provider: ProviderClaude, State: StateAwaitingInput,
			UpdatedAt: now, Unseen: true, Urgency: urgency}
	}
	working := func(ws string) Activity {
		return Activity{PaneID: "p" + ws, WorkspaceID: ws, Provider: ProviderClaude, State: StateWorking, UpdatedAt: now.Add(-time.Second)}
	}
	h, _ := DecideNotifications(Notifications{}, []Activity{working("a"), working("b"), working("c"), working("d")}, false, "", now)

	h, got := DecideNotifications(h, []Activity{in("a", "fyi"), in("b", "later"), in("c", "now"), in("d", UrgencyPending)}, false, "", now)
	var order []string
	for _, a := range got {
		order = append(order, a.WorkspaceID)
	}
	if strings.Join(order, ",") != "c,b" {
		t.Fatalf("delivered %v, want c then b (fyi dropped, pending held)", order)
	}
	if _, ok := h.pending["d"]; !ok {
		t.Fatal("the pending triage is not held")
	}
	if due := notifyDue(h, "d", h.pending["d"]); !due.Equal(now.Add(TriageWait)) {
		t.Errorf("due %v", due)
	}
	// Triage answers fyi: dropped, never sent.
	h2, got := DecideNotifications(h, []Activity{in("d", "fyi")}, false, "", now.Add(time.Second))
	if len(got) != 0 || len(h2.pending) != 0 {
		t.Errorf("fyi after waiting: delivered %v, pending %v", got, h2.pending)
	}
	// Triage answers soon: sent with it.
	_, got = DecideNotifications(h, []Activity{in("d", "soon")}, false, "", now.Add(time.Second))
	if len(got) != 1 || got[0].Urgency != "soon" {
		t.Errorf("soon: delivered %v", got)
	}
	// Triage never answers: sent after TriageWait.
	h3, got := DecideNotifications(h, []Activity{in("d", UrgencyPending)}, false, "", now.Add(time.Second))
	if len(got) != 0 {
		t.Errorf("sent before TriageWait: %v", got)
	}
	if _, got = DecideNotifications(h3, []Activity{in("d", UrgencyPending)}, false, "", now.Add(TriageWait)); len(got) != 1 {
		t.Errorf("not sent after TriageWait: %v", got)
	}
}
