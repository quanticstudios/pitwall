package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/store"
)

// TestAgyProgram follows the OSC 7501 reports Antigravity CLI 1.5.13 wrote
// in a terminal around a permission prompt and a question: they set the
// agy pane's activity where agy's hooks say nothing.
func TestAgyProgram(t *testing.T) {
	fakeGroups(t)
	d, _, id := openLive(t, 0) // why: with no foreground group the liveness poll skips the pane, so only the reports set its activity
	d.mu.Lock()
	d.st.Panes[slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })].Provider = model.ProviderAntigravity
	d.mu.Unlock()
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	want := func(step string, state model.AgentState, detail string) {
		t.Helper()
		if a := d.shown(id); a.State != state || a.Detail != detail || state != "" && a.Provider != model.ProviderAntigravity {
			t.Fatalf("%s: %+v, want %q %q", step, a, state, detail)
		}
	}

	status(t, d, id, "state=blocked:kind=auth:app=antigravity:msg="+b64("Sign in required"), 0)
	want("sign-in at start", "", "")
	status(t, d, id, "state=working:app=antigravity", 0)
	want("working with nothing asked: the hooks' to say", "", "")
	status(t, d, id, "state=blocked:kind=permission:app=antigravity:msg="+b64("Approve Bash: rm -rf ./junk"), 0)
	want("permission prompt", model.StatePendingApproval, "Approve Bash: rm -rf ./junk")
	status(t, d, id, "state=working:app=antigravity", 0)
	want("approved", model.StateWorking, "")
	status(t, d, id, "state=done:app=antigravity:msg="+b64("Removed it."), 0)
	want("done while working stays with the Stop hook", model.StateWorking, "")
	status(t, d, id, "state=blocked:kind=question:app=antigravity:msg="+b64("Which color do you like?"), 0)
	want("question", model.StateAwaitingInput, "Which color do you like?")
	status(t, d, id, "state=done:app=antigravity:msg="+b64("You picked **red**."), 0)
	want("answered and done", model.StateCompleted, "You picked **red**.")
	status(t, d, id, "state=blocked:kind=permission:app=antigravity:msg="+b64("Approve Bash: rmdir ./j2"), 0)
	status(t, d, id, "state=idle:app=antigravity", 0)
	want("denied with Esc, which interrupts the turn", "", "")

	// Another program's report, or one under another agent, is no agy's.
	status(t, d, id, "state=blocked:kind=permission:app=make", 0)
	want("another app", "", "")
	d.mu.Lock()
	d.st.Panes[slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })].Provider = model.ProviderClaude
	d.mu.Unlock()
	status(t, d, id, "state=blocked:kind=permission:app=antigravity", 0)
	if a := d.shown(id); a.Provider == model.ProviderAntigravity {
		t.Fatalf("an agy report under Claude Code set %+v", a)
	}
}

// TestAgyTrustQuestion: agy's folder-trust question makes the tab need
// the user, and a hook from the prompt agy runs meanwhile in its scratch
// folder does not mark it Done.
func TestAgyTrustQuestion(t *testing.T) {
	fakeGroups(t)
	d, _, id := openLive(t, 900)
	d.mu.Lock()
	d.st.Panes[slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })].Provider = model.ProviderAntigravity
	d.mu.Unlock()
	b64 := base64.StdEncoding.EncodeToString([]byte("Trust this folder?"))
	status(t, d, id, "state=blocked:kind=permission:app=antigravity:msg="+b64, 0)
	if a := d.shown(id); a.State != model.StateAwaitingInput || a.Detail != "Trust this folder?" {
		t.Fatalf("trust question: %+v", a)
	}
	for _, hook := range []string{
		`{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"fix it"}`,
		`{"hook_event_name":"Stop","session_id":"s","last_assistant_message":"Done in scratch."}`,
	} {
		must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderAntigravity, Payload: []byte(hook)}))
		if a := d.shown(id); a.State != model.StateAwaitingInput {
			t.Fatalf("after %s: %+v", hook, a)
		}
	}
	// Trusted: agy reports idle, and its hooks own the tab again.
	status(t, d, id, "state=idle:app=antigravity", 0)
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: id, Provider: model.ProviderAntigravity, Payload: []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"go"}`)}))
	if a := d.shown(id); a.State == model.StateAwaitingInput || a.State == "" {
		t.Fatalf("after trusting, the hook changed nothing: %+v", a)
	}
}

// TestAgyTrustWorktree: a worktree of a repo agy trusts is trusted before
// agy starts in it, in agy's own file and mode; one of a repo it does not
// trust is not.
func TestAgyTrustWorktree(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	wt := filepath.Join(repo, ".worktrees", "fix")
	if err := os.MkdirAll(filepath.Join(repo, ".git", "worktrees", "fix"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(repo, ".git", "worktrees", "fix")+"\n"), 0o644)
	env := []string{"HOME=" + home}
	settings := filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
	if note := agyTrustWorktree(wt, env); note != "" {
		t.Fatalf("no settings yet, yet: %s", note)
	}
	os.MkdirAll(filepath.Dir(settings), 0o700)
	os.WriteFile(settings, []byte(`{"trustedWorkspaces":["/elsewhere"]}`), 0o600)
	if note := agyTrustWorktree(wt, env); note != "" {
		t.Fatalf("an untrusted repo's worktree: %s", note)
	}
	trusted, _ := json.Marshal(map[string][]string{"trustedWorkspaces": {repo}})
	os.WriteFile(settings, trusted, 0o600)
	if note := agyTrustWorktree(wt, env); note == "" {
		t.Fatal("a trusted repo's worktree was not trusted")
	}
	b, _ := os.ReadFile(settings)
	var s struct{ TrustedWorkspaces []string }
	if json.Unmarshal(b, &s) != nil || !slices.Equal(s.TrustedWorkspaces, []string{repo, wt}) {
		t.Fatalf("settings %s", b)
	}
	if info, _ := os.Stat(settings); runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if note := agyTrustWorktree(repo, env); note != "" {
		t.Errorf("the main repo itself: %s", note)
	}
}

// TestAgySubagentHooksKeepMainSession replays real agy main and subagent hook
// fixtures through FromAntigravity and the daemon's hook path: the pane keeps
// its main SessionID, Transcript, Prompt, running turn and resume command,
// while subagent PostToolUse restarts the quiet timer.
func TestAgySubagentHooksKeepMainSession(t *testing.T) {
	const (
		mainID = "11111111-2222-3333-4444-555555555555"
		subID  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	)
	home := t.TempDir()
	brain := agent.AgyBrainDir(home)
	mainTr := agent.AntigravityTranscript(home, mainID)
	subTr := agent.AntigravityTranscript(home, subID)
	for _, p := range []string{
		mainTr,
		subTr,
		filepath.Join(brain, subID, ".system_generated", "messages", "m1.json"),
		filepath.Join(brain, mainID, ".system_generated", "subagents", subID+".json"),
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	loadFixture := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "agent", "testdata", "antigravity", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	os.WriteFile(filepath.Join(brain, subID, ".system_generated", "messages", "m1.json"), loadFixture("subagent_message.json"), 0o600)
	os.WriteFile(filepath.Join(brain, mainID, ".system_generated", "subagents", subID+".json"), loadFixture("subagent_meta.json"), 0o600)
	os.WriteFile(mainTr, []byte(`{"step_index":0,"type":"USER_INPUT","content":"<USER_REQUEST>\nrun audit\n</USER_REQUEST>"}
{"step_index":3,"type":"PLANNER_RESPONSE","status":"DONE","content":"Waiting for the audit subagent."}
`), 0o600)
	os.WriteFile(subTr, []byte(`{"step_index":5,"type":"PLANNER_RESPONSE","status":"DONE","content":"Audit complete and sent to the parent agent via send_message"}
`), 0o600)

	d, f := talkDaemon(t)
	useReportHooks(d)
	d.o.QuietAfter = func() time.Duration { return time.Minute }
	lp := &livePane{fakePane: f.pane(0)}
	d.mu.Lock()
	d.panes["a"] = lp
	d.st.Panes[0].Cmd = []string{"agy", "--mode", "auto"}
	d.st.Panes[0].Env = []string{"HOME=" + home}
	d.mu.Unlock()

	sendAgy := func(name string, conv, tr string) {
		t.Helper()
		var raw map[string]any
		if err := json.Unmarshal(loadFixture(name), &raw); err != nil {
			t.Fatal(err)
		}
		raw["conversationId"] = conv
		raw["artifactDirectoryPath"] = filepath.Join(brain, conv)
		raw["transcriptPath"] = tr
		b, _ := json.Marshal(raw)
		if payload := agent.FromAntigravity(b); payload != nil {
			must(t, d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderAntigravity, Payload: payload}))
		}
	}

	// Main session starts, submits prompt, invokes subagent, waits (fullyIdle=false).
	sendAgy("session_start.json", mainID, mainTr)
	sendAgy("pre_invocation.json", mainID, mainTr)
	sendAgy("main_post_tool_use_subagent.json", mainID, mainTr)
	sendAgy("main_stop_waiting.json", mainID, mainTr)

	p, a := reportedPane(d, "a"), d.activityOf("a")
	if p.SessionID != mainID || p.Transcript != mainTr || a.State != model.StateWorking || !a.Background || a.Detail != "Waiting for the audit subagent." {
		t.Fatalf("after main waiting Stop: pane=%+v act=%+v", p, a)
	}

	// Age the quiet timer, then fire every subagent hook.
	d.checkQuiet()
	age(d, "a", 2*time.Minute)
	sendAgy("subagent_session_start.json", subID, subTr)
	sendAgy("subagent_pre_invocation.json", subID, subTr)
	sendAgy("subagent_post_tool_use.json", subID, subTr)
	sendAgy("subagent_stop.json", subID, subTr)

	p, a = reportedPane(d, "a"), d.activityOf("a")
	if p.SessionID != mainID || p.Transcript != mainTr {
		t.Fatalf("subagent hooks hijacked pane: session=%q transcript=%q", p.SessionID, p.Transcript)
	}
	if a.State != model.StateWorking || !a.Background || a.Detail != "Waiting for the audit subagent." {
		t.Fatalf("subagent hooks changed turn state: %+v", a)
	}
	if !quietSince(d, "a").IsZero() {
		t.Error("subagent PostToolUse did not restart silence timer")
	}

	// Defense in depth: even a raw foreign-session Stop payload while busy is dropped.
	must(t, d.agentEvent(context.Background(), proto.AgentEvent{
		Pane:     "a",
		Provider: model.ProviderAntigravity,
		Payload:  []byte(`{"hook_event_name":"Stop","session_id":"` + subID + `","transcript_path":"` + subTr + `","last_assistant_message":"Audit complete"}`),
	}))
	if p, a = reportedPane(d, "a"), d.activityOf("a"); p.SessionID != mainID || a.State != model.StateWorking {
		t.Fatalf("foreign raw Stop hijacked pane: %+v %+v", p, a)
	}

	// Main wakes up, writes final reply, and stops with fullyIdle=true.
	sendAgy("main_pre_invocation_wake.json", mainID, mainTr)
	os.WriteFile(mainTr, []byte(`{"step_index":0,"type":"USER_INPUT","content":"<USER_REQUEST>\nrun audit\n</USER_REQUEST>"}
{"step_index":5,"type":"PLANNER_RESPONSE","status":"DONE","content":"All audits reviewed."}
`), 0o600)
	sendAgy("stop.json", mainID, mainTr)

	p, a = reportedPane(d, "a"), d.activityOf("a")
	if p.SessionID != mainID || p.Transcript != mainTr || a.State != model.StateCompleted || a.Detail != "All audits reviewed." {
		t.Fatalf("after main final Stop: pane=%+v act=%+v", p, a)
	}

	// A stray subagent PostToolUse after completion must not flip Completed back to Working.
	sendAgy("subagent_post_tool_use.json", subID, subTr)
	if a = d.activityOf("a"); a.State != model.StateCompleted || a.Detail != "All audits reviewed." {
		t.Fatalf("stray subagent PostToolUse clobbered completed state: %+v", a)
	}

	// Resume command points at the main conversation.
	wantCmd := []string{"agy", "--mode", "auto", "--conversation", mainID}
	if got := store.RestoreCmd(p); !reflect.DeepEqual(got, wantCmd) {
		t.Fatalf("RestoreCmd = %q, want %q", got, wantCmd)
	}
}

// TestAgyRepairSubagentSessionOnLoad: a saved Antigravity pane whose SessionID
// was overwritten by a (possibly nested) subagent returns to its main
// conversation and transcript when the daemon loads it.
func TestAgyRepairSubagentSessionOnLoad(t *testing.T) {
	const (
		mainID = "11111111-2222-3333-4444-555555555555"
		sub1   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		sub2   = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	)
	home := t.TempDir()
	brain := agent.AgyBrainDir(home)
	mainTr := agent.AntigravityTranscript(home, mainID)
	sub2Tr := agent.AntigravityTranscript(home, sub2)
	writeFile := func(p, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(mainTr, `{"step_index":0,"type":"USER_INPUT","content":"<USER_REQUEST>\nhi\n</USER_REQUEST>"}`+"\n")
	writeFile(sub2Tr, `{"step_index":0,"type":"SYSTEM_MESSAGE","content":"hi"}`+"\n")
	writeFile(filepath.Join(brain, sub1, ".system_generated", "messages", "m1.json"),
		`{"recipient":"`+sub1+`","sender":"`+mainID+`","sourceMetadata":{"tool":{"conversationId":"`+mainID+`","toolCall":{"name":"invoke_subagent"}}}}`)
	writeFile(filepath.Join(brain, sub2, ".system_generated", "messages", "m2.json"),
		`{"recipient":"`+sub2+`","sender":"`+sub1+`","sourceMetadata":{"tool":{"conversationId":"`+sub1+`","toolCall":{"name":"invoke_subagent"}}}}`)

	dir := t.TempDir()
	st := model.State{
		Sessions:   []model.Session{{ID: "s", Name: "main"}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", Name: "tab", Path: dir, Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{Pane: "p"}}}, ActiveTab: "t"}},
		Panes: []model.Pane{{
			ID:          "p",
			WorkspaceID: "w",
			Cwd:         dir,
			Provider:    model.ProviderAntigravity,
			SessionID:   sub2,
			Transcript:  sub2Tr,
			Cmd:         []string{"agy", "--conversation", sub2},
			Env:         []string{"HOME=" + home},
		}},
	}
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Load = func() (model.State, error) { return st, nil }
	o.RestoreCmd = store.RestoreCmd
	d := newDaemon(t, o)
	t.Cleanup(d.shutdown)
	p := reportedPane(d, "p")
	if p.SessionID != mainID || p.Transcript != mainTr {
		t.Fatalf("loaded pane not repaired: session=%q transcript=%q, want %q %q", p.SessionID, p.Transcript, mainID, mainTr)
	}
	wantCmd := []string{"agy", "--conversation", mainID}
	if got := f.pane(0).cfg.Cmd; !reflect.DeepEqual(got, wantCmd) {
		t.Fatalf("launched cmd = %q, want %q", got, wantCmd)
	}
}

// TestAgyPaneChatSubagents reads real Antigravity main and subagent transcripts
// through paneChat.catchUp and verifies both Agent cards show their calls and
// replies while keeping the main turn open until both subagents complete.
func TestAgyPaneChatSubagents(t *testing.T) {
	const (
		mainID = "11111111-2222-3333-4444-555555555555"
		sub1   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		sub2   = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
	)
	home := t.TempDir()
	mainTr := agent.AntigravityTranscript(home, mainID)
	sub1Tr := agent.AntigravityTranscript(home, sub1)
	sub2Tr := agent.AntigravityTranscript(home, sub2)
	for _, p := range []string{mainTr, sub1Tr, sub2Tr} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	readFixture := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("..", "session", "antigravity", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	os.WriteFile(mainTr, readFixture("subagent_main.jsonl"), 0o600)
	os.WriteFile(sub1Tr, readFixture("subagent_child1.jsonl"), 0o600)
	os.WriteFile(sub2Tr, readFixture("subagent_child2.jsonl"), 0o600)

	pc := &paneChat{
		id:   "a",
		path: mainTr,
		prov: model.ProviderAntigravity,
		snap: session.Snapshot{SessionID: "a", Provider: model.ProviderAntigravity, SessionState: session.StateReady, Running: true},
	}
	pc.tr = pc.transcript()
	if err := pc.catchUp(paneLive{p: &fakePane{}, path: mainTr, prov: model.ProviderAntigravity, home: home}); err != nil {
		t.Fatal(err)
	}
	var cards []*session.ToolCall
	for _, it := range pc.snap.Content {
		if it.Tool != nil && it.Tool.ToolName == "Agent" {
			cards = append(cards, it.Tool)
		}
	}
	if len(cards) != 2 {
		t.Fatalf("got %d Agent cards, want 2: %+v", len(cards), pc.snap.Content)
	}
	for i, c := range cards {
		if c.Status != "completed" || len(c.SubagentCalls) != 2 || c.SubagentCalls[0].ToolName != "Read" || c.SubagentCalls[0].Status != "completed" {
			t.Errorf("card %d: status=%q calls=%+v", i, c.Status, c.SubagentCalls)
		}
		if c.Output == "" {
			t.Errorf("card %d missing reply output", i)
		}
	}
	if len(pc.snap.Turns) != 1 || pc.snap.Turns[0].State != session.TurnCompleted {
		t.Fatalf("turns = %+v, want 1 completed turn", pc.snap.Turns)
	}
}
