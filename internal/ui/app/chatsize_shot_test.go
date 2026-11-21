package app

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// shotBackend serves one chat, c1, as sizeChat.
type shotBackend struct{ *FakeBackend }

func (shotBackend) Chat(string) (session.Snapshot, bool) { return sizeChat(), true }

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

// TestChatSizeShots writes PNGs of a chat tab at chat sizes 9, 13, 18
// and 28 to $PITWALL_CHAT_SHOTS, for a look at them; it skips without it.
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
	for _, cs := range []float64{9, 13, 18, 28} {
		b := shotBackend{NewFakeBackend()}
		b.st.Workspaces = append(b.st.Workspaces, model.Workspace{ID: "wc", SessionID: "s1", Name: "chat", Label: "Chat text size", Path: t.TempDir(), Chat: "c1", Branch: "chat-font-size"})
		s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
		s1.Order = append(s1.Order, "wc")
		b.st.Chats = []model.Chat{{ID: "c1", WorkspaceID: "wc", Provider: model.ProviderClaude, State: session.StateRunning, Title: "Chat text size"}}
		u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Font.UISize, u.cfg.Font.ChatSize = 13, cs
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, "wc", "")
		cmp := u.composer(&st, "c1")
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
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("chat-%g.png", cs)))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
