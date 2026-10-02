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
// are Codex 0.160.0 read the same way. The tuios-*.txt ones are tuios's screen
// fixtures, and tuios-claude-question.txt is tuios's derived select form.
func TestReadScreen(t *testing.T) {
	for file, want := range map[string]Screen{
		"claude-streaming.txt":          ScreenNone, // reply streaming, spinner hidden
		"claude-interrupted.txt":        ScreenNone, // Esc mid-reply
		"claude-denied.txt":             ScreenNone, // Esc at the permission prompt
		"claude-spinner.txt":            ScreenBusy,
		"claude-permission.txt":         ScreenForm,
		"tuios-claude-code-working.txt": ScreenBusy,
		"tuios-codex-working.txt":       ScreenBusy,
		"tuios-codex-approval.txt":      ScreenForm,
	} {
		if got := ReadScreen(screenFixture(t, file)); got != want {
			t.Errorf("%s: got %d, want %d", file, got, want)
		}
	}
}

func TestState(t *testing.T) {
	for file, want := range map[string]model.AgentState{
		"claude-spinner.txt":        model.StateWorking,
		"claude-permission.txt":     model.StatePendingApproval,
		"tuios-claude-question.txt": model.StateAwaitingInput,
		"claude-idle.txt":           "",
		"claude-streaming.txt":      "",
		"codex-working.txt":         model.StateWorking,
		"tuios-codex-approval.txt":  model.StatePendingApproval,
		"codex-idle.txt":            "",
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
		{"node", "/usr/bin/node", ""},
		{"zsh", "/usr/bin/zsh", ""},
		{"", "", ""},
	} {
		if got := Identify(tc.comm, tc.exe); got != tc.want {
			t.Errorf("Identify(%q, %q) = %q, want %q", tc.comm, tc.exe, got, tc.want)
		}
	}
}
