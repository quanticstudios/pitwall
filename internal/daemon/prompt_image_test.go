package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// imagePromptLine is a Claude Code user line with a pasted image of n
// bytes after its [Image #5] token.
func imagePromptLine(uuid string, n int) (string, string) {
	data := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("\x89PNG", n/4)))
	content, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "[Image #5] claude and codex are fine"},
		{"type": "image", "source": map[string]string{"type": "base64", "media_type": "image/png", "data": data}},
	})
	return `{"type":"user","uuid":"` + uuid + `","timestamp":"2026-11-18T10:00:01Z","message":{"role":"user","content":` + string(content) + `}}` + "\n", data
}

// TestPromptImageReachesWindow: a Claude pane's prompt with an image past
// session.ClipBytes comes cut in the subscribed window's base, and the
// window's read of the item whole brings the image's data.
func TestPromptImageReachesWindow(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	path := streamPane(t, d)
	line, data := imagePromptLine("p2", 64<<10)
	appendLine(t, path, line)
	sock := serveSocket(t, d)
	isChat := func(m any) bool { r, ok := m.(proto.ChatDeltas); return ok && r.Chat == "a" && !r.Push }

	c := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	c.send(proto.ChatSubscribe{Chat: "a"})
	base := c.waitFor("the base", isChat).(proto.ChatDeltas)
	var snap session.Snapshot
	if err := json.Unmarshal(base.Snapshot, &snap); err != nil {
		t.Fatal(err)
	}
	for _, b := range base.Deltas {
		var dl session.Delta
		json.Unmarshal(b, &dl)
		if n, err := session.Apply(&snap, base.Seq, dl); err == nil {
			snap = n
		}
	}
	var cut *session.Item
	for i := range snap.Content {
		if len(snap.Content[i].Attachments) > 0 {
			cut = &snap.Content[i]
		}
	}
	if cut == nil {
		t.Fatalf("no prompt with an image in the base: %+v", snap.Content)
	}
	t.Logf("base: cut=%v data=%d", cut.Cut, len(cut.Attachments[0].Data))
	if cut.Attachments[0].Data == data {
		return // came whole
	}
	if !cut.Cut {
		t.Fatalf("the image came without its data and the item is not marked cut: %+v", cut.Attachments[0])
	}
	c.send(proto.ChatRead{Chat: "a", Item: cut.ID})
	r := c.waitFor("the item", func(m any) bool { r, ok := m.(proto.ChatDeltas); return ok && r.Item != nil }).(proto.ChatDeltas)
	var it session.Item
	if err := json.Unmarshal(r.Item, &it); err != nil {
		t.Fatal(err)
	}
	if len(it.Attachments) != 1 || it.Attachments[0].Data != data {
		t.Fatalf("the item read whole lacks the image's data: %d bytes", len(it.Attachments[0].Data))
	}
	if it.UpdatedAt != cut.UpdatedAt {
		t.Fatalf("whole copy's UpdatedAt %q, cut copy's %q: the window never swaps it in", it.UpdatedAt, cut.UpdatedAt)
	}
}

// TestPromptImageAfterRetail: a prompt with an image the window paged in,
// or held in the tail before, still reads whole after the transcript grew
// enough for the pane chat to read its tail again.
func TestPromptImageAfterRetail(t *testing.T) {
	d, _, _ := remoteDaemon(t)
	path := streamPane(t, d)
	line, data := imagePromptLine("pimg", 64<<10)
	appendLine(t, path, line)
	read := func(r proto.ChatRead) proto.ChatDeltas {
		t.Helper()
		out, _ := d.chatRequest(context.Background(), r)
		m, ok := out.(proto.ChatDeltas)
		if !ok {
			t.Fatalf("%+v: %+v", r, out)
		}
		return m
	}
	whole := func(when string) {
		t.Helper()
		out, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: "a", Item: "pimg"})
		m, ok := out.(proto.ChatDeltas)
		if !ok {
			t.Fatalf("%s: item read: %+v", when, out)
		}
		var it session.Item
		json.Unmarshal(m.Item, &it)
		if len(it.Attachments) != 1 || it.Attachments[0].Data != data {
			t.Fatalf("%s: the item lacks its image", when)
		}
	}
	read(proto.ChatRead{Chat: "a"}) // the image prompt is in the tail
	whole("in the tail")

	var b strings.Builder
	for i := range 300 {
		b.WriteString(turnLines(i, "ok"))
	}
	appendLine(t, path, b.String())
	m := read(proto.ChatRead{Chat: "a"})
	for before := m.Older; before > 0; {
		p := read(proto.ChatRead{Chat: "a", Before: before})
		before = p.Older
	}
	whole("paged in")

	appendLine(t, path, strings.Repeat(turnLines(999, strings.Repeat("x", 1<<20)), 5)) // more than tailBytes unread: a retail
	read(proto.ChatRead{Chat: "a"})
	whole("after the tail was read again")
}
