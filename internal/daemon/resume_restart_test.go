package daemon

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
)

// savedLimit arms a real resume and saves it through store in a temp directory.
func savedLimit(t *testing.T, prov model.Provider) (*Daemon, *fakes, *fakeClock, string) {
	t.Helper()
	clock := useFakeClock(t, time.Date(2026, 10, 22, 14, 0, 0, 0, time.UTC))
	d, f := talkDaemon(t)
	d.o.ResumeAfterLimit = func() bool { return true }
	d.o.SessionID = agent.SessionID
	path := filepath.Join(t.TempDir(), "state.json")
	d.o.Save = func(s model.State) error { return store.Save(path, s) }
	d.mu.Lock()
	d.st.Panes[0].Provider, d.st.Panes[0].SessionID = prov, "agent-session"
	d.st.Activities = []model.Activity{{PaneID: "a", WorkspaceID: "w", Provider: prov, SessionID: "agent-session",
		State: model.StateError, Detail: "You've hit your session limit · resets 3pm (UTC)", UpdatedAt: clock.now}}
	d.mu.Unlock()
	d.limitStopped("a", clock.now, "You've hit your session limit · resets 3pm (UTC)", clock.now.Add(time.Hour))
	d.Save()
	saved, err := store.Load(path)
	if err != nil || saved.Panes[0].LimitResume == nil || !saved.Panes[0].LimitResume.Pending {
		t.Fatalf("armed resume on disk: %+v, %v", saved.Panes, err)
	}
	t.Cleanup(d.shutdown)
	return d, f, clock, path
}

func loadLimit(t *testing.T, f *fakes, path string, edit func(*Options)) *Daemon {
	t.Helper()
	o := f.options()
	o.Load = func() (model.State, error) { return store.Load(path) }
	o.Save = func(s model.State) error { return store.Save(path, s) }
	o.RestoreCmd, o.Derive, o.SessionID = store.RestoreCmd, agent.Derive, agent.SessionID
	o.ResumeAfterLimit = func() bool { return true }
	if edit != nil {
		edit(&o)
	}
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.shutdown)
	return d
}

func readyLimit(d *Daemon, id string) {
	d.mu.Lock()
	d.sawHook(id)
	d.live.hookAt[id] = time.Now().Add(-sendSettle.get())
	d.mu.Unlock()
}

func TestLimitResumeRestartFiresOnce(t *testing.T) {
	for _, prov := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		for _, overdue := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/overdue=%t", prov, overdue), func(t *testing.T) {
				first, f, clock, path := savedLimit(t, prov)
				at := clock.now.Add(time.Hour + resumeDelay)
				first.shutdown()
				clock.now = clock.now.Add(30 * time.Minute)
				if overdue {
					clock.now = at.Add(time.Hour)
				}
				d := loadLimit(t, f, path, nil)
				p := f.pane(2)
				if len(clock.waits) != 2 || clock.waits[1] != max(0, at.Sub(clock.now)) {
					t.Fatalf("restored waits %v", clock.waits)
				}
				clock.fire()
				if got := p.got(); got != "" || resumes(d, "a").IsZero() {
					t.Fatalf("before ready: input %q, resume %v", got, resumes(d, "a"))
				}
				readyLimit(d, "a")
				clock.fire()
				clock.fire() // a callback already queued cannot send twice
				if got := gotAfter(t, p, 0, 9); got != "continue\r" {
					t.Fatalf("typed %q", got)
				}
				st, err := store.Load(path)
				if err != nil || st.Panes[0].LimitResume == nil || st.Panes[0].LimitResume.Pending {
					t.Fatalf("consumption on disk: %+v, %v", st.Panes, err)
				}
				// The file saved before typing is the crash boundary. Its stopped
				// activity must not cause the consumed limit to arm again.
				before := len(clock.fns)
				again := loadLimit(t, f, path, nil)
				setScreen(f.pane(4), []string{"You've hit your session limit · resets 3pm (UTC)"})
				again.checkLimits()
				if len(clock.fns) != before || !resumes(again, "a").IsZero() || f.pane(4).got() != "" {
					t.Fatalf("the sent resume armed again: timers %d -> %d, resume %v, record %+v, activities %+v, input %q", before, len(clock.fns), resumes(again, "a"), st.Panes[0].LimitResume, st.Activities, f.pane(4).got())
				}
			})
		}
	}
}

func TestLimitResumeConsumedBeforeTypingSkipsRestartNudge(t *testing.T) {
	first, f, clock, path := savedLimit(t, model.ProviderClaude)
	clock.fire()
	if got := gotAfter(t, f.pane(0), 0, 9); got != "continue\r" {
		t.Fatalf("typed %q", got)
	}
	saved, err := store.Load(path) // consumption is durable before the working activity
	if err != nil {
		t.Fatal(err)
	}
	first.shutdown()
	if err := store.Save(path, saved); err != nil {
		t.Fatal(err)
	}
	before := len(clock.fns)
	d := loadLimit(t, f, path, nil)
	claudeHook(t, d, "a", `{"session_id":"agent-session","hook_event_name":"SessionStart","source":"resume"}`)
	setScreen(f.pane(2), []string{"You've hit your session limit · resets 3pm (UTC)"})
	d.checkLimits()
	d.mu.Lock()
	queued := len(d.mail.queue["a"])
	d.mu.Unlock()
	if len(clock.fns) != before || queued != 0 || !resumes(d, "a").IsZero() {
		t.Fatalf("sent resume produced %d timers, %d queued continuations", len(clock.fns)-before, queued)
	}
}

func TestLimitResumeRestartDropsStale(t *testing.T) {
	for _, name := range []string{"cancelled", "typed", "turn stopped", "turn", "pane", "session", "provider", "message", "off", "Claude continues"} {
		t.Run(name, func(t *testing.T) {
			first, f, clock, path := savedLimit(t, model.ProviderClaude)
			switch name {
			case "cancelled":
				if err := first.cancelResume(proto.CancelResume{Pane: "a"}); err != nil {
					t.Fatal(err)
				}
			case "typed":
				first.noteUserKey("a", []byte("x"))
			case "turn stopped":
				first.o.SessionID = agent.SessionID
				claudeHook(t, first, "a", `{"session_id":"agent-session","hook_event_name":"Stop","last_assistant_message":"stopped"}`)
			case "off":
				first.o.ResumeAfterLimit = func() bool { return false }
				first.checkLimits()
			}
			if name == "cancelled" || name == "typed" || name == "turn stopped" || name == "off" {
				saved, err := store.Load(path)
				if err != nil || saved.Panes[0].LimitResume.Pending {
					t.Fatalf("cancellation on disk before shutdown: %+v, %v", saved.Panes, err)
				}
			}
			first.shutdown()
			st, err := store.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "turn":
				st.Activities[0].UpdatedAt = st.Activities[0].UpdatedAt.Add(time.Second)
			case "pane":
				st.Panes[0].LimitResume.PaneID = "another-pane"
			case "session":
				st.Panes[0].SessionID = "another-session"
			case "provider":
				st.Panes[0].Provider = model.ProviderCodex
			case "message":
				st.Activities[0].Detail = "another error"
			}
			if err := store.Save(path, st); err != nil {
				t.Fatal(err)
			}
			before := len(clock.fns)
			d := loadLimit(t, f, path, func(o *Options) {
				if name == "Claude continues" {
					o.ClaudeContinues = func(model.Pane) bool { return true }
				}
			})
			if len(clock.fns) != before || !resumes(d, "a").IsZero() {
				t.Fatal("a stale resume armed after restart")
			}
			if name != "message" && name != "turn" && name != "turn stopped" {
				// Startup can first draw a blank screen, then the old limit.
				d.checkLimits()
				setScreen(f.pane(2), []string{"You've hit your session limit · resets 3pm (UTC)"})
				d.checkLimits()
				if len(clock.fns) != before {
					t.Fatal("the cancelled limit armed from the restored screen")
				}
			}
		})
	}
}

func TestLimitResumeWaitsForQueuedRestore(t *testing.T) {
	first, f, clock, path := savedLimit(t, model.ProviderCodex)
	first.shutdown()
	st, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Panes[1].Provider, st.Panes[1].SessionID = model.ProviderCodex, "other-session"
	st.Panes[0], st.Panes[1] = st.Panes[1], st.Panes[0]
	if err := store.Save(path, st); err != nil {
		t.Fatal(err)
	}
	before := len(clock.fns)
	d := loadLimit(t, f, path, func(o *Options) { o.StartupPerAgentDelay = func() time.Duration { return time.Hour } })
	if len(clock.fns) != before || d.panes["a"] != nil {
		t.Fatal("resume armed before its pane launched")
	}
	d.Save() // another restart while this pane is still queued must keep it
	saved, err := store.Load(path)
	if err != nil || !saved.Panes[1].LimitResume.Pending {
		t.Fatalf("queued resume: %+v, %v", saved.Panes, err)
	}
	d.mu.Lock()
	d.restoreNext()
	d.mu.Unlock()
	if len(clock.fns) != before+1 {
		t.Fatal("resume did not arm after its pane launched")
	}
	readyLimit(d, "a")
	clock.fire()
	if got := gotAfter(t, f.pane(3), 0, 9); got != "continue\r" {
		t.Fatalf("typed %q", got)
	}
}

func TestLimitResumeSaveFailureDoesNotSend(t *testing.T) {
	d, f, clock, _ := savedLimit(t, model.ProviderClaude)
	d.o.Save = func(model.State) error { return errors.New("disk is full") }
	clock.fire()
	if got := f.pane(0).got(); got != "" {
		t.Fatalf("typed %q without saving consumption", got)
	}
}

func TestLimitResumeCancelDuringSave(t *testing.T) {
	d, f, clock, _ := savedLimit(t, model.ProviderClaude)
	saving, release, fired, cancelled := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan error, 1)
	save := d.o.Save
	first := true
	d.o.Save = func(s model.State) error {
		if first {
			first = false
			close(saving)
			<-release
		}
		return save(s)
	}
	go func() { clock.fire(); close(fired) }()
	<-saving
	go func() { cancelled <- d.cancelResume(proto.CancelResume{Pane: "a"}) }()
	waitUntil(t, "cancel removed the resume", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.lim.pending["a"] == nil
	})
	close(release)
	<-fired
	if err := <-cancelled; err != nil {
		t.Fatal(err)
	}
	if got := f.pane(0).got(); got != "" {
		t.Fatalf("typed after cancellation during save: %q", got)
	}
}

func TestLimitResumeRestartKeepsDraft(t *testing.T) {
	first, f, clock, path := savedLimit(t, model.ProviderClaude)
	first.mu.Lock()
	first.mail.drafting["a"] = true
	// The draft predates the stopped turn, so no key cancels the timer.
	r := *first.st.Panes[0].LimitResume
	r.Drafting = true
	first.st.Panes[0].LimitResume = &r
	first.mu.Unlock()
	first.shutdown()
	d := loadLimit(t, f, path, nil)
	readyLimit(d, "a")
	setScreen(f.pane(2), claudeBox)
	clock.fire()
	if got := f.pane(2).got(); got != "" || !resumes(d, "a").IsZero() {
		t.Fatalf("draft got %q, resume %v", got, resumes(d, "a"))
	}
}

func TestLimitResumeCodexWaitsForInputBox(t *testing.T) {
	first, f, clock, path := savedLimit(t, model.ProviderCodex)
	first.shutdown()
	d := loadLimit(t, f, path, nil)
	d.mu.Lock()
	d.resumed["a"] = time.Now().Add(-restartWait)
	d.mu.Unlock()
	clock.fire()
	if f.pane(2).got() != "" || resumes(d, "a").IsZero() {
		t.Fatal("blank screen consumed the Codex resume")
	}
	setScreen(f.pane(2), []string{"›"})
	clock.fire()
	if got := gotAfter(t, f.pane(2), 0, 9); got != "continue\r" {
		t.Fatalf("typed %q", got)
	}
}

func TestLimitResumeAdoptedPane(t *testing.T) {
	first, f, clock, path := savedLimit(t, model.ProviderCodex)
	first.shutdown()
	st, err := store.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	o := f.options()
	o.ResumeAfterLimit = func() bool { return true }
	d := fromState(o, st)
	t.Cleanup(d.shutdown)
	d.mu.Lock()
	if err := d.startVT("a", nil, "", model.PortBlock{}, nil, nil); err != nil {
		d.mu.Unlock()
		t.Fatal(err)
	}
	d.restoreLimitResume("a", false)
	d.mu.Unlock()
	clock.fire()
	if got := gotAfter(t, f.pane(2), 0, 9); got != "continue\r" {
		t.Fatalf("adopted pane got %q", got)
	}
}

// Serve stops and starts on the same state file. PTYs are fake; no provider
// process or network API participates.
func TestLimitResumeDaemonStopStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	f := &fakes{statsCalls: map[string]int{}}
	turn := time.Now()
	st := model.State{
		Sessions:   []model.Session{{ID: "s", Name: "work"}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", Name: "work", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w", Provider: model.ProviderClaude, SessionID: "agent-session"}},
		Activities: []model.Activity{{PaneID: "a", WorkspaceID: "w", Provider: model.ProviderClaude, State: model.StateError, Detail: "You've hit your session limit", UpdatedAt: turn}},
	}
	if err := store.Save(path, st); err != nil {
		t.Fatal(err)
	}
	edit := func(o *Options) {
		o.Load = func() (model.State, error) { return store.Load(path) }
		o.Save = func(s model.State) error { return store.Save(path, s) }
		o.RestoreCmd, o.Derive, o.SessionID = store.RestoreCmd, agent.Derive, agent.SessionID
		o.ResumeAfterLimit = func() bool { return true }
	}
	d, _, stop := runDaemon(t, f, edit)
	d.limitStopped("a", turn, st.Activities[0].Detail, time.Now().Add(time.Hour))
	stop()
	saved, err := store.Load(path)
	if err != nil || saved.Panes[0].LimitResume == nil || !saved.Panes[0].LimitResume.Pending {
		t.Fatalf("shutdown schedule: %+v, %v", saved.Panes, err)
	}
	saved.Panes[0].LimitResume.Resets = time.Now().Add(-resumeDelay - time.Second)
	if err := store.Save(path, saved); err != nil {
		t.Fatal(err)
	}
	d, _, stop = runDaemon(t, f, edit)
	defer stop()
	if got := f.pane(1).got(); got != "" {
		t.Fatalf("typed before the resumed agent reported: %q", got)
	}
	claudeHook(t, d, "a", `{"session_id":"agent-session","hook_event_name":"SessionStart","source":"resume"}`)
	waitUntil(t, "one restored continuation", func() bool { return strings.Count(f.pane(1).got(), "continue\r") == 1 })
	saved, err = store.Load(path)
	if err != nil || saved.Panes[0].LimitResume.Pending {
		t.Fatalf("sent schedule on disk: %+v, %v", saved.Panes, err)
	}
}
