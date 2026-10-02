package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// The claude-*.txt fixtures are pitwall's own emulator reading Claude Code
// 2.1.287 in a 100x30 PTY; the tuios-*.txt ones are tuios's screen fixtures.
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
		b, err := os.ReadFile("testdata/" + file)
		if err != nil {
			t.Fatal(err)
		}
		e := vt.New(100, 30, nil)
		e.Write([]byte(strings.ReplaceAll(strings.TrimRight(string(b), "\n"), "\n", "\r\n")))
		if got := ReadScreen(e.Snapshot()); got != want {
			t.Errorf("%s: got %d, want %d", file, got, want)
		}
	}
}
