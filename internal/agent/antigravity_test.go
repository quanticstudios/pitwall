package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// The payloads in testdata/antigravity are agy 1.5.13's own, from a run
// under a temporary home, with paths and the conversation id replaced.
func agyFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "antigravity", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFromAntigravity(t *testing.T) {
	const sid = "11111111-2222-3333-4444-555555555555"
	now := time.Unix(1_800_000_000, 0)

	// The fixture's own paths are Unix ones; a path of this system stands in, so it is absolute everywhere.
	var raw map[string]any
	json.Unmarshal(agyFixture(t, "session_start.json"), &raw)
	path := filepath.Join(t.TempDir(), sid, ".system_generated", "logs", "transcript_full.jsonl")
	raw["transcriptPath"] = path
	b, _ := json.Marshal(raw)
	start := FromAntigravity(b)
	if SessionID(model.ProviderAntigravity, start) != sid || Transcript(start) != path {
		t.Fatalf("SessionStart: session %q, transcript %q", SessionID(model.ProviderAntigravity, start), Transcript(start))
	}
	if _, ok := Derive(nil, model.ProviderAntigravity, start, now); ok {
		t.Error("SessionStart changed the activity; agy waits at its prompt")
	}

	prompt := FromAntigravity(agyFixture(t, "pre_invocation.json"))
	if got := Prompt(model.ProviderAntigravity, prompt); !strings.HasPrefix(got, "Run exactly this shell command") {
		t.Errorf("first PreInvocation's prompt %q", got)
	}
	a, ok := Derive(nil, model.ProviderAntigravity, prompt, now)
	if !ok || a.State != model.StateWorking || a.SessionID != sid {
		t.Errorf("first PreInvocation: %+v, %v", a, ok)
	}
	if b := FromAntigravity(agyFixture(t, "pre_invocation_next.json")); b != nil {
		t.Errorf("a later PreInvocation read as %s", b)
	}
	if b := FromAntigravity(agyFixture(t, "pre_tool_use_question.json")); b != nil {
		t.Errorf("PreToolUse, which pitwall does not register, read as %s", b)
	}

	post := FromAntigravity(agyFixture(t, "post_tool_use.json"))
	if a, ok := Derive(nil, model.ProviderAntigravity, post, now); !ok || a.State != model.StateWorking {
		t.Errorf("PostToolUse: %+v, %v", a, ok)
	}
	if _, failed := ToolFailure(model.ProviderAntigravity, post); failed {
		t.Error("a command that succeeded read as a failure")
	}
	if c, ok := ToolCall(post); !ok || !c.End || c.Tool != "run_command" || c.Command != "mkdir -p ./junk2 && rmdir ./junk2" || c.ID != "call_12900578850290419103" {
		t.Errorf("PostToolUse call %+v, %v", c, ok)
	}

	// A command the user stopped: the same payload with the error agy gives it.
	raw = nil
	json.Unmarshal(agyFixture(t, "post_tool_use.json"), &raw)
	raw["error"] = "command was terminated by a signal: context canceled"
	b, _ = json.Marshal(raw)
	f, failed := ToolFailure(model.ProviderAntigravity, FromAntigravity(b))
	if !failed || !f.Interrupted || !f.Shell || f.Command != "mkdir -p ./junk2 && rmdir ./junk2" || f.Output != "command was terminated by a signal: context canceled" {
		t.Errorf("stopped command: %+v, %v", f, failed)
	}

	// Stop takes its reply from the transcript the payload names.
	dir := t.TempDir()
	tr := filepath.Join(dir, "transcript_full.jsonl")
	os.WriteFile(tr, []byte(`{"step_index":0,"type":"USER_INPUT","content":"<USER_REQUEST>\nhi\n</USER_REQUEST>"}
{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","content":"Ran it; **all good**."}
`), 0o600)
	stop := func(reason, errText string) []byte {
		var raw map[string]any
		json.Unmarshal(agyFixture(t, "stop.json"), &raw)
		raw["transcriptPath"], raw["terminationReason"], raw["error"] = tr, reason, errText
		b, _ := json.Marshal(raw)
		return FromAntigravity(b)
	}
	working := &model.Activity{Provider: model.ProviderAntigravity, State: model.StateWorking, SessionID: sid}
	if a, ok := Derive(working, model.ProviderAntigravity, stop("NO_TOOL_CALL", ""), now); !ok || a.State != model.StateCompleted || a.Detail != "Ran it; **all good**." {
		t.Errorf("Stop: %+v, %v", a, ok)
	}
	if got := LastMessage(stop("NO_TOOL_CALL", "")); got != "Ran it; **all good**." {
		t.Errorf("Stop's last message %q", got)
	}
	if a, ok := Derive(working, model.ProviderAntigravity, stop("ERROR", "quota exhausted"), now); !ok || a.State != model.StateError || a.Detail != "quota exhausted" {
		t.Errorf("Stop ERROR: %+v, %v", a, ok)
	}
	if a, ok := Derive(working, model.ProviderAntigravity, stop("USER_CANCELED", ""), now); !ok || a.State != "" {
		t.Errorf("Stop USER_CANCELED: %+v, %v; want the activity gone", a, ok)
	}

	for name, b := range map[string]string{
		"not JSON":       `{`,
		"no event":       `{"conversationId":"x"}`,
		"PostInvocation": `{"hookEventName":"PostInvocation","conversationId":"x","invocationNum":0}`,
		"Claude's shape": `{"hook_event_name":"Stop","session_id":"x"}`,
	} {
		if got := FromAntigravity([]byte(b)); got != nil {
			t.Errorf("%s: %s", name, got)
		}
	}
	// A transcript path that names some other file is not read or passed on.
	raw = map[string]any{"hookEventName": "Stop", "conversationId": "x", "transcriptPath": "/etc/passwd", "terminationReason": "NO_TOOL_CALL"}
	b, _ = json.Marshal(raw)
	if got := FromAntigravity(b); Transcript(got) != "" || LastMessage(got) != "" {
		t.Errorf("foreign transcript path kept: %s", got)
	}
}

func TestAntigravityHooks(t *testing.T) {
	var got map[string]map[string][]map[string]any
	if err := json.Unmarshal(AntigravityHooks("/opt/pitwall"), &got); err != nil {
		t.Fatal(err)
	}
	hooks := got[AntigravityHookName]
	if len(got) != 1 || len(hooks) != 4 || hooks["PreToolUse"] != nil {
		t.Fatalf("hooks %v; want SessionStart, PreInvocation, PostToolUse and Stop under %q", got, AntigravityHookName)
	}
	if c := hooks["Stop"][0]["command"]; c != commandPath(runtime.GOOS, "/opt/pitwall")+" hook antigravity" || hooks["Stop"][0]["timeout"] != float64(5) {
		t.Errorf("Stop handler %v", hooks["Stop"][0])
	}
	g := hooks["PostToolUse"][0]
	if inner, _ := g["hooks"].([]any); g["matcher"] != "*" || len(inner) != 1 {
		t.Errorf("PostToolUse group %v", g)
	}
}

func TestIdentifyAntigravity(t *testing.T) {
	for _, c := range [][2]string{{"agy", ""}, {"antigravity", ""}, {"x", "/home/u/.local/share/mise/installs/antigravity-cli/1.5.13/antigravity"}} {
		if p := Identify(c[0], c[1]); p != model.ProviderAntigravity {
			t.Errorf("Identify(%q, %q) = %q", c[0], c[1], p)
		}
	}
	if Command(model.ProviderAntigravity) != "agy" || !HooksFor(model.ProviderAntigravity) {
		t.Error("agy's command or hooks")
	}
}

// TestAgyFooter reads the footers agy 1.5.13 drew: with a mode word, in
// its default mode without one, and none while a picker covers it.
func TestAgyFooter(t *testing.T) {
	screen := func(rows ...string) vt.Grid {
		e := vt.New(100, 6, nil)
		e.Write([]byte(strings.Join(rows, "\r\n")))
		return e.Snapshot()
	}
	pad := func(left, right string) string {
		return left + strings.Repeat(" ", 100-len([]rune(left))-len([]rune(right))) + right
	}
	for _, c := range []struct {
		name                string
		g                   vt.Grid
		mode, model, effort string
		ok                  bool
	}{
		{"auto", screen("> ", pad("? for shortcuts", "auto · Gemini 3.8 Flash · low")), "auto", "Gemini 3.8 Flash", "low", true},
		{"busy, accept edits", screen(pad("esc to cancel", "accept-edits · Claude Sonnet 5.5 · medium")), "accept-edits", "Claude Sonnet 5.5", "medium", true},
		{"default mode", screen(pad("? for shortcuts", "Gemini 4 Neon · xhigh")), "default", "Gemini 4 Neon", "xhigh", true},
		{"picker", screen("Keyboard: ↑/↓ Navigate  ←/→ Effort  enter Select  esc Go Back"), "", "", "", false},
		{"prose with dots", screen(pad("", "a · b · c")), "", "", "", false},
	} {
		mode, mdl, eff, ok := AgyFooter(c.g)
		if mode != c.mode || mdl != c.model || eff != c.effort || ok != c.ok {
			t.Errorf("%s: %q %q %q %v", c.name, mode, mdl, eff, ok)
		}
	}
	b, err := os.ReadFile(filepath.Join("testdata", "antigravity-footer.txt"))
	if err != nil {
		t.Fatal(err)
	}
	e := vt.New(100, 30, nil)
	e.Write([]byte(strings.ReplaceAll(strings.TrimRight(string(b), "\n"), "\n", "\r\n")))
	if mode, mdl, eff, ok := AgyFooter(e.Snapshot()); mode != "accept-edits" || mdl != "Gemini 3.8 Flash" || eff != "low" || !ok {
		t.Errorf("idle screen: %q %q %q %v", mode, mdl, eff, ok)
	}
}

func TestAntigravityTranscript(t *testing.T) {
	home := t.TempDir()
	want := filepath.Join(home, ".gemini", "antigravity-cli", "brain", "11111111-2222-3333-4444-555555555555", ".system_generated", "logs", "transcript_full.jsonl")
	if got := AntigravityTranscript(home, "11111111-2222-3333-4444-555555555555"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	for _, bad := range []string{"", ".", "..", "../x", `a\b`} {
		if got := AntigravityTranscript(home, bad); got != "" {
			t.Errorf("id %q gave %q", bad, got)
		}
	}
}

// TestAgyAlwaysOffered: agy's command prompt offers option 2; its trust
// question, whose second row exits, does not.
func TestAgyAlwaysOffered(t *testing.T) {
	read := func(name string) vt.Grid {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		e := vt.New(100, 30, nil)
		e.Write([]byte(strings.ReplaceAll(strings.TrimRight(string(b), "\n"), "\n", "\r\n")))
		return e.Snapshot()
	}
	if !AgyAlwaysOffered(read("antigravity-approval.txt")) || AgyAlwaysOffered(read("antigravity-trust.txt")) {
		t.Error("option 2 read wrong")
	}
}

// TestAgyTrust: a worktree is added the way agy records trust itself,
// only when its main repo is trusted, and never twice.
func TestAgyTrust(t *testing.T) {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	app, fix := filepath.FromSlash("/src/app"), filepath.FromSlash("/src/app/.worktrees/fix")
	data := []byte("{\n  \"theme\": \"dark\",\n  \"trustedWorkspaces\": [\n    " + q(app) + "\n  ]\n}\n")
	out, ok, err := AgyTrust(data, app, fix)
	if err != nil || !ok {
		t.Fatalf("trusted repo: %v %v", ok, err)
	}
	if want := "{\n  \"theme\": \"dark\",\n  \"trustedWorkspaces\": [\n    " + q(app) + ",\n    " + q(fix) + "\n  ]\n}\n"; string(out) != want {
		t.Fatalf("settings:\n%s\nwant\n%s", out, want)
	}
	if _, ok, _ := AgyTrust(out, app, fix); ok {
		t.Error("trusted twice")
	}
	// A path agy wrote with a trailing separator is the same folder.
	if _, ok, _ := AgyTrust([]byte(`{"trustedWorkspaces":[`+q(app+string(filepath.Separator))+`]}`), app, fix); !ok {
		t.Error("a trusted repo written with a trailing separator was not matched")
	}
	for name, d := range map[string]string{
		"repo not trusted": `{"trustedWorkspaces":["/src/other"]}`,
		"nothing trusted":  `{}`,
		"a parent of repo": `{"trustedWorkspaces":["/src"]}`,
	} {
		if out, ok, err := AgyTrust([]byte(d), filepath.FromSlash("/src/app"), filepath.FromSlash("/src/app/.worktrees/fix")); ok || err != nil || string(out) != d {
			t.Errorf("%s: trusted %v, %v, %s", name, ok, err, out)
		}
	}
	if _, ok, err := AgyTrust([]byte(`{"trustedWorkspaces":"x"}`), "/src/app", "/w"); ok || err == nil {
		t.Error("a bad trustedWorkspaces read as a list")
	}
}

// TestWorktreeMain finds a linked worktree's main repo from its .git file.
func TestWorktreeMain(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	main := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command(git, args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run(main, "init", "-q")
	run(main, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(main, ".worktrees", "fix")
	run(main, "worktree", "add", "-q", wt)
	os.MkdirAll(filepath.Join(wt, "sub"), 0o755)
	real, _ := filepath.EvalSymlinks(main)
	for dir, want := range map[string]string{wt: real, filepath.Join(wt, "sub"): real, main: "", t.TempDir(): ""} {
		if got := WorktreeMain(dir); got != want && !(want != "" && got == main) {
			t.Errorf("WorktreeMain(%s) = %q, want %q", dir, got, want)
		}
	}
}

// TestAgyTrustOffers: on agy's trust question the tab offers Trust (Enter)
// and Don't trust (Esc, which closes agy as "No, exit" does).
func TestAgyTrustOffers(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "antigravity-trust.txt"))
	if err != nil {
		t.Fatal(err)
	}
	e := vt.New(100, 30, nil)
	e.Write([]byte(strings.ReplaceAll(strings.TrimRight(string(b), "\n"), "\n", "\r\n")))
	g := e.Snapshot()
	a := model.Activity{Provider: model.ProviderAntigravity, State: model.StateAwaitingInput}
	got := Offers(a, g)
	if len(got) != 2 || got[0] != (Offer{Trust, "⏎"}) || got[1] != (Offer{DontTrust, "⎋"}) {
		t.Fatalf("offers %+v", got)
	}
	if !AgyTrustAsked(g) || StateOf(model.ProviderAntigravity, g) != model.StateAwaitingInput {
		t.Error("the trust question reads as no question")
	}
	a.State = model.StatePendingApproval
	if Offers(a, g) != nil {
		t.Error("offers for another state")
	}
}

// TestAgyPromptDetectionAndDetail checks every recorded agy screen fixture
// (approval, wrapped approval, question with and without Recommended, folder
// trust, plan review, working, working with numbered list, user-opened /model
// and /settings menus, idle) through StateOf, ReadScreenOf, AgyPromptDetail,
// ReadQuestion and Offers.
func TestAgyPromptDetectionAndDetail(t *testing.T) {
	read := func(name string) vt.Grid {
		t.Helper()
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		e := vt.New(100, 30, nil)
		e.Write([]byte(strings.ReplaceAll(strings.TrimRight(string(b), "\n"), "\n", "\r\n")))
		return e.Snapshot()
	}

	for _, tc := range []struct {
		file       string
		wantState  model.AgentState
		wantScreen Screen
		wantDetail string
		wantOffers []Offer
		wantQOpts  []string
	}{
		{
			file:       "antigravity-approval.txt",
			wantState:  model.StatePendingApproval,
			wantScreen: ScreenForm,
			wantDetail: "Approve Bash: rm -rf ./junk",
			wantOffers: []Offer{{Label: AllowAlways, Key: "2"}},
		},
		{
			file:       "antigravity-approval-wrapped.txt",
			wantState:  model.StatePendingApproval,
			wantScreen: ScreenForm,
			wantDetail: "Approve Bash: echo hello-repro",
			wantOffers: []Offer{{Label: AllowAlways, Key: "2"}},
		},
		{
			file:       "antigravity-question.txt",
			wantState:  model.StateAwaitingInput,
			wantScreen: ScreenForm,
			wantDetail: "Which color do you like?",
			wantOffers: []Offer{{Label: "(Recommended) Red", Key: "1"}, {Label: "Blue", Key: "2"}},
			wantQOpts:  []string{"(Recommended) Red", "Blue"},
		},
		{
			file:       "antigravity-question-no-rec.txt",
			wantState:  model.StateAwaitingInput,
			wantScreen: ScreenForm,
			wantDetail: "Which framework do you prefer?",
			wantOffers: []Offer{{Label: "Go", Key: "1"}, {Label: "Rust", Key: "2"}},
			wantQOpts:  []string{"Go", "Rust"},
		},
		{
			file:       "antigravity-trust.txt",
			wantState:  model.StateAwaitingInput,
			wantScreen: ScreenForm,
			wantDetail: "Trust this folder?",
			wantOffers: []Offer{{Label: Trust, Key: "⏎"}, {Label: DontTrust, Key: "⎋"}},
		},
		{
			file:       "antigravity-plan-artifact.txt",
			wantState:  model.StatePlanReady,
			wantScreen: ScreenForm,
			wantDetail: "Approve plan",
			wantOffers: []Offer{{Label: ApprovePlan, Key: "y"}},
		},
		{
			file:       "antigravity-working.txt",
			wantState:  model.StateWorking,
			wantScreen: ScreenBusy,
		},
		{
			file:       "antigravity-working-list.txt",
			wantState:  model.StateWorking,
			wantScreen: ScreenBusy,
		},
		{
			file:       "antigravity-working-generating.txt",
			wantState:  model.StateWorking,
			wantScreen: ScreenBusy,
		},
		{
			file:       "antigravity-model-menu.txt",
			wantState:  "",
			wantScreen: ScreenForm,
		},
		{
			file:       "antigravity-settings-menu.txt",
			wantState:  "",
			wantScreen: ScreenForm,
		},
		{
			file:       "antigravity-idle.txt",
			wantState:  "",
			wantScreen: ScreenNone,
		},
		{
			file:       "antigravity-plan.txt",
			wantState:  "",
			wantScreen: ScreenNone,
		},
	} {
		t.Run(tc.file, func(t *testing.T) {
			g := read(tc.file)
			if got := StateOf(model.ProviderAntigravity, g); got != tc.wantState {
				t.Errorf("StateOf = %q, want %q", got, tc.wantState)
			}
			if got := ReadScreenOf(model.ProviderAntigravity, g); got != tc.wantScreen {
				t.Errorf("ReadScreenOf = %d, want %d", got, tc.wantScreen)
			}
			if got := ScreenDetail(model.ProviderAntigravity, tc.wantState, g); got != tc.wantDetail {
				t.Errorf("ScreenDetail = %q, want %q", got, tc.wantDetail)
			}
			a := model.Activity{Provider: model.ProviderAntigravity, State: tc.wantState}
			gotOffers := Offers(a, g)
			if len(gotOffers) != len(tc.wantOffers) {
				t.Fatalf("Offers = %+v, want %+v", gotOffers, tc.wantOffers)
			}
			for i := range gotOffers {
				if gotOffers[i] != tc.wantOffers[i] {
					t.Errorf("Offers[%d] = %+v, want %+v", i, gotOffers[i], tc.wantOffers[i])
				}
			}
			q := ReadQuestion(model.ProviderAntigravity, g)
			if len(tc.wantQOpts) == 0 {
				if q != nil {
					t.Errorf("ReadQuestion = %+v, want nil", q)
				}
			} else {
				if q == nil || !q.Other || q.OtherIndex != len(tc.wantQOpts) || q.Cursor != 0 || len(q.Options) != len(tc.wantQOpts) {
					t.Fatalf("ReadQuestion = %+v, want options %v", q, tc.wantQOpts)
				}
				for i, want := range tc.wantQOpts {
					if q.Options[i].Label != want {
						t.Errorf("option %d = %q, want %q", i, q.Options[i].Label, want)
					}
				}
			}
		})
	}

	// A question scrolled into transcript above a new prompt must not match.
	g := read("antigravity-question.txt")
	e := vt.New(100, 40, nil)
	e.Write([]byte(strings.ReplaceAll(ScreenText(g, 30)+"\n> New prompt", "\n", "\r\n")))
	if q := ReadQuestion(model.ProviderAntigravity, e.Snapshot()); q != nil {
		t.Errorf("question above a prompt matched: %+v", q)
	}
}

// TestSamePath: a trusted folder matches through a symlink, a trailing
// separator and, on Windows, another case or slash.
func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "repo")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if !samePath(real+string(filepath.Separator), real) {
		t.Error("a trailing separator")
	}
	if link := filepath.Join(dir, "link"); os.Symlink(real, link) == nil && !samePath(link, real) {
		t.Error("a symlink to the folder")
	}
	if samePath(real, filepath.Join(dir, "other")) {
		t.Error("another folder")
	}
	if runtime.GOOS == "windows" && (!samePath(strings.ToUpper(real), real) || !samePath(filepath.ToSlash(real), real)) {
		t.Error("case or forward slashes on Windows")
	}
}

func TestFromAntigravitySubagents(t *testing.T) {
	const (
		mainID = "11111111-2222-3333-4444-555555555555"
		subID  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	)
	now := time.Unix(1_800_000_000, 0)

	// Subagent SessionStart, PreInvocation and Stop return nil so they never
	// touch the pane's SessionID, Transcript, Prompt or turn state.
	for _, name := range []string{"subagent_session_start.json", "subagent_pre_invocation.json", "subagent_stop.json"} {
		if got := FromAntigravity(agyFixture(t, name)); got != nil {
			t.Errorf("%s: got %s, want nil", name, got)
		}
	}

	// Subagent PostToolUse carries agent_id and omits session_id/transcript_path
	// so the daemon counts it as activity without switching session or state.
	subPost := FromAntigravity(agyFixture(t, "subagent_post_tool_use.json"))
	if subPost == nil {
		t.Fatal("subagent PostToolUse returned nil")
	}
	if !SubagentHook(subPost) {
		t.Errorf("subagent PostToolUse not marked as SubagentHook: %s", subPost)
	}
	if SessionID(model.ProviderAntigravity, subPost) != "" || Transcript(subPost) != "" {
		t.Errorf("subagent PostToolUse leaked session/transcript: %s", subPost)
	}
	if c, ok := ToolCall(subPost); !ok || !c.End || c.Tool != "read_file" || c.ID != "call_16567486039228421041" {
		t.Errorf("subagent PostToolUse call %+v, %v", c, ok)
	}

	// Failed subagent PostToolUse preserves error and still omits session/transcript.
	var raw map[string]any
	json.Unmarshal(agyFixture(t, "subagent_post_tool_use.json"), &raw)
	raw["error"] = "permission denied"
	b, _ := json.Marshal(raw)
	subFail := FromAntigravity(b)
	if !SubagentHook(subFail) || SessionID(model.ProviderAntigravity, subFail) != "" {
		t.Errorf("subagent PostToolUse failure: %s", subFail)
	}
	if f, ok := ToolFailure(model.ProviderAntigravity, subFail); !ok || f.Output != "permission denied" {
		t.Errorf("subagent failure %+v, %v", f, ok)
	}

	// On-disk parent evidence classifies a subagent even if lastUserInput is set.
	home := t.TempDir()
	brain := AgyBrainDir(home)
	msgDir := filepath.Join(brain, subID, ".system_generated", "messages")
	if err := os.MkdirAll(msgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(msgDir, "m1.json"), agyFixture(t, "subagent_message.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw = nil
	json.Unmarshal(agyFixture(t, "subagent_stop.json"), &raw)
	raw["lastUserInput"] = "unexpected non-empty"
	raw["artifactDirectoryPath"] = filepath.Join(brain, subID)
	raw["transcriptPath"] = AntigravityTranscript(home, subID)
	b, _ = json.Marshal(raw)
	if got := FromAntigravity(b); got != nil {
		t.Errorf("on-disk subagent Stop with lastUserInput: got %s, want nil", got)
	}

	// Main Stop with fullyIdle=false keeps the pane working in background mode
	// instead of ending the turn while subagents run.
	tr := AntigravityTranscript(home, mainID)
	if err := os.MkdirAll(filepath.Dir(tr), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(tr, []byte(`{"step_index":0,"type":"USER_INPUT","content":"<USER_REQUEST>\nrun audit\n</USER_REQUEST>"}
{"step_index":3,"type":"PLANNER_RESPONSE","status":"DONE","content":"Waiting for the subagent to reply."}
`), 0o600)
	raw = nil
	json.Unmarshal(agyFixture(t, "main_stop_waiting.json"), &raw)
	raw["artifactDirectoryPath"] = filepath.Join(brain, mainID)
	raw["transcriptPath"] = tr
	b, _ = json.Marshal(raw)
	waiting := FromAntigravity(b)
	if !BackgroundRunning(waiting) {
		t.Fatalf("main Stop fullyIdle=false did not set BackgroundRunning: %s", waiting)
	}
	working := &model.Activity{Provider: model.ProviderAntigravity, State: model.StateWorking, SessionID: mainID}
	if a, ok := Derive(working, model.ProviderAntigravity, waiting, now); !ok || a.State != model.StateWorking || a.Detail != "Waiting for the subagent to reply." {
		t.Errorf("main Stop fullyIdle=false: %+v, %v", a, ok)
	}

	// Main PreInvocation waking on a subagent message keeps the main session.
	wake := FromAntigravity(agyFixture(t, "main_pre_invocation_wake.json"))
	if SessionID(model.ProviderAntigravity, wake) != mainID || Prompt(model.ProviderAntigravity, wake) == "" {
		t.Errorf("main wake PreInvocation: %s", wake)
	}
}

func TestAgyParentAndMainConversation(t *testing.T) {
	const (
		mainID = "11111111-2222-3333-4444-555555555555"
		sub1   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
		sub2   = "bbbbbbbb-cccc-dddd-eeee-ffffffffffff"
		sub3   = "cccccccc-dddd-eeee-ffff-000000000000"
	)
	home := t.TempDir()
	brain := AgyBrainDir(home)

	writeFile := func(rel string, data []byte) {
		t.Helper()
		p := filepath.Join(brain, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Depth 1: mainID -> sub1 via messages/*.json (invoke_subagent) and parent subagents/*.json.
	writeFile(filepath.Join(sub1, ".system_generated", "messages", "d341.json"), agyFixture(t, "subagent_message.json"))
	writeFile(filepath.Join(mainID, ".system_generated", "subagents", sub1+".json"), agyFixture(t, "subagent_meta.json"))
	// Main conversation received a send_message reply from sub1: must NOT make sub1 main's parent.
	writeFile(filepath.Join(mainID, ".system_generated", "messages", "reply.json"), []byte(`{
		"recipient":"`+mainID+`","sender":"`+sub1+`",
		"renderDetails":{"messageTitle":"Message from Note File Reader (research)"},
		"sourceMetadata":{"tool":{"conversationId":"`+sub1+`","toolCall":{"name":"send_message"}}}
	}`))
	// Depth 2: sub1 -> sub2.
	writeFile(filepath.Join(sub2, ".system_generated", "messages", "m2.json"), []byte(`{
		"recipient":"`+sub2+`","sender":"`+sub1+`",
		"renderDetails":{"messageTitle":"Message from Parent Agent"},
		"sourceMetadata":{"tool":{"conversationId":"`+sub1+`","toolCall":{"name":"invoke_subagent"}}}
	}`))
	// Fallback without messages/: step 0 SYSTEM_MESSAGE + parent's subagents/<id>.json.
	writeFile(filepath.Join(sub3, ".system_generated", "logs", "transcript_full.jsonl"), []byte(
		`{"step_index":0,"source":"SYSTEM","type":"SYSTEM_MESSAGE","content":"<SYSTEM_MESSAGE>\n[Message] timestamp=2026-11-22T16:51:35Z sender=`+mainID+` priority=MESSAGE_PRIORITY_HIGH content=hi\n</SYSTEM_MESSAGE>"}`+"\n",
	))
	writeFile(filepath.Join(mainID, ".system_generated", "subagents", sub3+".json"), []byte(`{"conversationId":"`+sub3+`"}`))

	if got := AgyParent(brain, mainID); got != "" {
		t.Errorf("main conversation parent = %q, want empty", got)
	}
	if got := AgyParent(brain, sub1); got != mainID {
		t.Errorf("sub1 parent = %q, want %q", got, mainID)
	}
	if got := AgyParent(brain, sub2); got != sub1 {
		t.Errorf("sub2 parent = %q, want %q", got, sub1)
	}
	if got := AgyParent(brain, sub3); got != mainID {
		t.Errorf("sub3 fallback parent = %q, want %q", got, mainID)
	}
	if got := AgyMainConversation(home, mainID); got != "" {
		t.Errorf("main root = %q, want empty", got)
	}
	if got := AgyMainConversation(home, sub1); got != mainID {
		t.Errorf("sub1 root = %q, want %q", got, mainID)
	}
	if got := AgyMainConversation(home, sub2); got != mainID {
		t.Errorf("sub2 nested root = %q, want %q", got, mainID)
	}
	if got := AgyMainConversation(home, sub3); got != mainID {
		t.Errorf("sub3 fallback root = %q, want %q", got, mainID)
	}
}
