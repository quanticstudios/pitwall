package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func screenFixture(t *testing.T, file string) vt.Grid {
	t.Helper()
	b, err := os.ReadFile("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	e := vt.New(100, 30, nil)
	e.Write([]byte(strings.ReplaceAll(strings.TrimRight(string(b), "\n"), "\n", "\r\n")))
	return e.Snapshot()
}

// The claude-*.txt fixtures are pitwall's own emulator reading Claude Code
// 2.1.287 in a 100x30 PTY, except claude-idle.txt, which is claude-spinner.txt
// with the spinner turned into the line a finished turn leaves. codex-*.txt
// are Codex 0.160.0 read the same way, except codex-trust.txt, which is
// written from a capture of Codex's folder trust dialog. The tuios-*.txt ones are tuios's screen
// fixtures, and tuios-claude-question.txt is tuios's derived select form.
func TestReadScreen(t *testing.T) {
	for file, want := range map[string]Screen{
		"claude-streaming.txt":          ScreenNone, // reply streaming, spinner hidden
		"claude-interrupted.txt":        ScreenNone, // Esc mid-reply
		"claude-denied.txt":             ScreenNone, // Esc at the permission prompt
		"claude-spinner.txt":            ScreenBusy,
		"claude-permission.txt":         ScreenForm,
		"claude-permission-warning.txt": ScreenForm,
		"tuios-claude-code-working.txt": ScreenBusy,
		"tuios-codex-working.txt":       ScreenBusy,
		"tuios-codex-approval.txt":      ScreenForm,
		"codex-trust.txt":               ScreenForm,
	} {
		if got := ReadScreen(screenFixture(t, file)); got != want {
			t.Errorf("%s: got %d, want %d", file, got, want)
		}
	}
}

// The draft fixtures are claude-idle.txt and codex-idle.txt with the box
// changed. Claude's placeholder has the cursor on its first letter.
func TestInputReady(t *testing.T) {
	for _, tc := range []struct {
		p     model.Provider
		file  string
		ready bool
	}{
		{model.ProviderClaude, "claude-idle.txt", true},
		{model.ProviderClaude, "claude-draft.txt", true},
		{model.ProviderCodex, "codex-empty.txt", true},
		{model.ProviderCodex, "codex-placeholder.txt", true},
		{model.ProviderCodex, "codex-trust.txt", false},
	} {
		if got := InputReady(tc.p, screenFixture(t, tc.file)); got != tc.ready {
			t.Errorf("%s: ready %v, want %v", tc.file, got, tc.ready)
		}
	}
	if InputReady(model.ProviderClaude, vt.Grid{}) || InputReady(model.ProviderCodex, vt.Grid{}) {
		t.Fatal("a blank startup screen is not an input box")
	}
}

func TestDraft(t *testing.T) {
	for _, tc := range []struct {
		p     model.Provider
		file  string
		draft bool
	}{
		{model.ProviderClaude, "claude-idle.txt", false},
		{model.ProviderClaude, "claude-placeholder.txt", false},
		{model.ProviderClaude, "claude-draft.txt", true},
		{model.ProviderClaude, "claude-draft-multiline.txt", true},
		{model.ProviderClaude, "claude-draft-second-line.txt", true},
		{model.ProviderClaude, "claude-draft-rule.txt", true},
		{model.ProviderCodex, "codex-empty.txt", false},
		{model.ProviderCodex, "codex-placeholder.txt", false},
		{model.ProviderCodex, "codex-draft.txt", true},
		{model.ProviderCodex, "codex-draft-multiline.txt", true},
		{model.ProviderCodex, "codex-draft-second-line.txt", true},
		{model.ProviderCodex, "codex-draft-third-line.txt", true},
		{model.ProviderCodex, "codex-placeholder-words.txt", true}, // typed, not faint
		{model.ProviderCodex, "codex-shell.txt", true},
		// No known box, or no box on screen: nothing to hold for.
		{model.ProviderGemini, "claude-draft.txt", false},
		{model.ProviderPi, "codex-draft.txt", false},
		{model.ProviderClaude, "codex-draft.txt", false},
		{model.ProviderCodex, "claude-draft.txt", false},
		{model.ProviderClaude, "tuios-claude-question.txt", false},
	} {
		if got := Draft(tc.p, screenFixture(t, tc.file)); got != tc.draft {
			t.Errorf("%s %s: draft %v, want %v", tc.p, tc.file, got, tc.draft)
		}
	}
	if Draft(model.ProviderClaude, vt.Grid{}) || Draft(model.ProviderCodex, vt.Grid{}) {
		t.Error("an empty grid reads as a draft")
	}
}

func TestState(t *testing.T) {
	for file, want := range map[string]model.AgentState{
		"claude-spinner.txt":            model.StateWorking,
		"claude-permission.txt":         model.StatePendingApproval,
		"claude-permission-warning.txt": model.StatePendingApproval,
		"tuios-claude-question.txt":     model.StateAwaitingInput,
		"claude-idle.txt":               "",
		"claude-streaming.txt":          "",
		"codex-working.txt":             model.StateWorking,
		"tuios-codex-approval.txt":      model.StatePendingApproval,
		"codex-idle.txt":                "",
		"codex-trust.txt":               model.StatePendingApproval,
	} {
		if got := State(screenFixture(t, file)); got != want {
			t.Errorf("%s: got %q, want %q", file, got, want)
		}
	}
}

func TestIdentify(t *testing.T) {
	for _, tc := range []struct {
		comm, exe string
		want      model.Provider
	}{
		{"claude", "/home/u/.local/share/claude/versions/2.1.287", model.ProviderClaude},
		{"codex", "/home/u/.local/share/mise/installs/codex/0.160.0/bin/codex", model.ProviderCodex},
		{"MainThread", "/usr/lib/codex/codex", model.ProviderCodex},
		{"pi", "/home/u/.local/share/mise/installs/pi/latest/pi/pi", model.ProviderPi},
		{"omp", "/home/u/.local/share/mise/installs/github-can1357-oh-my-pi/18.12.6/omp", model.ProviderOmp},
		{"gemini", "/usr/bin/gemini", model.ProviderGemini},
		{"opencode", "/home/u/.npm/lib/node_modules/opencode-ai/bin/opencode.exe", model.ProviderOpenCode},
		{"cursor-agent", "", model.ProviderCursor},
		{"aider", "/usr/bin/python3.14", model.ProviderAider}, // a python shebang keeps the script's name
		{"node", "/usr/bin/node", ""},
		{"zsh", "/usr/bin/zsh", ""},
		{"", "", ""},
	} {
		if got := Identify(tc.comm, tc.exe); got != tc.want {
			t.Errorf("Identify(%q, %q) = %q, want %q", tc.comm, tc.exe, got, tc.want)
		}
	}
}

// The gemini, opencode and aider fixtures are hand-written from the
// strings in each agent's source: Gemini CLI's LoadingIndicator and
// ToolConfirmationQueue, OpenCode's prompt, permission and question views,
// and aider's io.py and its WaitingSpinner.
func TestStateOf(t *testing.T) {
	for _, tc := range []struct {
		p    model.Provider
		file string
		want model.AgentState
	}{
		{model.ProviderGemini, "gemini-working.txt", model.StateWorking},
		{model.ProviderGemini, "gemini-approval.txt", model.StatePendingApproval},
		{model.ProviderGemini, "gemini-plan.txt", model.StatePlanReady},
		{model.ProviderGemini, "gemini-question.txt", model.StateAwaitingInput},
		{model.ProviderGemini, "gemini-idle.txt", ""},
		{model.ProviderOpenCode, "opencode-working.txt", model.StateWorking},
		{model.ProviderOpenCode, "opencode-permission.txt", model.StatePendingApproval},
		{model.ProviderOpenCode, "opencode-plan.txt", model.StatePlanReady},
		{model.ProviderOpenCode, "opencode-question.txt", model.StateAwaitingInput},
		{model.ProviderOpenCode, "opencode-idle.txt", ""},
		{model.ProviderAider, "aider-confirm.txt", model.StateAwaitingInput},
		{model.ProviderAider, "aider-working.txt", model.StateWorking},
		{model.ProviderAider, "aider-idle.txt", ""},
		{model.ProviderClaude, "claude-spinner.txt", model.StateWorking},
		{model.ProviderAmp, "claude-spinner.txt", ""}, // no rules: nothing is guessed
	} {
		if got := StateOf(tc.p, screenFixture(t, tc.file)); got != tc.want {
			t.Errorf("%s %s: got %q, want %q", tc.p, tc.file, got, tc.want)
		}
	}
}

func TestScript(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want model.Provider
	}{
		{[]string{"node", "/usr/bin/gemini"}, model.ProviderGemini},
		{[]string{"/usr/bin/node", "--max-old-space-size=8192", "/usr/lib/node_modules/@google/gemini-cli/bundle/gemini.js", "--yolo"}, model.ProviderGemini},
		{[]string{"node", "/home/u/.local/share/cursor-agent/versions/2026.10.01/index.js"}, model.ProviderCursor},
		{[]string{"bun", "/home/u/.bun/bin/opencode"}, model.ProviderOpenCode},
		{[]string{"deno", "run", "-A", "/home/u/bin/amp.ts"}, model.ProviderAmp},
		{[]string{"python3", "/home/u/.local/bin/aider", "--model", "x"}, model.ProviderAider},
		{[]string{"python3", "-m", "aider"}, model.ProviderAider},
		{[]string{"claude"}, model.ProviderClaude},             // a process title
		{[]string{"node", "server.js", "/usr/bin/gemini"}, ""}, // only the script counts
		{[]string{"python3", "-c", "print(1)"}, ""},
		{nil, ""},
	} {
		if got := Script(tc.argv); got != tc.want {
			t.Errorf("Script(%q) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}
