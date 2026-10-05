package app

import (
	"bytes"
	"context"
	"log"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func notifyActivity(state model.AgentState, at time.Time) model.Activity {
	return model.Activity{PaneID: "pane", WorkspaceID: "ws", Provider: model.ProviderCodex, State: state, UpdatedAt: at, Unseen: model.NeedsYou(state)}
}

func TestDecideNotificationsStates(t *testing.T) {
	now := time.Unix(100, 0)
	for _, tc := range []struct {
		state model.AgentState
		want  bool
	}{
		{model.StateAwaitingInput, true},
		{model.StatePendingApproval, true},
		{model.StatePlanReady, true},
		{model.StateError, true},
		{model.StateCompleted, true},
		{model.StateWorking, false},
		{model.StateConnecting, false},
		{model.StateTerminalRunning, false},
	} {
		for _, focused := range []bool{false, true} {
			for _, active := range []string{"ws", "other"} {
				t.Run(string(tc.state)+"/"+active+"/"+map[bool]string{true: "focused", false: "unfocused"}[focused], func(t *testing.T) {
					prev, _ := decideNotifications(notificationHistory{}, []model.Activity{notifyActivity(model.StateWorking, now)}, false, "", now)
					next := []model.Activity{notifyActivity(tc.state, now.Add(time.Second))}
					if _, snapshot := decideNotifications(notificationHistory{}, next, focused, active, now); len(snapshot) != 0 {
						t.Fatal("initial snapshot notified")
					}
					_, got := decideNotifications(prev, next, focused, active, now.Add(time.Second))
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
	working := notifyActivity(model.StateWorking, now)
	input := notifyActivity(model.StateAwaitingInput, now.Add(time.Second))
	approval := notifyActivity(model.StatePendingApproval, now.Add(2*time.Second))
	errorActivity := notifyActivity(model.StateError, now.Add(3*time.Second))
	done := notifyActivity(model.StateCompleted, now.Add(2*time.Second))
	seenInput, seenApproval := input, approval
	seenInput.Unseen, seenApproval.Unseen = false, false
	type step struct {
		next    []model.Activity
		at      time.Duration
		focused bool
		active  string
		want    []model.AgentState
		reset   bool
	}
	for _, tc := range []struct {
		name  string
		steps []step
	}{
		{"startup snapshot", []step{{next: []model.Activity{input, approval, errorActivity, done}}}},
		{"duplicates", []step{
			{next: []model.Activity{working}},
			{next: []model.Activity{input}, at: time.Second, want: []model.AgentState{model.StateAwaitingInput}},
			{next: []model.Activity{input}, at: 4 * time.Second},
			{next: []model.Activity{working}, at: 5 * time.Second},
			{next: []model.Activity{input}, at: 6 * time.Second},
		}},
		{"reconnect snapshot", []step{
			{next: []model.Activity{working}},
			{next: []model.Activity{input}, at: time.Second, want: []model.AgentState{model.StateAwaitingInput}},
			{next: []model.Activity{approval}, at: 2 * time.Second, reset: true},
			{next: []model.Activity{errorActivity}, at: 3 * time.Second},
			{next: []model.Activity{errorActivity}, at: 4 * time.Second, want: []model.AgentState{model.StateError}},
		}},
		{"completion requires working", []step{
			{next: []model.Activity{input}},
			{next: []model.Activity{done}, at: 2 * time.Second},
		}},
		{"same state newer timestamp", []step{
			{next: []model.Activity{input}},
			{next: []model.Activity{notifyActivity(model.StateAwaitingInput, now.Add(4*time.Second))}, at: 4 * time.Second},
		}},
		{"seen while focused", []step{
			{next: []model.Activity{working}},
			{next: []model.Activity{input}, at: time.Second, focused: true, active: "ws"},
			{next: []model.Activity{input}, at: 4 * time.Second},
		}},
		{"rate limit and latest wins", []step{
			{next: []model.Activity{working}},
			{next: []model.Activity{input}, at: time.Second, want: []model.AgentState{model.StateAwaitingInput}},
			{next: []model.Activity{approval}, at: 2 * time.Second},
			{next: []model.Activity{errorActivity}, at: 3 * time.Second},
			{next: []model.Activity{errorActivity}, at: 4*time.Second - time.Nanosecond},
			{next: []model.Activity{errorActivity}, at: 4 * time.Second, want: []model.AgentState{model.StateError}},
			{next: []model.Activity{errorActivity}, at: 7 * time.Second},
		}},
		{"pending expires on working", []step{
			{next: []model.Activity{working}},
			{next: []model.Activity{input}, at: time.Second, want: []model.AgentState{model.StateAwaitingInput}},
			{next: []model.Activity{approval}, at: 2 * time.Second},
			{next: []model.Activity{working}, at: 3 * time.Second},
			{next: []model.Activity{working}, at: 4 * time.Second},
		}},
		{"pending expires on removal", []step{
			{next: []model.Activity{working}},
			{next: []model.Activity{input}, at: time.Second, want: []model.AgentState{model.StateAwaitingInput}},
			{next: []model.Activity{approval}, at: 2 * time.Second},
			{at: 4 * time.Second},
		}},
		{"seen never notifies", []step{
			{next: []model.Activity{working}},
			{next: []model.Activity{seenInput}, at: time.Second},
			{next: []model.Activity{seenInput}, at: 5 * time.Second},
		}},
		{"pending expires once seen", []step{
			{next: []model.Activity{working}},
			{next: []model.Activity{input}, at: time.Second, want: []model.AgentState{model.StateAwaitingInput}},
			{next: []model.Activity{approval}, at: 2 * time.Second},
			{next: []model.Activity{seenApproval}, at: 3 * time.Second},
			{next: []model.Activity{seenApproval}, at: 5 * time.Second},
		}},
		{"pending consumed on focus", []step{
			{next: []model.Activity{working}},
			{next: []model.Activity{input}, at: time.Second, want: []model.AgentState{model.StateAwaitingInput}},
			{next: []model.Activity{approval}, at: 2 * time.Second},
			{next: []model.Activity{approval}, at: 3 * time.Second, focused: true, active: "ws"},
			{next: []model.Activity{approval}, at: 4 * time.Second},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var h notificationHistory
			for i, s := range tc.steps {
				if s.reset {
					h.activities = nil
					h.pending = map[string]model.Activity{}
				}
				before := h
				var got []model.Activity
				h, got = decideNotifications(h, s.next, s.focused, s.active, now.Add(s.at))
				var states []model.AgentState
				for _, a := range got {
					states = append(states, a.State)
				}
				if !reflect.DeepEqual(states, s.want) {
					t.Fatalf("step %d: got %v, want %v", i, states, s.want)
				}
				// Repeating the call with the same inputs must give the same result.
				again, repeated := decideNotifications(before, s.next, s.focused, s.active, now.Add(s.at))
				if !reflect.DeepEqual(h, again) || !reflect.DeepEqual(got, repeated) {
					t.Fatal("decision mutated its inputs")
				}
			}
		})
	}
}

func TestDecideNotificationsWorkspaces(t *testing.T) {
	now := time.Unix(100, 0)
	a := notifyActivity(model.StateWorking, now)
	b := a
	b.PaneID = "second"
	c := a
	c.PaneID, c.WorkspaceID = "third", "other"
	h, _ := decideNotifications(notificationHistory{}, []model.Activity{a, b, c}, false, "", now)
	a.State, a.UpdatedAt = model.StateError, now.Add(time.Second)
	b.State, b.UpdatedAt = model.StateAwaitingInput, now.Add(2*time.Second)
	c.State, c.UpdatedAt = model.StatePlanReady, now.Add(time.Second)
	a.Unseen, b.Unseen, c.Unseen = true, true, true
	for _, next := range [][]model.Activity{{a, b, c}, {c, b, a}} {
		_, got := decideNotifications(h, next, false, "", now.Add(2*time.Second))
		if len(got) != 2 || got[0].PaneID != "third" || got[1].PaneID != "second" {
			t.Fatalf("coalescing depends on slice order: %v", got)
		}
	}
}

func TestNotificationCommand(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("checks the notify-send arguments")
	}
	for _, tc := range []struct {
		state          model.AgentState
		label, urgency string
	}{
		{model.StateAwaitingInput, "Input", "normal"},
		{model.StatePendingApproval, "Approval", "critical"},
		{model.StatePlanReady, "Plan Ready", "normal"},
		{model.StateError, "Error", "critical"},
		{model.StateCompleted, "Done", "normal"},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			a := notifyActivity(tc.state, time.Time{})
			a.Detail = strings.Repeat("界", 121) + " $(secret)"
			cmd := notificationCommand(context.Background(), notification{a, "-project / workspace"})
			want := []string{"notify-send", "--app-name=pitwall", "--urgency=" + tc.urgency, "--hint=string:x-canonical-private-synchronous:pitwall-ws", "--", "-project / workspace", tc.label + ": " + strings.Repeat("界", 120)}
			if !reflect.DeepEqual(cmd.Args, want) {
				t.Fatalf("got %q, want %q", cmd.Args, want)
			}
			a.Detail = ""
			cmd = notificationCommand(context.Background(), notification{a, "p / w"})
			if cmd.Args[len(cmd.Args)-1] != tc.label {
				t.Fatal("empty detail adds punctuation")
			}
			a.Detail = "Use $(literal) text"
			cmd = notificationCommand(context.Background(), notification{a, "p / w"})
			if cmd.Args[len(cmd.Args)-1] != tc.label+": "+a.Detail {
				t.Fatal("short detail changed")
			}
		})
	}
	now := notifyActivity(model.StateAwaitingInput, time.Time{})
	now.Urgency = "now"
	if cmd := notificationCommand(context.Background(), notification{now, "p / w"}); cmd.Args[2] != "--urgency=critical" {
		t.Errorf("triaged now: %q", cmd.Args)
	}
	a := notifyActivity(model.StateAwaitingInput, time.Time{})
	a.Provider, a.Detail = model.ProviderTerminal, "tests passed"
	if cmd := notificationCommand(context.Background(), notification{a, "p / w"}); cmd.Args[len(cmd.Args)-1] != "tests passed" {
		t.Fatalf("terminal notification body %q", cmd.Args[len(cmd.Args)-1])
	}
}

func TestDesktopSenderMissing(t *testing.T) {
	// An empty PATH prevents this test from executing notify-send.
	t.Setenv("PATH", t.TempDir())
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	send := desktopSender()
	for range 2 {
		send(context.Background(), notification{})
	}
	if strings.Count(output.String(), " unavailable") != 1 {
		t.Fatalf("missing command logs: %q", output.String())
	}
}

func TestNotifierWithoutWindow(t *testing.T) {
	b := NewFakeBackend()
	delivered := make(chan notification, 20)
	n := newNotifier(b, func() {}, func(_ context.Context, n notification) { delivered <- n })
	defer n.close()
	focused := false
	n.setView(&focused, "", "")
	b.Tick()
	select {
	case got := <-delivered:
		st := b.State()
		ws := findWorkspace(&st, got.activity.WorkspaceID)
		if ws == nil || got.title != notificationTitle(&st, *ws) {
			t.Fatalf("got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend transition did not send without a frame")
	}
}

func TestNotifierCoalescesWithoutFrames(t *testing.T) {
	b := NewFakeBackend()
	set := func(state model.AgentState) {
		b.mu.Lock()
		b.st.Activities = []model.Activity{notifyActivity(state, time.Now())}
		b.st.Activities[0].WorkspaceID = "w1"
		b.bump()
		b.mu.Unlock()
	}
	// Simulate a connected backend's baseline before attaching the notifier.
	set(model.StateWorking)
	delivered := make(chan notification, 10)
	n := newNotifier(b, func() {}, func(_ context.Context, n notification) { delivered <- n })
	defer n.close()
	set(model.StateAwaitingInput)
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("first transition missing")
	}
	started := time.Now()
	set(model.StatePendingApproval)
	set(model.StateError)
	select {
	case got := <-delivered:
		if time.Since(started) < notificationInterval-50*time.Millisecond {
			t.Fatal("rate limit violated")
		}
		if got.activity.State != model.StateError {
			t.Fatalf("got %s, want latest error", got.activity.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("coalesced transition needs a backend change or frame")
	}
}

func TestNotificationTitle(t *testing.T) {
	st := model.State{Projects: []model.Project{{ID: "g", Name: "agents"}}}
	if got := notificationTitle(&st, model.Workspace{Name: "api"}); got != "api" {
		t.Fatalf("ungrouped title = %q", got)
	}
	if got := notificationTitle(&st, model.Workspace{Name: "api", ProjectID: "g"}); got != "agents / api" {
		t.Fatalf("grouped title = %q", got)
	}
	st.Sessions = []model.Session{{ID: "s", Name: "swift-otter"}}
	if got := notificationTitle(&st, model.Workspace{Name: "Migrate billing", SessionID: "s", ProjectID: "g"}); got != "swift-otter · agents / Migrate billing" {
		t.Fatalf("title with a session = %q", got)
	}
}

// Each session's notifications come from one window: the one showing it,
// or for a session no window shows, the window whose session sorts first.
func TestNotifiesOnce(t *testing.T) {
	st := model.State{Sessions: []model.Session{{ID: "a", Windows: 1}, {ID: "b", Windows: 1}, {ID: "c"}}}
	for _, tc := range []struct {
		mine, of string
		want     bool
	}{
		{"a", "a", true}, {"a", "b", false}, {"a", "c", true},
		{"b", "b", true}, {"b", "a", false}, {"b", "c", false},
	} {
		if got := notifies(&st, tc.mine, tc.of); got != tc.want {
			t.Errorf("window on %s notifies about %s: %v", tc.mine, tc.of, got)
		}
	}
	// With no window counted (the fake backend), a window notifies all.
	if !notifies(&model.State{Sessions: []model.Session{{ID: "a"}, {ID: "c"}}}, "c", "a") {
		t.Error("lone window stays quiet")
	}
}

// TestTriagedNotifications: fyi sends nothing, a pending triage holds the
// notification up to triageWait, and the most urgent goes first.
func TestTriagedNotifications(t *testing.T) {
	now := time.Unix(100, 0)
	in := func(ws, urgency string) model.Activity {
		return model.Activity{PaneID: "p" + ws, WorkspaceID: ws, Provider: model.ProviderClaude, State: model.StateAwaitingInput,
			UpdatedAt: now, Unseen: true, Urgency: urgency}
	}
	working := func(ws string) model.Activity {
		return model.Activity{PaneID: "p" + ws, WorkspaceID: ws, Provider: model.ProviderClaude, State: model.StateWorking, UpdatedAt: now.Add(-time.Second)}
	}
	h, _ := decideNotifications(notificationHistory{}, []model.Activity{working("a"), working("b"), working("c"), working("d")}, false, "", now)

	h, got := decideNotifications(h, []model.Activity{in("a", "fyi"), in("b", "later"), in("c", "now"), in("d", model.UrgencyPending)}, false, "", now)
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
	if due := notifyDue(h, "d", h.pending["d"]); !due.Equal(now.Add(triageWait)) {
		t.Errorf("due %v", due)
	}
	// Triage answers fyi: dropped, never sent.
	h2, got := decideNotifications(h, []model.Activity{in("d", "fyi")}, false, "", now.Add(time.Second))
	if len(got) != 0 || len(h2.pending) != 0 {
		t.Errorf("fyi after waiting: delivered %v, pending %v", got, h2.pending)
	}
	// Triage answers soon: sent with it.
	_, got = decideNotifications(h, []model.Activity{in("d", "soon")}, false, "", now.Add(time.Second))
	if len(got) != 1 || got[0].Urgency != "soon" {
		t.Errorf("soon: delivered %v", got)
	}
	// Triage never answers: sent after triageWait.
	h3, got := decideNotifications(h, []model.Activity{in("d", model.UrgencyPending)}, false, "", now.Add(time.Second))
	if len(got) != 0 {
		t.Errorf("sent before triageWait: %v", got)
	}
	if _, got = decideNotifications(h3, []model.Activity{in("d", model.UrgencyPending)}, false, "", now.Add(triageWait)); len(got) != 1 {
		t.Errorf("not sent after triageWait: %v", got)
	}
}
