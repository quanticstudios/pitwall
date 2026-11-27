package app

import (
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

type awayBackend struct {
	*FakeBackend
	snap session.Snapshot
}

func (b awayBackend) Chat(string) (session.Snapshot, bool) { return b.snap, true }

// TestAwayShots writes headless PNGs of a chat's Since you left card, in
// the light and dark themes and while its summary is on its way, to
// $PITWALL_AWAY_SHOTS; it skips without it.
func TestAwayShots(t *testing.T) {
	dir := os.Getenv("PITWALL_AWAY_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_AWAY_SHOTS is not set")
	}
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })

	left := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	at := func(m int) string { return left.Add(time.Duration(m) * time.Minute).Format(time.RFC3339Nano) }
	in := func(p string) json.RawMessage { b, _ := json.Marshal(map[string]string{"file_path": p}); return b }
	t1, zero, one := "t1", 0, 1
	prov := model.ProviderClaude
	snap := session.Snapshot{SessionID: "c1", Provider: prov, SessionState: session.StateReady, Content: []session.Item{
		{ID: "u1", Kind: session.KindUserPrompt, Text: "Make the login test stop racing the session cookie, then run the auth tests.", TurnID: &t1, Provider: &prov, CreatedAt: at(-2)},
		{ID: "e1", Kind: session.KindToolCall, TurnID: &t1, CreatedAt: at(3), Tool: &session.ToolCall{CallID: "e1", ToolName: "Edit", Status: "completed", Input: in("/repo/auth/login.go")}},
		{ID: "e2", Kind: session.KindToolCall, TurnID: &t1, CreatedAt: at(4), Tool: &session.ToolCall{CallID: "e2", ToolName: "Edit", Status: "completed", Input: in("/repo/auth/login_test.go")}},
		{ID: "c1", Kind: session.KindToolCall, TurnID: &t1, CreatedAt: at(6), Tool: &session.ToolCall{CallID: "c1", ToolName: "Bash", Command: "go build ./...", Status: "completed", ExitCode: &zero}},
		{ID: "c2", Kind: session.KindToolCall, TurnID: &t1, CreatedAt: at(8), Tool: &session.ToolCall{CallID: "c2", ToolName: "Bash", Command: "go test ./auth/...", Status: "failed", ExitCode: &one, Output: "--- FAIL: TestLoginRefresh (0.02s)\n    cookie expired 1s early"}},
		{ID: "a1", Kind: session.KindAssistantText, TurnID: &t1, CreatedAt: at(9), Provider: &prov, Text: "The race is gone, but `TestLoginRefresh` fails: the cookie expires a second early because the refresh uses the server clock. I can pad the expiry by the clock skew, or move the check to the client. Which do you want?"},
	}, Turns: []session.TurnSummary{{ID: t1, State: session.TurnCompleted}}}
	card := model.Away{Chat: "c1", Since: left, At: left.Add(2*time.Hour + 14*time.Minute), Replies: 1, Turns: 1,
		Edited: []string{"login.go", "login_test.go"}, EditedN: 2, Commands: 2, Failed: "go test ./auth/...", Tests: "failed", Item: "e1", Seq: 2,
		Status: "The cookie race is fixed; TestLoginRefresh still fails by one second.", Needs: "Pick: pad the expiry for clock skew, or check on the client."}

	size := image.Pt(1040, 720)
	// shoot lays u out a few frames on b's state and writes it as name.
	shoot := func(name string, u *ui) {
		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		var r input.Router
		var ops op.Ops
		now := time.Now()
		for range 5 {
			ops.Reset()
			now = now.Add(200 * time.Millisecond)
			u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: gl.Exact(size), Now: now})
			r.Frame(&ops)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		out := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(out); err != nil {
			t.Fatal(err)
		}
		win.Release()
		f, err := os.Create(filepath.Join(dir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	// chat shows the chat tab with snap and card a.
	chat := func(name string, th *theme.Theme, snap session.Snapshot, a model.Away) {
		b := awayBackend{NewFakeBackend(), snap}
		b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "Login cookie race", Path: t.TempDir(), Chat: "c1", Branch: "login-race"})
		s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
		s1.Order = append(s1.Order, "wc")
		b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: prov, State: session.StateReady, Title: "Login cookie race"}}
		b.st.Activities = []model.Activity{{PaneID: "c1", WorkspaceID: "wc", Provider: prov, State: model.StateAwaitingInput}}
		b.st.Away = map[string]model.Away{"c1": a}
		u := &ui{b: b, th: th, panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Font.UISize, u.cfg.Font.ChatSize = 13, 13
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, "wc", "")
		shoot(name, u)
	}
	// pane shows the fake's Codex tab "Fix invoice rounding" in its
	// terminal, pane n, with card a over it.
	pane := func(name string, th *theme.Theme, a model.Away) {
		b := NewFakeBackend()
		b.st.Away = map[string]model.Away{"n": a}
		u := &ui{b: b, th: th, panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Font.UISize, u.cfg.Font.ChatSize = 13, 13
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, "w10", "")
		shoot(name, u)
	}
	lc, _ := config.Builtin("aide-light")
	light, err := theme.New(lc, config.Font{})
	if err != nil {
		t.Fatal(err)
	}
	pane("away-pane-dark", theme.Dark(), model.Away{Chat: "n", Since: left, At: left.Add(47 * time.Minute), Replies: 2, Turns: 1,
		Edited: []string{"invoice.go", "invoice_test.go"}, EditedN: 2, Commands: 3, Tests: "passed", Item: "x", Seq: 3,
		Status: "Rounding now uses half to even; the invoice tests pass.", Needs: "Confirm half to even is what accounting wants."})
	asking := card
	asking.Chat, asking.Status, asking.Needs, asking.Asking = "n", "", "", true
	pane("away-pane-asking", light, asking)

	// The missed turn fits the log's view: only the marker.
	chat("away-chat-marker", light, snap, card)
	// A long one does not: the marker, and the card above the box.
	long := snap
	long.Content = append(slices.Clone(snap.Content), session.Item{ID: "a2", Kind: session.KindAssistantText, TurnID: &t1, CreatedAt: at(10), Provider: &prov,
		Text: strings.Repeat("Here is what I checked, step by step, so you can follow the reasoning without rerunning anything.\n\n", 14)})
	chat("away-chat-card", light, long, card)
	chat("away-chat-card-dark", theme.Dark(), long, card)
}
