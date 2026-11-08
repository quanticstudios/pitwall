package daemon

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
)

// The fake process tree of these tests. The pane's foreground group is
// 100, its agent's. That agent starts each hook in a group of its own
// (ownerHook), as Claude Code does, and runs a tool shell, 200, in another.
// From the tool shell run a nested Claude, whose hooks also lead their own
// groups (claudeChild), and a nested Codex, whose hooks stay in its group
// (codexChild). wrappedHook is an owner hook started through a shell that
// leads the hook's group.
const (
	ownerHook   = 1001
	wrappedHook = 1003
	claudeChild = 2011
	codexChild  = 2021
)

var nestedTree = map[int][2]int{ // pid: parent, group
	100: {50, 100}, 50: {1, 50},
	ownerHook:   {100, ownerHook},
	1002:        {100, 1002},
	wrappedHook: {1002, 1002},
	200:         {100, 200},
	201:         {200, 200},
	claudeChild: {201, claudeChild},
	202:         {200, 200},
	codexChild:  {202, 200},
}

// useTree makes parentOf read tree.
func useTree(t *testing.T, tree map[int][2]int) {
	t.Helper()
	old := parentOf
	parentOf = func(pid int) (int, int) { return tree[pid][0], tree[pid][1] }
	t.Cleanup(func() { parentOf = old })
}

// sendFrom delivers a hook as serveConn does for one from process pid.
func sendFrom(t *testing.T, d *Daemon, pid int, provider model.Provider, payload string) {
	t.Helper()
	ev := proto.AgentEvent{Pane: "a", Provider: provider, Payload: []byte(payload)}
	if !d.nestedHook(ev, pid) {
		must(t, d.agentEvent(t.Context(), ev))
	}
}

func hookFrom(t *testing.T, d *Daemon, pid int, provider model.Provider, event, sid string) {
	t.Helper()
	payload := fmt.Sprintf(`{"hook_event_name":%q,"session_id":%q,"source":"startup","prompt":"go","tool_name":"Bash","permission_mode":"plan","transcript_path":%q,"last_assistant_message":"child reply"}`,
		event, sid, filepath.Join(t.TempDir(), "session.jsonl"))
	sendFrom(t, d, pid, provider, payload)
}

// nestedHook sends a hook from the pane's agent, or, for sid "child" or
// "", from a nested agent of that provider.
func nestedHook(t *testing.T, d *Daemon, provider model.Provider, event, sid string) {
	t.Helper()
	pid := ownerHook
	if sid == "child" || sid == "" {
		pid = claudeChild
		if provider == model.ProviderCodex {
			pid = codexChild
		}
	}
	hookFrom(t, d, pid, provider, event, sid)
}

func nestedDaemon(t *testing.T) (*Daemon, *livePane) {
	t.Helper()
	d, f := talkDaemon(t)
	t.Cleanup(d.shutdown)
	useReportHooks(d)
	useTree(t, nestedTree)
	d.o.RestoreCmd = store.RestoreCmd
	lp := &livePane{fakePane: f.pane(0)}
	lp.fgGroup.Store(100)
	d.mu.Lock()
	d.panes["a"] = lp
	d.mu.Unlock()
	return d, lp
}

func TestNestedHooksLeaveOwnerUntouched(t *testing.T) {
	for _, owner := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		for _, child := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
			t.Run(string(owner)+"/"+string(child), func(t *testing.T) {
				d, _ := nestedDaemon(t)
				nestedHook(t, d, owner, "UserPromptSubmit", "owner")
				d.mu.Lock()
				d.reviews = map[string]string{"a": "b"}
				d.replies = map[string]string{"a": "owner reply"}
				d.st.Panes[0].AgentMode = "bypassPermissions"
				pane, activity := d.st.Panes[0], d.st.Activities[0]
				at, seq, fg, version := d.live.hookAt["a"], d.live.hookSeq["a"], d.live.fg["a"], d.st.Version
				notifications, _ := model.DecideNotifications(model.Notifications{}, d.attended(), false, "", time.Now())
				d.mu.Unlock()
				for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PermissionRequest", "Stop", "SessionEnd"} {
					nestedHook(t, d, child, event, "child")
					d.mu.Lock()
					if !reflect.DeepEqual(d.st.Panes[0], pane) || !reflect.DeepEqual(d.st.Activities, []model.Activity{activity}) {
						t.Errorf("%s changed owner: pane %+v, activities %+v", event, d.st.Panes[0], d.st.Activities)
					}
					if d.live.hookAt["a"] != at || d.live.hookSeq["a"] != seq || d.live.fg["a"] != fg || d.st.Version != version {
						t.Errorf("%s changed hook bookkeeping or published state", event)
					}
					if d.replies["a"] != "owner reply" || d.reviews["a"] != "b" || len(d.mail.queue["b"]) != 0 {
						t.Errorf("%s kept or relayed a child reply", event)
					}
					var notices []model.Activity
					notifications, notices = model.DecideNotifications(notifications, d.attended(), false, "", time.Now())
					if len(notices) != 0 {
						t.Errorf("%s notified: %+v", event, notices)
					}
					d.mu.Unlock()
					if t.Failed() {
						t.FailNow()
					}
				}
				d.Save()
				saved, err := d.o.Load()
				must(t, err)
				if cmd := store.RestoreCmd(saved.Panes[0]); !reflect.DeepEqual(cmd, store.RestoreCmd(pane)) {
					t.Fatalf("resume command: %q", cmd)
				}
				d.shutdown()
				restarted := newDaemon(t, d.o)
				if got := restarted.panes["a"].(*fakePane).cfg.Cmd; !reflect.DeepEqual(got, store.RestoreCmd(pane)) {
					t.Fatalf("restarted command: %q", got)
				}
			})
		}
	}
}

func TestNestedHooksActiveStates(t *testing.T) {
	for _, state := range []model.AgentState{model.StateWorking, model.StateConnecting, model.StatePendingApproval, model.StateAwaitingInput, model.StatePlanReady, model.StateCompleted} {
		t.Run(string(state), func(t *testing.T) {
			d, _ := nestedDaemon(t)
			nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "owner")
			d.mu.Lock()
			d.st.Activities[0].State = state
			d.mu.Unlock()
			nestedHook(t, d, model.ProviderCodex, "Stop", "child")
			if p, a := reportedPane(d, "a"), d.activityOf("a"); p.SessionID != "owner" || a.State != state {
				t.Fatalf("child changed %s: %+v, %+v", state, p, a)
			}
		})
	}
}

// A nested agent's hook is dropped by where it comes from, so one without
// a session id is dropped too, of the owner's provider or not.
func TestNestedHooksWithoutSession(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		t.Run(string(provider), func(t *testing.T) {
			d, _ := nestedDaemon(t)
			nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "owner")
			nestedHook(t, d, provider, "Stop", "")
			if p, a := reportedPane(d, "a"), d.activityOf("a"); p.Provider != model.ProviderClaude || p.SessionID != "owner" || a.State != model.StateWorking {
				t.Fatalf("sessionless hook: %+v, %+v", p, a)
			}
		})
	}
}

func TestNestedHooksReplacementBeforePoll(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		t.Run(string(provider), func(t *testing.T) {
			d, lp := nestedDaemon(t)
			nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "owner")
			lp.fgGroup.Store(300) // the owner exits without a hook; another job starts
			nestedHook(t, d, provider, "SessionStart", "replacement")
			nestedHook(t, d, provider, "UserPromptSubmit", "replacement")
			if p, a := reportedPane(d, "a"), d.activityOf("a"); p.Provider != provider || p.SessionID != "replacement" || a.SessionID != "replacement" || a.State != model.StateWorking {
				t.Fatalf("replacement blocked: %+v, %+v", p, a)
			}
		})
	}
}

func TestNestedHooksReplacementAfterExit(t *testing.T) {
	d, lp, id := openLive(t, 100)
	useReportHooks(d)
	claudeHook(t, d, id, `{"hook_event_name":"UserPromptSubmit","session_id":"owner"}`)
	lp.fgGroup.Store(200)
	waitUntil(t, "dead owner clears", func() bool { return d.providerOf(id) == "" && d.stateOf(id) == "" })
	lp.fgGroup.Store(300)
	must(t, d.handle(t.Context(), proto.AgentEvent{Pane: id, Provider: model.ProviderCodex,
		Payload: []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"next"}`)}))
	if p, a := reportedPane(d, id), d.activityOf(id); p.Provider != model.ProviderCodex || p.SessionID != "next" || a.State != model.StateWorking {
		t.Fatalf("replacement after exit blocked: %+v, %+v", p, a)
	}
}

func TestNestedHooksOwnerChangesSession(t *testing.T) {
	for _, command := range []string{"/clear", "/resume"} {
		t.Run(command, func(t *testing.T) {
			d, _ := nestedDaemon(t)
			nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "owner")
			claudeHook(t, d, "a", fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"owner","prompt":%q}`, command))
			nestedHook(t, d, model.ProviderClaude, "SessionEnd", "owner")
			if a := d.activityOf("a"); a.State != "" {
				t.Fatalf("owner SessionEnd kept activity: %+v", a)
			}
			nestedHook(t, d, model.ProviderClaude, "SessionStart", "next")
			nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "next")
			if p, a := reportedPane(d, "a"), d.activityOf("a"); p.SessionID != "next" || a.SessionID != "next" || a.State != model.StateWorking {
				t.Fatalf("new session blocked: %+v, %+v", p, a)
			}
		})
	}
}

func TestNestedHooksAfterInterrupt(t *testing.T) {
	d, _, id := openLive(t, 100)
	useReportHooks(d)
	claudeHook(t, d, id, `{"hook_event_name":"UserPromptSubmit","session_id":"owner"}`)
	d.noteInput(id, []byte("\x1b"))
	claudeHook(t, d, id, `{"hook_event_name":"SessionStart","session_id":"next","source":"resume"}`)
	claudeHook(t, d, id, `{"hook_event_name":"UserPromptSubmit","session_id":"next"}`)
	if p, a := reportedPane(d, id), d.activityOf(id); p.SessionID != "next" || a.SessionID != "next" || a.State != model.StateWorking {
		t.Fatalf("session after Esc blocked: %+v, %+v", p, a)
	}
}

func TestNestedHooksIdleTakeover(t *testing.T) {
	for _, event := range []string{"SessionStart", "Stop", "StopFailure", "SessionEnd"} {
		t.Run(event, func(t *testing.T) {
			d, _ := nestedDaemon(t)
			nestedHook(t, d, model.ProviderClaude, event, "owner")
			nestedHook(t, d, model.ProviderCodex, "UserPromptSubmit", "next")
			if p, a := reportedPane(d, "a"), d.activityOf("a"); p.Provider != model.ProviderCodex || p.SessionID != "next" || a.State != model.StateWorking {
				t.Fatalf("idle takeover blocked: %+v, %+v", p, a)
			}
		})
	}
}

func TestNestedHooksKeepSubagents(t *testing.T) {
	d, _ := nestedDaemon(t)
	nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "owner")
	for _, event := range []string{"SubagentStart", "PreToolUse", "PostToolUse", "SubagentStop"} {
		nestedHook(t, d, model.ProviderClaude, event, "owner")
		if p, a := reportedPane(d, "a"), d.activityOf("a"); p.SessionID != "owner" || a.State != model.StateWorking {
			t.Fatalf("%s changed owner: %+v, %+v", event, p, a)
		}
	}
}

func TestNestedHooksKeepForks(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		t.Run(string(provider), func(t *testing.T) {
			d, _ := nestedDaemon(t)
			nestedHook(t, d, provider, "UserPromptSubmit", "owner")
			cmd, err := store.ForkCmd(model.Pane{Provider: provider, SessionID: "source"})
			must(t, err)
			d.mu.Lock()
			d.st.Panes[0].Cmd = cmd
			seq := d.live.hookSeq["a"]
			d.mu.Unlock()
			nestedHook(t, d, provider, "SessionStart", "source")
			if p := reportedPane(d, "a"); p.SessionID != "owner" {
				t.Fatalf("source replaced fork session: %+v", p)
			}
			d.mu.Lock()
			accepted := d.live.hookSeq["a"] == seq+1
			d.mu.Unlock()
			if !accepted {
				t.Fatal("fork source hook bypassed its existing handling")
			}
		})
	}
	d, _ := nestedDaemon(t)
	nestedHook(t, d, model.ProviderCodex, "UserPromptSubmit", "owner")
	must(t, d.agentEvent(t.Context(), proto.AgentEvent{Pane: "a", Provider: model.ProviderCodex,
		Payload: []byte(`{"hook_event_name":"Stop","session_id":"side","transcript_path":null,"permission_mode":"plan"}`)}))
	if p, a := reportedPane(d, "a"), d.activityOf("a"); p.SessionID != "owner" || a.State != model.StateWorking {
		t.Fatalf("/side changed owner: %+v, %+v", p, a)
	}
}

func TestNestedHooksKeepPiRuntimes(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderPi, model.ProviderOmp} {
		t.Run(string(provider), func(t *testing.T) {
			d, _ := nestedDaemon(t)
			send := func(event, sid, runtime string) {
				t.Helper()
				payload := fmt.Sprintf(`{"event":%q,"session_id":%q,"runtime":%q}`, event, sid, runtime)
				must(t, d.handle(t.Context(), proto.AgentEvent{Pane: "a", Provider: provider, Payload: []byte(payload)}))
			}
			send("session_start", "old", "r1")
			send("agent_start", "old", "r1")
			send("session_start", "new", "r2")
			send("agent_start", "new", "r2")
			send("session_shutdown", "old", "r1")
			for _, event := range []string{"session_start", "agent_start", "tool_call"} {
				send(event, "old", "r1")
				if p, a := reportedPane(d, "a"), d.activityOf("a"); p.SessionID != "new" || a.SessionID != "new" || a.State != model.StateWorking {
					t.Fatalf("retired runtime changed owner: %+v, %+v", p, a)
				}
			}
		})
	}
}

func TestNestedIn(t *testing.T) {
	useTree(t, nestedTree)
	for pid, want := range map[int]bool{
		ownerHook: false, wrappedHook: false, // the pane's agent
		claudeChild: true, codexChild: true, // agents its tool shell runs
		100:  false, // a hook in the foreground group
		50:   false, // the shell above it
		9999: false, // gone
	} {
		if got := nestedIn(pid, 100); got != want {
			t.Errorf("nestedIn(%d) = %v, want %v", pid, got, want)
		}
	}
	if nestedIn(claudeChild, 300) {
		t.Error("a child counted as nested under a group that is not its ancestor")
	}
}

// Finding 1: Claude exits and Codex starts in the tab. The exit check
// clears the hooks' foreground group, and detect shows Codex working from
// its screen, with no session. Codex's hooks, with or without a
// transcript, now own the pane.
func TestNestedHooksReplacementAfterDetect(t *testing.T) {
	d, lp := nestedDaemon(t)
	nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "claude")
	nestedHook(t, d, model.ProviderClaude, "Stop", "claude")
	lp.fgGroup.Store(300)
	d.checkForeground()
	d.mu.Lock()
	d.setProvider("a", model.ProviderCodex)
	d.setActivity(t.Context(), "a", model.ProviderCodex, model.StateWorking, "")
	d.mu.Unlock()
	codex := map[int][2]int{300: {50, 300}, 3001: {300, 300}}
	useTree(t, codex)
	sendFrom(t, d, 3001, model.ProviderCodex, `{"hook_event_name":"SessionStart","session_id":"codex","transcript_path":null}`)
	sendFrom(t, d, 3001, model.ProviderCodex, `{"hook_event_name":"UserPromptSubmit","session_id":"codex","prompt":"go"}`)
	sendFrom(t, d, 3001, model.ProviderCodex, `{"hook_event_name":"Stop","session_id":"codex","last_assistant_message":"done"}`)
	if p, a := reportedPane(d, "a"), d.activityOf("a"); p.Provider != model.ProviderCodex || p.SessionID != "codex" || a.State != model.StateCompleted {
		t.Fatalf("Codex after Claude: %+v, %+v", p, a)
	}
}

// Finding 2: a background child of the pane's agent reports while that
// agent is idle, then dies without a Stop. Its hooks never took the pane,
// so the owner's next turn shows and ends.
func TestNestedHooksBackgroundChildDies(t *testing.T) {
	d, _ := nestedDaemon(t)
	nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "owner")
	nestedHook(t, d, model.ProviderClaude, "Stop", "owner")
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "PreToolUse"} {
		nestedHook(t, d, model.ProviderClaude, event, "child")
	}
	if p, a := reportedPane(d, "a"), d.activityOf("a"); p.SessionID != "owner" || a.State != model.StateCompleted {
		t.Fatalf("idle owner's background child took the pane: %+v, %+v", p, a)
	}
	nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "owner")
	if a := d.activityOf("a"); a.State != model.StateWorking {
		t.Fatalf("owner's prompt dropped: %+v", a)
	}
	nestedHook(t, d, model.ProviderClaude, "Stop", "owner")
	if p, a := reportedPane(d, "a"), d.activityOf("a"); p.SessionID != "owner" || a.State != model.StateCompleted {
		t.Fatalf("owner's Stop dropped: %+v, %+v", p, a)
	}
}

// Finding 3: a script in the foreground runs claude twice, and the first is
// killed mid-turn. The second Claude is in the same foreground group.
func TestNestedHooksReplacementInSameGroup(t *testing.T) {
	d, _ := nestedDaemon(t)
	useTree(t, map[int][2]int{100: {50, 100}, 101: {100, 100}, 1011: {101, 1011}, 102: {100, 100}, 1021: {102, 1021}})
	hookFrom(t, d, 1011, model.ProviderClaude, "UserPromptSubmit", "first")
	for _, event := range []string{"SessionStart", "UserPromptSubmit"} {
		hookFrom(t, d, 1021, model.ProviderClaude, event, "second")
	}
	if p, a := reportedPane(d, "a"), d.activityOf("a"); p.SessionID != "second" || a.SessionID != "second" || a.State != model.StateWorking {
		t.Fatalf("second Claude blocked: %+v, %+v", p, a)
	}
	hookFrom(t, d, 1021, model.ProviderClaude, "Stop", "second")
	if a := d.activityOf("a"); a.State != model.StateCompleted {
		t.Fatalf("second Claude's Stop dropped: %+v", a)
	}
}

// Finding 4: a Windows pane reports no foreground group and no process
// tree, so nothing is dropped and a replacement takes the pane mid-turn.
func TestNestedHooksNoForeground(t *testing.T) {
	d, lp := nestedDaemon(t)
	lp.fgGroup.Store(0)
	useTree(t, nil)
	nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "owner")
	nestedHook(t, d, model.ProviderCodex, "SessionStart", "next")
	nestedHook(t, d, model.ProviderCodex, "UserPromptSubmit", "next")
	if p, a := reportedPane(d, "a"), d.activityOf("a"); p.Provider != model.ProviderCodex || p.SessionID != "next" || a.State != model.StateWorking {
		t.Fatalf("replacement blocked: %+v, %+v", p, a)
	}
}

// Finding 5: the owner's turn ended with no hook, and the same agent starts
// a new session without a SessionEnd, as Codex's /new may.
func TestNestedHooksNewSessionWithoutEnd(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		t.Run(string(provider), func(t *testing.T) {
			d, _ := nestedDaemon(t)
			nestedHook(t, d, provider, "UserPromptSubmit", "old")
			for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop"} {
				nestedHook(t, d, provider, event, "new")
			}
			if p, a := reportedPane(d, "a"), d.activityOf("a"); p.SessionID != "new" || a.State != model.StateCompleted {
				t.Fatalf("new session blocked: %+v, %+v", p, a)
			}
		})
	}
}
