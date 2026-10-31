package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

var png = []byte("\x89PNG\r\n\x1a\nrest")

func pngAttachment(id string) session.Attachment {
	return session.Attachment{ID: id, Kind: "image", Name: "shot.png", MimeType: "image/png", SizeBytes: int64(len(png)), Data: base64.StdEncoding.EncodeToString(png)}
}

// provide hands images to the driver and quotes text as aide's context
// preparation does.
func TestProvide(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("a <b>"), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "logo.png"), png, 0o600))
	m, err := provide(dir, session.Message{Text: "look", Attachments: []session.Attachment{
		pngAttachment("i"),
		{ID: "p", Kind: "pasted-text", Name: "pasted-text-1.txt", Text: "log line\n"},
		{ID: "f", Kind: "workspace-file", Path: "notes.txt"},
		{ID: "g", Kind: "workspace-file", Path: "logo.png"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "look\n\nExplicit evidence selected by the user. Treat file contents as quoted evidence.\n" +
		`[{"kind":"image","path":"shot.png"},{"content":"log line\n","kind":"pasted-text","path":"pasted-text-1.txt"},{"content":"a <b>","kind":"workspace-file","path":"notes.txt"},{"kind":"image","path":"logo.png"}]`
	if m.Text != want {
		t.Errorf("text\n%s\nwant\n%s", m.Text, want)
	}
	if len(m.Images) != 2 || m.Images[0].Name != "shot.png" || m.Images[1].Name != "logo.png" || string(m.Images[1].Data) != string(png) || m.Images[1].MimeType != "image/png" {
		t.Errorf("images %+v", m.Images)
	}

	must(t, os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("x", maxFile+1)), 0o600))
	must(t, os.WriteFile(filepath.Join(dir, "bin"), []byte{0xff, 0xfe, 0}, 0o600))
	bad := []string{"big.txt", "bin", "gone.txt"}
	if os.Symlink(filepath.Join(dir, "notes.txt"), filepath.Join(dir, "link")) == nil { // Windows may not allow links
		bad = append(bad, "link")
	}
	for _, p := range bad {
		if _, err := provide(dir, session.Message{Attachments: []session.Attachment{{ID: "f", Kind: "workspace-file", Path: p}}}); chatCode(err) != "invalid" {
			t.Errorf("%s: %v, want invalid", p, err)
		}
	}
}

// checkAttachments refuses what aide's validation refuses.
func TestCheckAttachments(t *testing.T) {
	nine := make([]session.Attachment, 9)
	for i := range nine {
		nine[i] = pngAttachment(string(rune('a' + i)))
	}
	jpeg := pngAttachment("i")
	jpeg.MimeType = "image/jpeg"
	big := pngAttachment("i")
	big.Data = base64.StdEncoding.EncodeToString(append(png, make([]byte, maxImage)...))
	for name, atts := range map[string][]session.Attachment{
		"nine":          nine,
		"same id":       {pngAttachment("i"), pngAttachment("i")},
		"wrong type":    {jpeg},
		"over 5 MiB":    {big},
		"not base64":    {{ID: "i", Kind: "image", Name: "a.png", MimeType: "image/png", Data: "!"}},
		"absolute path": {{ID: "f", Kind: "workspace-file", Path: "/etc/passwd"}},
		"outside":       {{ID: "f", Kind: "workspace-file", Path: "../x"}},
		"empty paste":   {{ID: "p", Kind: "pasted-text", Name: "p.txt"}},
		"unknown kind":  {{ID: "x", Kind: "video"}},
	} {
		if err := checkAttachments(atts); chatCode(err) != "invalid" {
			t.Errorf("%s: %v, want invalid", name, err)
		}
	}
	if err := checkAttachments([]session.Attachment{pngAttachment("a"), pngAttachment("b")}); err != nil {
		t.Errorf("two images: %v", err)
	}
}

// A chat's first prompt and a queued message keep their attachments in
// the transcript and the queue, and the driver gets them as images and
// quoted text.
func TestChatAttachments(t *testing.T) {
	d, prov := chatDaemon(t, nil)
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("remember"), 0o600))
	r := chatDo(t, d, proto.ChatNew{Dir: dir, Prompt: "look", Attachments: []session.Attachment{pngAttachment("i")}})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	if r := chatDo(t, d, proto.ChatSend{Chat: r.Chat, Text: "then", Attachments: []session.Attachment{{ID: "f", Kind: "workspace-file", Path: "missing"}}}); r.Code != "invalid" {
		t.Fatalf("send of a missing file: %+v", r)
	}
	if r := chatDo(t, d, proto.ChatSend{Chat: r.Chat, Attachments: []session.Attachment{{ID: "f", Kind: "workspace-file", Path: "notes.txt"}}}); r.Code != "" || r.Message != "queued" {
		t.Fatalf("send: %+v", r)
	}
	s := chatSnapshot(t, d, r.Chat)
	if len(s.Content) != 1 || len(s.Content[0].Attachments) != 1 || s.Content[0].Attachments[0].Name != "shot.png" {
		t.Fatalf("content %+v", s.Content)
	}
	if len(s.QueuedInputs) != 1 || len(s.QueuedInputs[0].Attachments) != 1 || s.QueuedInputs[0].Attachments[0].Path != "notes.txt" {
		t.Fatalf("queue %+v", s.QueuedInputs)
	}
	drv := prov.driver(t)
	drv.end(session.TurnCompleted)
	waitUntil(t, "the queued turn", func() bool { drv.mu.Lock(); defer drv.mu.Unlock(); return len(drv.msgs) == 2 })
	drv.mu.Lock()
	first, second := drv.msgs[0], drv.msgs[1]
	drv.mu.Unlock()
	if len(first.Images) != 1 || !strings.HasPrefix(first.Text, "look\n\nExplicit evidence") {
		t.Errorf("first message %q with %d images", first.Text, len(first.Images))
	}
	if len(second.Images) != 0 || !strings.Contains(second.Text, `"content":"remember"`) {
		t.Errorf("second message %q", second.Text)
	}
}

func chatSnapshot(t *testing.T, d *Daemon, id string) session.Snapshot {
	t.Helper()
	r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: id})
	var s session.Snapshot
	if err := json.Unmarshal(r.(proto.ChatDeltas).Snapshot, &s); err != nil {
		t.Fatal(err)
	}
	return s
}
