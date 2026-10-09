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
			cmd := notificationCommand(context.Background(), notification{activity: a, title: "-project / workspace"}, false)
			want := []string{"notify-send", "--app-name=pitwall", "--urgency=" + tc.urgency, "--hint=string:x-canonical-private-synchronous:pitwall-ws", "--", "-project / workspace", tc.label + ": " + strings.Repeat("界", 120)}
			if !reflect.DeepEqual(cmd.Args, want) {
				t.Fatalf("got %q, want %q", cmd.Args, want)
			}
			a.Detail = ""
			cmd = notificationCommand(context.Background(), notification{activity: a, title: "p / w"}, false)
			if cmd.Args[len(cmd.Args)-1] != tc.label {
				t.Fatal("empty detail adds punctuation")
			}
			a.Detail = "Use $(literal) text"
			cmd = notificationCommand(context.Background(), notification{activity: a, title: "p / w"}, false)
			if cmd.Args[len(cmd.Args)-1] != tc.label+": "+a.Detail {
				t.Fatal("short detail changed")
			}
		})
	}
	now := notifyActivity(model.StateAwaitingInput, time.Time{})
	now.Urgency = "now"
	if cmd := notificationCommand(context.Background(), notification{activity: now, title: "p / w"}, false); cmd.Args[2] != "--urgency=critical" {
		t.Errorf("triaged now: %q", cmd.Args)
	}
	a := notifyActivity(model.StateAwaitingInput, time.Time{})
	a.Provider, a.Detail = model.ProviderTerminal, "tests passed"
	if cmd := notificationCommand(context.Background(), notification{activity: a, title: "p / w"}, false); cmd.Args[len(cmd.Args)-1] != "tests passed" {
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
	n := newNotifier(b, func() {}, func(_ context.Context, n notification) { delivered <- n }, nil)
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
	n := newNotifier(b, func() {}, func(_ context.Context, n notification) { delivered <- n }, nil)
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
		if time.Since(started) < model.NotificationInterval-50*time.Millisecond {
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
