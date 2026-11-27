package panel

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// rollupSnap is a finished turn of 20 steps: thoughts, reads, searches,
// commands, two edits to two files and one failed test run, then the
// reply. It took 2m 14s.
func rollupSnap() *session.Snapshot {
	t0 := time.Date(2026, 11, 27, 14, 0, 0, 0, time.UTC)
	at := func(s int) string { return t0.Add(time.Duration(s) * time.Second).Format(time.RFC3339Nano) }
	turn := "t1"
	var items []session.Item
	add := func(it session.Item, s int) {
		it.ID, it.TurnID, it.CreatedAt, it.UpdatedAt = "i"+strconv.Itoa(len(items)), &turn, at(s), at(s)
		items = append(items, it)
	}
	tool := func(name, status string, in map[string]any) session.Item {
		b, _ := json.Marshal(in)
		return session.Item{Kind: session.KindToolCall, Tool: &session.ToolCall{ToolName: name, Status: status, Input: b, Output: "ok"}}
	}
	think := func(t string) session.Item { return session.Item{Kind: session.KindReasoningText, Text: t} }
	add(session.Item{Kind: session.KindUserPrompt, Text: "Make the login test stop racing the session cookie, then run the auth tests."}, 0)
	steps := []session.Item{
		think("The test reads the cookie before the handler writes it."),
		tool("Grep", "completed", map[string]any{"pattern": "sessionCookie"}),
		tool("Read", "completed", map[string]any{"file_path": "/repo/auth/login.go"}),
		tool("Read", "completed", map[string]any{"file_path": "/repo/auth/login_test.go"}),
		think("The refresh path sets the cookie in a goroutine."),
		tool("Read", "completed", map[string]any{"file_path": "/repo/auth/session.go"}),
		tool("Bash", "completed", map[string]any{"command": "git log -3 --oneline auth/"}),
		tool("Edit", "completed", map[string]any{"file_path": "/repo/auth/login.go", "old_string": "go setCookie(w, s)\nreturn\n", "new_string": "setCookie(w, s)\nreturn nil\n"}),
		think("Now wait for the write in the test."),
		tool("Edit", "completed", map[string]any{"file_path": "/repo/auth/login_test.go", "old_string": "time.Sleep(10 * time.Millisecond)\n", "new_string": "<-done\nc := rec.Result().Cookies()\nif len(c) == 0 {\n\tt.Fatal(\"no cookie\")\n}\n"}),
		tool("Bash", "completed", map[string]any{"command": "go vet ./auth/"}),
		tool("Bash", "failed", map[string]any{"command": "go test ./auth/..."}),
		think("TestLoginRefresh fails by one second: the server clock."),
		tool("Grep", "completed", map[string]any{"pattern": "time.Now"}),
		tool("Read", "completed", map[string]any{"file_path": "/repo/auth/clock.go"}),
		tool("Bash", "completed", map[string]any{"command": "go test -run TestLogin ./auth/"}),
		tool("Glob", "completed", map[string]any{"pattern": "auth/**/*_test.go"}),
		tool("Read", "completed", map[string]any{"file_path": "/repo/auth/refresh_test.go"}),
		think("The race is fixed; the clock skew is a separate choice."),
		tool("Bash", "completed", map[string]any{"command": "git diff --stat"}),
	}
	for i, it := range steps {
		add(it, 3+i*6)
	}
	add(session.Item{Kind: session.KindAssistantText, Text: "The race is gone. `TestLoginRefresh` still fails by one second: the refresh uses the server clock. I can pad the expiry by the skew, or move the check to the client. Which do you want?"}, 134)
	end := at(134)
	return &session.Snapshot{SessionID: "rollup", Provider: model.ProviderClaude, SessionState: session.StateReady, Content: items,
		Turns: []session.TurnSummary{{ID: turn, State: "completed", StartedAt: at(0), CompletedAt: &end}}}
}

// TestRollupShots writes a 20-step turn in both layouts and each tool
// call mode to $PITWALL_RENDER_DIR, for a look; it skips without it.
func TestRollupShots(t *testing.T) {
	dir := os.Getenv("PITWALL_RENDER_DIR")
	if dir == "" {
		t.Skip("PITWALL_RENDER_DIR is not set")
	}
	for _, l := range []string{LayoutLedger, LayoutColumn} {
		for _, m := range []string{ToolsAll, ToolsFailed, ToolsNone, ToolsZen} {
			img, _ := ledgerFrameMode(t, theme.Dark(), rollupSnap(), l, m, 1440, 1100)
			f, err := os.Create(filepath.Join(dir, "rollup-"+l+"-"+m+".png"))
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, img); err != nil {
				t.Fatal(err)
			}
			f.Close()
		}
	}
}
