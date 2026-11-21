package app

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// shotBackend serves every chat as sizeChat, from prov.
type shotBackend struct {
	*FakeBackend
	prov model.Provider
}

func (b shotBackend) Chat(string) (session.Snapshot, bool) {
	s := sizeChat()
	s.Provider = b.prov
	return s, true
}

// sizeChat is a finished turn with tool rows, markdown, a table and code,
// then a running one that waits on an edit's approval card with its diff,
// with a message queued.
func sizeChat() session.Snapshot {
	in := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	at := func(s int) string { return time.Date(2026, 11, 21, 10, 0, s, 0, time.UTC).Format(time.RFC3339Nano) }
	turn, turn2, zero, one := "t1", "t2", 0, 1
	reply := "## Chat text size\n\nThe chat now has **its own size**, `[font] chat_size`, and code follows it.\n\n" +
		"| Key | In a chat |\n| --- | --- |\n| Ctrl+= | One point bigger |\n| Ctrl+- | One point smaller |\n| Ctrl+0 | Follow the interface size |\n\n" +
		"```go\nfunc (u *ui) chatSize() float64 {\n\treturn cmp.Or(u.cfg.Font.ChatSize, u.cfg.Font.UISize)\n}\n```\n\n" +
		"- Spacing scales with the text, so nothing crowds at 9 or 28.\n- The sidebar and dialogs keep `ui_size`."
	calls := []session.ToolCall{
		{CallID: "ca", ToolName: "Read", Status: "completed", Input: in(map[string]any{"file_path": "/repo/internal/ui/app/chat.go"}), Output: "package app\n"},
		{CallID: "cb", ToolName: "Edit", Status: "completed", Input: in(map[string]any{"file_path": "/repo/internal/ui/app/chat.go", "old_string": "a", "new_string": "b"}),
			Diff: "@@ -140,6 +140,7 @@\n \t\tenv.Copy = func(s string) { putClipboard(gtx, s) }\n+\t\tgtx.Metric = u.chatMetric(gtx.Metric)\n \t\tgl.Flex{Axis: gl.Vertical}.Layout(gtx,\n"},
		{CallID: "cc", ToolName: "Bash", Status: "completed", Input: in(map[string]any{"command": "go test ./internal/ui/app/", "description": "Run the app tests"}), ExitCode: &zero,
			Output: "ok  \tgithub.com/quanticstudios/pitwall/internal/ui/app\t14.27s", StartedAt: at(10), EndedAt: at(24)},
		{CallID: "cd", ToolName: "Bash", Status: "failed", Input: in(map[string]any{"command": "go vet ./..."}), ExitCode: &one,
			Output: "# github.com/quanticstudios/pitwall/internal/ui/app\n./chatsize.go:12:2: \"strconv\" imported and not used", StartedAt: at(25), EndedAt: at(27)},
	}
	diff := "@@ -140,6 +140,7 @@\n \t\tenv.Copy = func(s string) { putClipboard(gtx, s) }\n+\t\tgtx.Metric = u.chatMetric(gtx.Metric)\n \t\tgl.Flex{Axis: gl.Vertical}.Layout(gtx,\n"
	s := session.Snapshot{SessionID: "c1", Provider: model.ProviderClaude, SessionState: session.StateWaiting, Running: true,
		QueuedInputs:     []session.QueuedInput{{ID: "q1", Text: "Then render it at 9, 13, 18 and 28 and look at every shot", CreatedAt: at(40)}},
		PendingApprovals: []session.Approval{{ID: "a1", RequestType: session.RequestFileChange, Request: &session.ToolRequest{ToolName: "Edit", FilePath: "/repo/internal/ui/app/chat.go", Diff: diff}}}}
	s.Content = append(s.Content,
		session.Item{ID: "p1", Kind: session.KindUserPrompt, Text: "We should be able to change font sizes in the GUI view of chat as well.", TurnID: &turn, CreatedAt: at(0)})
	for i := range calls {
		c := calls[i]
		s.Content = append(s.Content, session.Item{ID: c.CallID, Kind: session.KindToolCall, Tool: &c, TurnID: &turn, CreatedAt: at(6 + i), UpdatedAt: at(6 + i)})
	}
	s.Content = append(s.Content,
		session.Item{ID: "r1", Kind: session.KindAssistantText, Text: reply, TurnID: &turn, CreatedAt: at(30), UpdatedAt: at(30)},
		session.Item{ID: "p2", Kind: session.KindUserPrompt, Text: "Wire it into the chat body.", TurnID: &turn2, CreatedAt: at(35)},
		session.Item{ID: "ce", Kind: session.KindToolCall, TurnID: &turn2, CreatedAt: at(36), UpdatedAt: at(36),
			Tool: &session.ToolCall{CallID: "ce", ToolName: "Edit", Status: "pending", Input: in(map[string]any{"file_path": "/repo/internal/ui/app/chat.go"}), Diff: diff}})
	return s
}

// TestChatSizeShots writes PNGs of a Claude chat tab at chat sizes 9, 13,
// 18 and 28, a Codex pane chat at 18 and an Antigravity chat tab at 24 to
// $PITWALL_CHAT_SHOTS, for a look at them; it skips without it.
func TestChatSizeShots(t *testing.T) {
	dir := os.Getenv("PITWALL_CHAT_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_CHAT_SHOTS is not set")
	}
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	var pic bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 320, 200))
	for y := range 200 {
		for x := range 320 {
			img.Set(x, y, color.RGBA{uint8(40 + x/3), uint8(60 + y/2), 160, 255})
		}
	}
	png.Encode(&pic, img)
	const scale = 1
	size := image.Pt(1100, 1600)
	for _, c := range []struct {
		name string
		cs   float64
		prov model.Provider
		pane bool // the chat of pane h in a tab of panes, else a New task chat tab
	}{
		{"chat-9", 9, model.ProviderClaude, false}, {"chat-13", 13, model.ProviderClaude, false},
		{"chat-18", 18, model.ProviderClaude, false}, {"chat-28", 28, model.ProviderClaude, false},
		{"pane-codex-18", 18, model.ProviderCodex, true}, {"tab-antigravity-24", 24, model.ProviderAntigravity, false},
	} {
		cs := c.cs
		b := shotBackend{NewFakeBackend(), c.prov}
		b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "Chat text size", Path: t.TempDir(), Chat: "c1", Branch: "chat-font-size"})
		s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
		s1.Order = append(s1.Order, "wc")
		b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: c.prov, State: session.StateRunning, Title: "Chat text size"}}
		id, ws := "c1", "wc"
		if c.pane {
			i := slices.IndexFunc(b.st.Panes, func(p model.Pane) bool { return p.ID == "h" })
			b.st.Panes[i].Provider, b.st.Panes[i].Transcript = c.prov, "/tmp/s.jsonl"
			id, ws = "h", "w6"
		}
		u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Font.UISize, u.cfg.Font.ChatSize = 13, cs
		u.cfg.Chat.Claude, u.cfg.Chat.Codex, u.cfg.Chat.Antigravity = true, true, true
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, ws, "")
		if c.pane {
			u.nav.setFocus("h")
		}
		cmp := u.composer(&st, id)
		cmp.SetText("Also scale the composer's chips and the queue row")
		cmp.AddImage(pic.Bytes())
		u.chatSizeAt = time.Now()

		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		var r input.Router
		var ops op.Ops
		for range 4 {
			ops.Reset()
			u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale},
				Constraints: gl.Exact(size), Now: time.Now()})
			r.Frame(&ops)
			time.Sleep(150 * time.Millisecond)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		out := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(out); err != nil {
			t.Fatal(err)
		}
		win.Release()
		f, err := os.Create(filepath.Join(dir, c.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
