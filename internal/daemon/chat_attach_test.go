package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// chatDaemon is a daemon whose Claude chats run on a stub driver.
func chatDaemon(t *testing.T, edit func(*Options)) (*Daemon, *stubProvider) {
	t.Helper()
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	prov := &stubProvider{}
	o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: prov}
	if edit != nil {
		edit(&o)
	}
	return newDaemon(t, o), prov
}

// chatDo sends m to d's chat loop and returns its ChatResult.
func chatDo(t *testing.T, d *Daemon, m any) proto.ChatResult {
	t.Helper()
	r, ok := d.chatRequest(context.Background(), m)
	if !ok {
		t.Fatalf("%T is not a chat request", m)
	}
	return r.(proto.ChatResult)
}

func chatTab(t *testing.T, d *Daemon, id string) model.Workspace {
	t.Helper()
	st := d.state()
	i := slices.IndexFunc(st.Workspaces, func(w model.Workspace) bool { return w.Chat == id })
	if i < 0 {
		t.Fatalf("no tab for chat %s", id)
	}
	return st.Workspaces[i]
}

// A chat in a new worktree gets the tab's port block and its own id as
// PITWALL_PANE; its hooks change nothing, and its id names its tab.
func TestChatWorktreePorts(t *testing.T) {
	d, prov := chatDaemon(t, func(o *Options) {
		o.Worktrees = func(string) (config.WorktreeSettings, []config.Problem) {
			return config.WorktreeSettings{PortBase: 4000, PortStep: 10, Setup: "make"}, nil
		}
	})
	repo := filepath.Join(t.TempDir(), "repo")
	mkdir(t, repo)
	r := chatDo(t, d, proto.ChatNew{Dir: repo, Worktree: "feat"})
	if r.Code != "" || !strings.Contains(r.Message, "setup not run") {
		t.Fatalf("new: %+v", r)
	}
	ws, cfg := chatTab(t, d, r.Chat), prov.driver(t).cfg
	if ws.Ports != (model.PortBlock{First: 4010, Last: 4019}) || ws.WorktreeRoot != repo || cfg.Cwd != filepath.Join(repo, ".worktrees", "feat") {
		t.Fatalf("tab %+v, cwd %s", ws, cfg.Cwd)
	}
	for _, want := range []string{"PORT=4010", "PITWALL_PANE=" + r.Chat} {
		if !slices.Contains(cfg.Env, want) {
			t.Errorf("env %q lacks %s", cfg.Env, want)
		}
	}
	must(t, d.handle(context.Background(), proto.AgentEvent{Pane: r.Chat, Provider: model.ProviderClaude,
		Payload: []byte(`{"session_id":"s","hook_event_name":"UserPromptSubmit","prompt":"x"}`)}))
	if st := d.state(); len(st.Activities) != 0 {
		t.Errorf("a chat's hook made an activity: %+v", st.Activities)
	}
	d.mu.Lock()
	tab := d.paneTab(r.Chat)
	d.mu.Unlock()
	if tab == nil || tab.ID != ws.ID {
		t.Errorf("paneTab(chat) = %+v, want %s", tab, ws.ID)
	}
}

// Each turn takes the tab's checkpoint before the driver gets the prompt.
func TestChatCheckpoint(t *testing.T) {
	var mu sync.Mutex
	var got []string
	var prov *stubProvider
	d, prov := chatDaemon(t, func(o *Options) {
		o.Checkpoint = func(_ context.Context, dir, ws string) error {
			prov.mu.Lock()
			sent := len(prov.drivers[0].got())
			prov.mu.Unlock()
			mu.Lock()
			got = append(got, fmt.Sprintf("%s %s after %d sends", dir, ws, sent))
			mu.Unlock()
			return nil
		}
	})
	dir := t.TempDir()
	r := chatDo(t, d, proto.ChatNew{Dir: dir, Prompt: "fix it"})
	drv := prov.driver(t)
	drv.end(session.TurnCompleted)
	waitUntil(t, "turn over", func() bool { return d.activityOf(r.Chat).State == model.StateCompleted })
	chatDo(t, d, proto.ChatSend{Chat: r.Chat, Text: "and the tests"})
	ws := chatTab(t, d, r.Chat)
	st := d.state()
	mu.Lock()
	defer mu.Unlock()
	want := []string{st.LivePath(ws) + " " + ws.ID + " after 0 sends", st.LivePath(ws) + " " + ws.ID + " after 1 sends"}
	if !slices.Equal(got, want) {
		t.Fatalf("checkpoints %q, want %q", got, want)
	}
}

// A chat's Activity follows its snapshot; triage answers a needs-you
// state, SeePane marks it seen, and archiving drops it.
func TestChatActivity(t *testing.T) {
	m := &fakeModel{answers: map[string]decide.Answer{"blocked": yes(0.9), "nothing": yes(0.1)}}
	d, prov := chatDaemon(t, func(o *Options) {
		s := config.DecideSettings{Provider: "jev", Triage: true, Timeout: time.Second}
		o.Decisions = func() Decisions { return Decisions{Settings: s, Provider: m} }
	})
	r := chatDo(t, d, proto.ChatNew{Dir: t.TempDir(), Prompt: "fix it"})
	id, drv, ws := r.Chat, prov.driver(t), chatTab(t, d, r.Chat)
	waitUntil(t, "working", func() bool {
		a := d.activityOf(id)
		return a.State == model.StateWorking && a.WorkspaceID == ws.ID && a.Provider == model.ProviderClaude
	})

	drv.event(session.Event{Type: session.EventRequestOpened, RequestID: "r1", RequestType: session.RequestCommand, Detail: "make deploy"})
	waitUntil(t, "triaged approval", func() bool {
		a := d.activityOf(id)
		return a.State == model.StatePendingApproval && a.Detail == "make deploy" && a.Urgency == "now"
	})
	unseen := func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, a := range d.snapshot().Activities {
			if a.PaneID == id {
				return a.Unseen
			}
		}
		return false
	}
	if !unseen() {
		t.Error("a new approval is not unseen")
	}
	must(t, d.seePane(id))
	if unseen() {
		t.Error("SeePane left the chat unseen")
	}

	if r := chatDo(t, d, proto.ChatApprove{Chat: id, Decision: "allow"}); r.Code != "" {
		t.Fatalf("approve: %+v", r)
	}
	drv.event(session.Event{Type: session.EventContent, ItemID: "a1", Kind: session.KindAssistantText, Text: "All   done,\nthe build passes."})
	drv.end(session.TurnCompleted)
	waitUntil(t, "completed", func() bool {
		a := d.activityOf(id)
		return a.State == model.StateCompleted && a.Detail == "All done, the build passes."
	})

	if r := chatDo(t, d, proto.ChatArchive{Chat: id}); r.Code != "" {
		t.Fatalf("archive: %+v", r)
	}
	if a := d.activityOf(id); a.State != "" {
		t.Errorf("archived chat keeps %+v", a)
	}
}

// A turn a plan limit refused schedules the limit resume, which sends
// continue through the driver; Claude's limit goes where flow reads it.
func TestChatLimitResume(t *testing.T) {
	clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
	var mu sync.Mutex
	var saved []flow.Limit
	d, prov := chatDaemon(t, func(o *Options) {
		o.ResumeAfterLimit = func() bool { return true }
		o.SaveLimit = func(l flow.Limit) { mu.Lock(); saved = append(saved, l); mu.Unlock() }
	})
	r := chatDo(t, d, proto.ChatNew{Dir: t.TempDir(), Prompt: "fix it"})
	id, drv := r.Chat, prov.driver(t)
	resets := clock.now.Add(time.Hour)
	drv.event(session.Event{Type: session.EventLimit, Limit: &session.Limit{Rejected: true, Resets: resets,
		Windows: []session.LimitWindow{{Minutes: 300, Used: 100, Resets: resets}}}})
	drv.end(session.TurnFailed)
	timers := func() int { d.mu.Lock(); defer d.mu.Unlock(); return len(clock.waits) }
	waitUntil(t, "resume set", func() bool { return timers() == 1 })
	want := resets.Add(resumeDelay)
	if clock.waits[0] != want.Sub(clock.now) || !resumes(d, id).Equal(want) {
		t.Fatalf("timer %v, Resumes %v; want %v", clock.waits[0], resumes(d, id), want)
	}
	if a := d.activityOf(id); a.State != model.StateError {
		t.Fatalf("stopped turn %+v", a)
	}
	mu.Lock()
	if len(saved) != 1 || saved[0].Provider != model.ProviderClaude || saved[0].Windows[0] != (flow.Window{Minutes: 300, Used: 100, Resets: resets}) {
		t.Errorf("saved limits %+v", saved)
	}
	mu.Unlock()

	clock.fire()
	if got := drv.got(); got[len(got)-1] != "send continue" {
		t.Fatalf("driver calls %q", got)
	}
	if a := d.activityOf(id); a.State != model.StateWorking || a.Detail != model.ResumedDetail {
		t.Fatalf("after the resume %+v, want working with ResumedDetail", a)
	}
	if !resumes(d, id).IsZero() {
		t.Error("Resumes stays after it fired")
	}
}

func TestChatState(t *testing.T) {
	str := func(s string) *string { return &s }
	turn := []session.TurnSummary{{ID: "t", State: session.TurnRunning}}
	for _, c := range []struct {
		name   string
		s      session.Snapshot
		state  model.AgentState
		detail string
	}{
		{"connecting", session.Snapshot{Turns: turn, SessionState: session.StateConnecting}, model.StateConnecting, ""},
		{"plan", session.Snapshot{Turns: turn, SessionState: session.StateWaiting, PlanApproval: &session.PlanApproval{Detail: str("Step 1\nStep 2")}}, model.StatePlanReady, "Step 1"},
		{"input", session.Snapshot{Turns: turn, SessionState: session.StateWaiting,
			PendingUserInputs: []session.InputRequest{{Questions: []session.Question{{Prompt: "Which?"}}}}}, model.StateAwaitingInput, "Which?"},
		{"interrupted", session.Snapshot{Turns: []session.TurnSummary{{State: session.TurnInterrupted}}, SessionState: session.StateError}, model.StateCompleted, "Interrupted"},
		{"failed", session.Snapshot{Turns: []session.TurnSummary{{State: session.TurnFailed}}, SessionState: session.StateError, LastError: str("boom")}, model.StateError, "boom"},
		{"stopped", session.Snapshot{Turns: turn, SessionState: session.StateStopped}, model.StateCompleted, "Interrupted"},
	} {
		state, detail, ok := chatState(c.s, false)
		if !ok || state != c.state || detail != c.detail {
			t.Errorf("%s: %q %q %v", c.name, state, detail, ok)
		}
	}
	if _, _, ok := chatState(session.Snapshot{SessionState: session.StateRunning}, false); ok {
		t.Error("an activity before the first turn")
	}
}
