package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/timelog"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// wideLivePane renders into a 100x30 terminal grid so full-width agy, Claude
// and Codex screen fixtures fit without wrapping away their footers.
type wideLivePane struct {
	*livePane
}

func (p *wideLivePane) Snapshot() vt.Grid {
	p.gmu.Lock()
	defer p.gmu.Unlock()
	s := p.screen
	e := vt.New(100, 30, nil)
	e.Write([]byte(strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\r\n")))
	return e.Snapshot()
}

func loadAgentScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func shortenWaitTimings(t *testing.T, agy, fallback, menu time.Duration) {
	t.Helper()
	oldAgy := agyPromptDelay.set(agy)
	oldFallback := formFallbackDelay.set(fallback)
	oldMenu := userMenuWindow.set(menu)
	t.Cleanup(func() {
		agyPromptDelay.set(oldAgy)
		formFallbackDelay.set(oldFallback)
		userMenuWindow.set(oldMenu)
	})
}

// TestAgyOSC7501Fixtures replays the recorded live agy OSC 7501 reports
// (permission, working, question, plan approval, folder trust, idle) and
// confirms each produces the expected state and detail, including when a
// report arrives before Provider is set on the pane.
func TestAgyOSC7501Fixtures(t *testing.T) {
	fakeGroups(t)
	b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", "antigravity", "osc7501_reports.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name       string `json:"name"`
		OSC7501    string `json:"osc7501"`
		WantState  string `json:"want_state"`
		WantDetail string `json:"want_detail"`
	}
	if err := json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	d, _, id := openLive(t, 900)
	// Leave Provider empty on the first report to verify startup reports work
	// before the first detect poll identifies agy.
	for _, tc := range fixtures {
		status(t, d, id, tc.OSC7501, 0)
		a := d.shown(id)
		if string(a.State) != tc.WantState || a.Detail != tc.WantDetail {
			t.Fatalf("%s: got state=%q detail=%q, want %q %q", tc.Name, a.State, a.Detail, tc.WantState, tc.WantDetail)
		}
	}
}

// TestAgyHookedScreenPromptsAndNotification verifies that a hooked Antigravity
// pane whose OSC 7501 reports never arrive (or were overwritten by a hook)
// still transitions within agyPromptDelay on approval, question, folder-trust
// and plan screens, produces the right detail, offers/buttons, desktop
// notification and desktop chat card, never false-positives while working or
// printing numbered lists, and returns to working when the prompt closes.
func TestAgyHookedScreenPromptsAndNotification(t *testing.T) {
	shortenWaitTimings(t, 20*time.Millisecond, 500*time.Millisecond, 200*time.Millisecond)
	d, lp, id := openLive(t, 900)
	useReportHooks(d)
	wp := &wideLivePane{lp}
	d.mu.Lock()
	d.panes[id] = wp
	d.mu.Unlock()

	agyHook := func(name string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", "antigravity", name))
		if err != nil {
			t.Fatal(err)
		}
		if payload := agent.FromAntigravity(b); payload != nil {
			must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderAntigravity, Payload: payload}))
		}
	}

	// Start a hooked working turn; no OSC 7501 is sent.
	wp.show(loadAgentScreen(t, "antigravity-working.txt"), false)
	agyHook("session_start.json")
	agyHook("pre_invocation.json")
	if s := d.stateOf(id); s != model.StateWorking {
		t.Fatalf("after PreInvocation: state=%q, want working", s)
	}

	// Working screens (including a numbered list) must not false-positive.
	for _, screen := range []string{"antigravity-working.txt", "antigravity-working-list.txt", "antigravity-working-generating.txt"} {
		wp.show(loadAgentScreen(t, screen), false)
		time.Sleep(60 * time.Millisecond)
		if s := d.stateOf(id); s != model.StateWorking {
			t.Fatalf("%s flipped working to %q", screen, s)
		}
	}

	// Seed notification tracker on the working state (window unfocused).
	d.mu.Lock()
	acts := d.attended()
	d.mu.Unlock()
	notif, _ := model.DecideNotifications(model.Notifications{}, acts, false, "", time.Now())

	// 1. Command permission prompt appears on screen (no OSC 7501).
	wp.show(loadAgentScreen(t, "antigravity-approval-wrapped.txt"), false)
	waitUntil(t, "agy approval detected", func() bool {
		a := d.activityOf(id)
		return a.State == model.StatePendingApproval && a.Detail == "Approve Bash: echo hello-repro" && slices.Contains(a.Buttons, agent.AllowAlways)
	})
	d.mu.Lock()
	acts = d.attended()
	d.mu.Unlock()
	var deliveries []model.Activity
	notif, deliveries = model.DecideNotifications(notif, acts, false, "", time.Now())
	if len(deliveries) != 1 || deliveries[0].State != model.StatePendingApproval || deliveries[0].Detail != "Approve Bash: echo hello-repro" {
		t.Fatalf("approval notification = %+v", deliveries)
	}
	// Desktop chat shows the command approval card with Allow for session offered.
	pc, live, ok := d.paneChatOf(id)
	if !ok {
		t.Fatal("paneChatOf returned false for agy pane")
	}
	pc.mu.Lock()
	_ = pc.catchUp(live)
	approvals := slices.Clone(pc.snap.PendingApprovals)
	pc.mu.Unlock()
	if len(approvals) != 1 || approvals[0].Request == nil || approvals[0].Request.Command != "echo hello-repro" || approvals[0].NoSession {
		t.Fatalf("desktop chat approval card = %+v", approvals)
	}

	// A stray PostToolUse hook while the prompt is still on screen must not
	// leave the pane stuck on working: screen detection re-promotes it.
	agyHook("post_tool_use.json")
	waitUntil(t, "re-promoted after stray hook", func() bool {
		return d.stateOf(id) == model.StatePendingApproval
	})

	// When the prompt closes and work resumes, state returns to working and
	// the desktop chat card withdraws.
	wp.show(loadAgentScreen(t, "antigravity-working.txt"), false)
	waitUntil(t, "resumed working", func() bool { return d.stateOf(id) == model.StateWorking })
	pc, live, _ = d.paneChatOf(id)
	pc.mu.Lock()
	_ = pc.catchUp(live)
	pendingAfter := len(pc.snap.PendingApprovals)
	pc.mu.Unlock()
	if pendingAfter != 0 {
		t.Fatalf("approval card lingered after prompt closed: %d", pendingAfter)
	}

	// 2. Question prompt (ask_question) appears on screen (no OSC 7501).
	wp.show(loadAgentScreen(t, "antigravity-question-no-rec.txt"), false)
	waitUntil(t, "agy question detected", func() bool {
		a := d.activityOf(id)
		return a.State == model.StateAwaitingInput && a.Detail == "Which framework do you prefer?" && slices.Equal(a.Buttons, []string{"Go", "Rust"})
	})
	d.mu.Lock()
	acts = d.attended()
	d.mu.Unlock()
	notif, deliveries = model.DecideNotifications(notif, acts, false, "", time.Now().Add(model.NotificationInterval+time.Second))
	if len(deliveries) != 1 || deliveries[0].State != model.StateAwaitingInput || deliveries[0].Detail != "Which framework do you prefer?" {
		t.Fatalf("question notification = %+v", deliveries)
	}
	pc, live, _ = d.paneChatOf(id)
	pc.mu.Lock()
	_ = pc.catchUp(live)
	inputs := slices.Clone(pc.snap.PendingUserInputs)
	pc.mu.Unlock()
	if len(inputs) != 1 || len(inputs[0].Questions) != 1 || inputs[0].Questions[0].Prompt != "Which framework do you prefer?" || len(inputs[0].Questions[0].Options) != 2 {
		t.Fatalf("desktop chat question card = %+v", inputs)
	}

	// 3. Folder-trust prompt appears on screen.
	wp.show(loadAgentScreen(t, "antigravity-working.txt"), false)
	waitUntil(t, "working before trust", func() bool { return d.stateOf(id) == model.StateWorking })
	wp.show(loadAgentScreen(t, "antigravity-trust.txt"), false)
	waitUntil(t, "agy trust detected", func() bool {
		a := d.activityOf(id)
		return a.State == model.StateAwaitingInput && a.Detail == "Trust this folder?" && slices.Equal(a.Buttons, []string{agent.Trust, agent.DontTrust})
	})

	// 4. Plan approval review dialog appears on screen.
	wp.show(loadAgentScreen(t, "antigravity-working.txt"), false)
	waitUntil(t, "working before plan", func() bool { return d.stateOf(id) == model.StateWorking })
	wp.show(loadAgentScreen(t, "antigravity-plan-artifact.txt"), false)
	waitUntil(t, "agy plan detected", func() bool {
		a := d.activityOf(id)
		return a.State == model.StatePlanReady && a.Detail == "Approve plan" && slices.Equal(a.Buttons, []string{agent.ApprovePlan})
	})
	pc, live, _ = d.paneChatOf(id)
	pc.mu.Lock()
	_ = pc.catchUp(live)
	plan := pc.snap.PlanApproval
	pc.mu.Unlock()
	if plan == nil {
		t.Fatal("desktop chat missing PlanApproval card for agy plan")
	}
}

// TestScreenFormSafetyNetAndUserMenu verifies the 10-second safety net for
// every agent: a missed permission hook on Claude or Codex promotes after
// formFallbackDelay, logs why, and notifies; a menu the user opened
// themselves while typing in the TUI (/model, /settings) never triggers it;
// and a prompt submitted in the TUI before the turn started does not suppress
// a subsequent permission prompt.
func TestScreenFormSafetyNetAndUserMenu(t *testing.T) {
	shortenWaitTimings(t, 20*time.Millisecond, 90*time.Millisecond, 250*time.Millisecond)
	var logBuf bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	// Case 1: Claude missed hook -> safety net waits for formFallbackDelay,
	// then promotes, logs why, and notifies; clears when spinner returns.
	t.Run("claude missed hook", func(t *testing.T) {
		d, lp, id := openLive(t, 100)
		wp := &wideLivePane{lp}
		d.mu.Lock()
		d.panes[id] = wp
		d.mu.Unlock()

		// User submits a prompt in the TUI, then hook starts the turn.
		d.noteUserKey(id, []byte("run tests\r"))
		time.Sleep(15 * time.Millisecond)
		wp.show(loadAgentScreen(t, "claude-spinner.txt"), false)
		must(t, d.handle(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte("working")}))

		d.mu.Lock()
		acts := d.attended()
		d.mu.Unlock()
		notif, _ := model.DecideNotifications(model.Notifications{}, acts, false, "", time.Now())

		wp.show(loadAgentScreen(t, "claude-permission.txt"), false)
		// Before formFallbackDelay elapses, state must stay working.
		time.Sleep(35 * time.Millisecond)
		if s := d.stateOf(id); s != model.StateWorking {
			t.Fatalf("safety net fired too early: state=%q", s)
		}
		waitUntil(t, "claude safety net promotes", func() bool {
			return d.stateOf(id) == model.StatePendingApproval
		})
		if !strings.Contains(logBuf.String(), "screen showed a form") {
			t.Errorf("safety net did not log why: %q", logBuf.String())
		}
		d.mu.Lock()
		acts = d.attended()
		d.mu.Unlock()
		_, deliveries := model.DecideNotifications(notif, acts, false, "", time.Now())
		if len(deliveries) != 1 || deliveries[0].State != model.StatePendingApproval {
			t.Fatalf("safety net notification = %+v", deliveries)
		}

		// Prompt dismissed / work resumes -> state returns to working.
		wp.show(loadAgentScreen(t, "claude-spinner.txt"), false)
		waitUntil(t, "claude resumed working", func() bool { return d.stateOf(id) == model.StateWorking })
	})

	// Case 2: Codex missed hook -> covered by the same safety net.
	t.Run("codex missed hook", func(t *testing.T) {
		d, lp, id := openLive(t, 300)
		wp := &wideLivePane{lp}
		d.mu.Lock()
		d.panes[id] = wp
		d.mu.Unlock()

		wp.show(loadAgentScreen(t, "codex-working.txt"), false)
		must(t, d.handle(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderCodex, Payload: []byte("working")}))

		wp.show(loadAgentScreen(t, "tuios-codex-approval.txt"), false)
		waitUntil(t, "codex safety net promotes", func() bool {
			return d.stateOf(id) == model.StatePendingApproval
		})
	})

	// Case 3: User opens /model in Claude TUI while working -> never fires.
	// Once they close it and later a real form appears without typing, it fires.
	t.Run("user opened menu suppressed", func(t *testing.T) {
		d, lp, id := openLive(t, 100)
		wp := &wideLivePane{lp}
		d.mu.Lock()
		d.panes[id] = wp
		d.mu.Unlock()

		wp.show(loadAgentScreen(t, "claude-spinner.txt"), false)
		must(t, d.handle(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderClaude, Payload: []byte("working")}))
		time.Sleep(15 * time.Millisecond)

		// User types /model in the TUI while Claude works.
		d.noteUserKey(id, []byte("/model\r"))
		wp.show(loadAgentScreen(t, "claude-model-menu.txt"), false)
		time.Sleep(200 * time.Millisecond) // > 2x formFallbackDelay
		if s := d.stateOf(id); s != model.StateWorking {
			t.Fatalf("user-opened /model menu changed state to %q", s)
		}

		// User closes /model; after userMenuWindow passes, a real approval fires.
		wp.show(loadAgentScreen(t, "claude-spinner.txt"), false)
		time.Sleep(280 * time.Millisecond)
		wp.show(loadAgentScreen(t, "claude-permission.txt"), false)
		waitUntil(t, "real prompt after menu closed promotes", func() bool {
			return d.stateOf(id) == model.StatePendingApproval
		})
	})

	// Case 4: User opens /settings in agy TUI while working -> never fires.
	t.Run("agy user opened settings menu suppressed", func(t *testing.T) {
		d, lp, id := openLive(t, 900)
		wp := &wideLivePane{lp}
		d.mu.Lock()
		d.panes[id] = wp
		d.mu.Unlock()

		wp.show(loadAgentScreen(t, "antigravity-working.txt"), false)
		must(t, d.handle(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderAntigravity, Payload: []byte("working")}))
		time.Sleep(15 * time.Millisecond)

		d.noteUserKey(id, []byte("/settings\r"))
		wp.show(loadAgentScreen(t, "antigravity-settings-menu.txt"), false)
		time.Sleep(200 * time.Millisecond)
		if s := d.stateOf(id); s != model.StateWorking {
			t.Fatalf("user-opened agy /settings menu changed state to %q", s)
		}
	})
}

// TestAgyScreenWaitRecordedInTiming verifies that a wait detected from agy's
// screen (without OSC 7501) is recorded in timing.jsonl's turn Wait field the
// same way Claude Code's approval hooks are.
func TestAgyScreenWaitRecordedInTiming(t *testing.T) {
	shortenWaitTimings(t, 15*time.Millisecond, 500*time.Millisecond, 200*time.Millisecond)
	oldPoll := livePoll.set(10 * time.Millisecond)
	t.Cleanup(func() { livePoll.set(oldPoll) })

	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Workspaces: []model.Workspace{{ID: "w", Name: "agy-wait", Path: "/home/u/repo", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "a"}}}, ActiveTab: "t"}},
		Panes:      []model.Pane{{ID: "a", WorkspaceID: "w", Cwd: "/home/u/repo"}},
	}
	o := f.options()
	o.Derive, o.SessionID = agent.Derive, agent.SessionID
	timingPath := filepath.Join(t.TempDir(), "timing.jsonl")
	tl, err := timelog.Open(timingPath)
	must(t, err)
	o.Timing = tl

	d, err := NewWith(o)
	must(t, err)
	lp := &livePane{fakePane: f.pane(0)}
	lp.fgGroup.Store(900)
	wp := &wideLivePane{lp}
	d.mu.Lock()
	d.panes["a"] = wp
	d.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() { d.livenessLoop(ctx); close(done) }()

	sendAgy := func(name string) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", "antigravity", name))
		if err != nil {
			t.Fatal(err)
		}
		if payload := agent.FromAntigravity(b); payload != nil {
			must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderAntigravity, Payload: payload}))
		}
	}

	wp.show(loadAgentScreen(t, "antigravity-working.txt"), false)
	sendAgy("session_start.json")
	sendAgy("pre_invocation.json")
	time.Sleep(30 * time.Millisecond)

	// Permission prompt sits on screen (no OSC 7501).
	wp.show(loadAgentScreen(t, "antigravity-approval-wrapped.txt"), false)
	waitUntil(t, "agy approval", func() bool { return d.stateOf("a") == model.StatePendingApproval })
	time.Sleep(80 * time.Millisecond)

	// Prompt answered -> returns to working -> Stop finishes turn.
	wp.show(loadAgentScreen(t, "antigravity-working.txt"), false)
	waitUntil(t, "agy working again", func() bool { return d.stateOf("a") == model.StateWorking })
	time.Sleep(25 * time.Millisecond)
	sendAgy("stop.json")
	tl.Close(time.Second)

	evs, err := timelog.Read(timingPath, time.Time{})
	must(t, err)
	if len(evs) != 2 {
		t.Fatalf("timing events = %+v, want prompt + turn", evs)
	}
	turn := evs[1]
	if turn.Kind != timelog.Turn || turn.Provider != "antigravity" || turn.End != timelog.Completed {
		t.Fatalf("turn = %+v", turn)
	}
	if turn.Wait < 50 {
		t.Fatalf("turn.Wait = %dms, want >= 50ms (turn=%+v)", turn.Wait, turn)
	}
	if turn.Model+turn.Wait != turn.Ms {
		t.Errorf("turn parts mismatch: model=%d wait=%d ms=%d", turn.Model, turn.Wait, turn.Ms)
	}
}
