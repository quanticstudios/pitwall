package app

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
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
	n := newNotifier(b, config.DefaultNotifySettings(), func() {}, func(_ context.Context, n notification) { delivered <- n }, nil, nil)
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
	n := newNotifier(b, config.DefaultNotifySettings(), func() {}, func(_ context.Context, n notification) { delivered <- n }, nil, nil)
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

func TestNotificationTitleNamesSharedTabHost(t *testing.T) {
	b := FakeShared{FakeBackend: NewFakeBackend(), Tab: "w1"}
	st := b.State()
	for _, id := range []string{"w1", "w2"} {
		w := findWorkspace(&st, id)
		want := notificationTitle(&st, *w)
		if id == "w1" {
			want = "box · " + want
		}
		if got := notificationTitleOn(b, &st, *w); got != want {
			t.Errorf("%s notification title = %q, want %q", id, got, want)
		}
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

// Completion routing is installed before the notifier starts, including
// when the backend changes between its initial State and Changed calls.
type finishingBackend struct{ *FakeBackend }

func (b finishingBackend) Changed() <-chan struct{} {
	b.mu.Lock()
	if b.st.Activities[0].State == model.StateWorking {
		b.st.Activities[0].State = model.StateCompleted
		b.st.Activities[0].Unseen = true
		b.st.Activities[0].UpdatedAt = time.Now()
		b.bump()
	}
	b.mu.Unlock()
	return b.FakeBackend.Changed()
}

func TestNotifierFinishedCallbackReadyAtStartup(t *testing.T) {
	b := finishingBackend{NewFakeBackend()}
	b.mu.Lock()
	b.st.Activities = []model.Activity{notifyActivity(model.StateWorking, time.Now())}
	b.st.Activities[0].WorkspaceID = "w1"
	b.mu.Unlock()
	delivered := make(chan notification, 1)
	finished := make(chan doneNote, 1)
	n := newNotifier(b, config.DefaultNotifySettings(), func() {}, func(_ context.Context, nt notification) { delivered <- nt }, nil,
		func(a model.Activity, title string) { finished <- doneNote{a, title} })
	defer n.close()
	select {
	case note := <-finished:
		if note.act.WorkspaceID != "w1" || note.title == "" {
			t.Fatalf("finished = %+v", note)
		}
	case nt := <-delivered:
		t.Fatalf("startup completion reached desktop: %+v", nt)
	case <-time.After(2 * time.Second):
		t.Fatal("missing startup completion")
	}
}

func TestNotifierRulesReadyAtStartup(t *testing.T) {
	b := finishingBackend{NewFakeBackend()}
	b.mu.Lock()
	b.st.Activities = []model.Activity{notifyActivity(model.StateWorking, time.Now())}
	b.st.Activities[0].WorkspaceID = "w1"
	b.mu.Unlock()
	rules := config.DefaultNotifySettings()
	rules.Done = false
	changed := make(chan struct{}, 1)
	delivered := make(chan notification, 1)
	finished := make(chan doneNote, 1)
	n := newNotifier(b, rules, func() { changed <- struct{}{} }, func(_ context.Context, nt notification) { delivered <- nt }, nil,
		func(a model.Activity, title string) { finished <- doneNote{a, title} })
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		n.close()
		t.Fatal("startup completion was not processed")
	}
	n.close() // wait for the delivery decision before inspecting the channels
	if len(delivered) != 0 || len(finished) != 0 {
		t.Fatal("startup completion ignored disabled done notifications")
	}
}

func TestNotifierRoutesFinishedTabsAtStartup(t *testing.T) {
	for _, tc := range []struct {
		name, parent, session, mine string
		sound                       bool
		wantWindow                  bool
	}{
		{name: "user tab", session: "s1", mine: "s1", wantWindow: true},
		{name: "worker", parent: "parent", session: "s1", mine: "s1"},
		{name: "session without a window", session: "s2", mine: "s1", wantWindow: true},
		{name: "configured sound", session: "s1", mine: "s1", sound: true, wantWindow: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := finishingBackend{NewFakeBackend()}
			b.mu.Lock()
			b.st.Activities = []model.Activity{notifyActivity(model.StateWorking, time.Now())}
			b.st.Activities[0].WorkspaceID = "w1"
			for i := range b.st.Workspaces {
				if b.st.Workspaces[i].ID == "w1" {
					b.st.Workspaces[i].Parent = tc.parent
					b.st.Workspaces[i].SessionID = tc.session
				}
			}
			b.st.Sessions[0].Windows = 1
			b.mu.Unlock()
			delivered := make(chan notification, 4)
			finished := make(chan doneNote, 4)
			// Configure the notifier before run to make sound and ownership
			// deterministic while the completion arrives on its first read.
			ctx, cancel := context.WithCancel(context.Background())
			n := &notifier{session: tc.mine, rules: config.DefaultNotifySettings(), done: make(chan struct{}), cancel: cancel,
				finished: func(a model.Activity, title string) { finished <- doneNote{a, title} }}
			if tc.sound {
				n.rules.Sound = "configured.oga"
			}
			go n.run(ctx, b, b.State(), func() {}, func(_ context.Context, nt notification) { delivered <- nt })
			defer n.close()
			if tc.wantWindow {
				select {
				case note := <-finished:
					if note.act.WorkspaceID != "w1" || note.title == "" {
						t.Fatalf("finished = %+v", note)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("missing in-window notice")
				}
				if tc.sound {
					select {
					case nt := <-delivered:
						if !nt.soundOnly || nt.sound != n.rules.Sound {
							t.Fatalf("sound = %+v", nt)
						}
					case <-time.After(2 * time.Second):
						t.Fatal("missing completion sound")
					}
				}
			} else {
				select {
				case nt := <-delivered:
					if nt.soundOnly || nt.activity.WorkspaceID != "w1" {
						t.Fatalf("desktop = %+v", nt)
					}
				case <-finished:
					t.Fatal("worker reached window notice list")
				case <-time.After(2 * time.Second):
					t.Fatal("missing desktop notification")
				}
			}
		})
	}
}

func TestSoundOnlySkipsDesktopCommand(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var output bytes.Buffer
	old := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(old)
	send := desktopSender() // Windows logs its toast preload here, before the call under test
	output.Reset()
	send(context.Background(), notification{soundOnly: true})
	if output.Len() != 0 {
		t.Fatalf("sound-only delivery attempted desktop command: %q", output.String())
	}
}

// Only the daemon's resumed activity notifies, once.
func TestResumedAt(t *testing.T) {
	at := time.Date(2026, 10, 22, 15, 1, 0, 0, time.UTC)
	stopped := notifyActivity(model.StateError, at.Add(-2*time.Hour))
	stopped.Resumes = at
	seen := map[string]time.Time{}
	if got := resumedAt(seen, []model.Activity{stopped}); len(got) != 0 {
		t.Fatalf("a pending resume notified: %+v", got)
	}
	cancelled := notifyActivity(model.StateError, at.Add(-2*time.Hour)) // its Resumes gone at the deadline
	if got := resumedAt(seen, []model.Activity{cancelled}); len(got) != 0 {
		t.Fatalf("a cancelled resume notified: %+v", got)
	}
	done := notifyActivity(model.StateWorking, at)
	done.Detail = model.ResumedDetail
	if got := resumedAt(seen, []model.Activity{done}); len(got) != 1 || got[0].PaneID != "pane" {
		t.Fatalf("got %+v", got)
	}
	if got := resumedAt(seen, []model.Activity{done}); len(got) != 0 {
		t.Fatalf("notified twice: %+v", got)
	}
}

// approvingBackend's first pane goes from working to waiting for approval.
type approvingBackend struct{ *FakeBackend }

func (b approvingBackend) Changed() <-chan struct{} {
	b.mu.Lock()
	if b.st.Activities[0].State == model.StateWorking {
		b.st.Activities[0].State = model.StatePendingApproval
		b.st.Activities[0].Unseen = true
		b.st.Activities[0].UpdatedAt = time.Now()
		b.bump()
	}
	b.mu.Unlock()
	return b.FakeBackend.Changed()
}

// A tab waiting on the user, a worker's too, shows a notice in the window.
// With the window in the background it also goes to the desktop.
func TestNotifierWaitingTabNotice(t *testing.T) {
	for _, focused := range []bool{true, false} {
		b := approvingBackend{NewFakeBackend()}
		b.mu.Lock()
		b.st.Activities = []model.Activity{notifyActivity(model.StateWorking, time.Now())}
		b.st.Activities[0].WorkspaceID = "w1"
		for i := range b.st.Workspaces {
			if b.st.Workspaces[i].ID == "w1" {
				b.st.Workspaces[i].Parent = "parent" // a worker
			}
		}
		sess := b.st.Workspaces[0].SessionID
		b.mu.Unlock()
		delivered := make(chan notification, 4)
		finished := make(chan doneNote, 4)
		ctx, cancel := context.WithCancel(context.Background())
		n := &notifier{focused: focused, active: "other", session: sess, rules: config.DefaultNotifySettings(), done: make(chan struct{}), cancel: cancel,
			finished: func(a model.Activity, title string) { finished <- doneNote{a, title} }}
		go n.run(ctx, b, b.State(), func() {}, func(_ context.Context, nt notification) { delivered <- nt })
		select {
		case note := <-finished:
			if note.act.State != model.StatePendingApproval {
				t.Fatalf("focused=%v: notice = %+v", focused, note)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("focused=%v: missing in-window notice", focused)
		}
		select {
		case nt := <-delivered:
			if focused {
				t.Fatalf("focused window also sent a desktop notification: %+v", nt)
			}
		case <-time.After(300 * time.Millisecond):
			if !focused {
				t.Fatal("background window sent no desktop notification")
			}
		}
		n.close()
	}
}

// Focus can change while a batch is being delivered. Each tab must use
// the current focus when deciding whether its card also needs the desktop.
func TestNotifierWaitingBatchFocusChange(t *testing.T) {
	for _, focused := range []bool{true, false} {
		t.Run(fmt.Sprint(focused), func(t *testing.T) {
			b := approvingBackend{NewFakeBackend()}
			b.mu.Lock()
			first := notifyActivity(model.StateWorking, time.Now())
			first.WorkspaceID = "w1"
			second := first
			second.PaneID, second.WorkspaceID = "other-pane", "w2"
			second.State = model.StatePendingApproval
			second.Unseen = true
			// Only the first activity is in the baseline. Both waits arrive
			// together when approvingBackend.Changed transitions it.
			b.st.Activities = []model.Activity{first, second}
			sess := b.st.Workspaces[0].SessionID
			b.mu.Unlock()
			initial := b.State()
			initial.Activities = initial.Activities[:1]
			ctx, cancel := context.WithCancel(context.Background())
			delivered := make(chan notification, 4)
			finished := make(chan doneNote, 4)
			n := &notifier{focused: focused, active: "other", session: sess, rules: config.DefaultNotifySettings(), done: make(chan struct{}), cancel: cancel}
			n.rules.Sound = "configured.oga"
			n.finished = func(a model.Activity, title string) {
				finished <- doneNote{a, title}
				if a.WorkspaceID == "w1" {
					next := !focused
					n.setView(&next, "", "")
				}
			}
			go n.run(ctx, b, initial, func() {}, func(_ context.Context, nt notification) { delivered <- nt })
			for range 2 {
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					n.close()
					t.Fatal("missing waiting notice")
				}
			}
			n.close()
			if len(delivered) != 2 {
				t.Fatalf("got %d deliveries, want one sound per notice", len(delivered))
			}
			firstDelivery, secondDelivery := <-delivered, <-delivered
			if firstDelivery.soundOnly != focused || secondDelivery.soundOnly == focused || firstDelivery.sound != n.rules.Sound || secondDelivery.sound != n.rules.Sound {
				t.Fatalf("focused=%v: first=%+v second=%+v", focused, firstDelivery, secondDelivery)
			}
		})
	}
}
