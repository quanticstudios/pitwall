package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// A reply from the sheet goes in as typed once the agent waits: each
// image's path as its own paste, then the text as one bracketed paste,
// then Enter. To a busy agent it waits for the turn to end.
func TestReplySend(t *testing.T) {
	b := newAgentBackend(model.StateWorking)
	u := &ui{b: b}
	st := b.State()
	u.sidebarEvent(&st, sidebar.Reply{WorkspaceID: "w", Text: "use a.go\nthen b.go", Images: []string{"/s/paste/image-1.png"}})
	waitSends(t, u, &st)
	if len(b.sent) != 0 || len(u.review.queue) != 1 || u.notice != "Queued until claude waits for input" {
		t.Fatalf("busy agent: sent %q, queue %d, notice %q", b.sent, len(u.review.queue), u.notice)
	}

	b.st.Activities[0].State = model.StateAwaitingInput
	st = b.State()
	now := time.Now()
	u.deliver(&st, now)
	if len(b.sent) != 2 {
		t.Fatalf("sent %q, want the image and the text", b.sent)
	}
	if got := string(b.sent[0].Data); got != "\x1b[200~'/s/paste/image-1.png'\x1b[201~" {
		t.Errorf("image paste %q", got)
	}
	if got := string(b.sent[1].Data); got != "\x1b[200~use a.go\nthen b.go\x1b[201~" {
		t.Errorf("text paste %q", got)
	}
	u.deliver(&st, now.Add(replyDelay))
	if len(b.sent) != 3 || string(b.sent[2].Data) != "\r" {
		t.Fatalf("no Enter after the paste: %q", b.sent)
	}

	// An image alone pastes no empty text.
	u.sidebarEvent(&st, sidebar.Reply{WorkspaceID: "w", PaneID: "p", Images: []string{"/s/b.png"}})
	waitSends(t, u, &st)
	if len(b.sent) != 4 || !strings.Contains(string(b.sent[3].Data), "/s/b.png") || u.notice != "Sent to claude" {
		t.Fatalf("image only: sent %q, notice %q", b.sent[3:], u.notice)
	}

	// A tab with no agent says so.
	u.sidebarEvent(&st, sidebar.Reply{WorkspaceID: "gone", Text: "hi"})
	waitSends(t, u, &st)
	if u.sends.err != "Reply: the tab has no agent to send to" {
		t.Errorf("no agent: %q", u.sends.err)
	}
}

// Attaching an image reads this machine's clipboard, sends the image to
// the daemon for the tab's agent pane and hands the path it answers with
// to the sheet; a clipboard error ends it with nothing sent.
func TestReplyAttachImage(t *testing.T) {
	old := readClipImage
	t.Cleanup(func() { readClipImage = old })
	png := []byte("\x89PNG\r\n\x1a\nimage")
	var clipErr error
	readClipImage = func(context.Context) ([]byte, error) { return png, clipErr }
	b := &pasteBackend{agentBackend: newAgentBackend(model.StateAwaitingInput), level: proto.Level}
	u := &ui{b: b}
	st := b.State()
	// tick runs frames until the reply's image is done or sent.
	tick := func(sent int) {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); u.replyImg != nil && len(b.images) < sent; time.Sleep(time.Millisecond) {
			if time.Now().After(end) {
				t.Fatal("the clipboard read never ended")
			}
			u.tickReplyImage()
		}
	}

	u.sidebarEvent(&st, sidebar.AttachImage{WorkspaceID: "w"})
	tick(1)
	if len(b.images) != 1 || b.images[0].Pane != "p" || !bytes.Equal(b.images[0].Data, png) || u.replyImg == nil || !u.replyImg.wait {
		t.Fatalf("sent %d images, waiting %v", len(b.images), u.replyImg)
	}
	u.sidebarEvent(&st, sidebar.AttachImage{WorkspaceID: "w"}) // one at a time
	b.reply = &proto.PasteImageResult{Pane: "p", Path: "/s/paste/image-1.png"}
	u.tickReplyImage()
	if u.replyImg != nil || len(b.images) != 1 {
		t.Fatalf("after the answer: %v, %d images", u.replyImg, len(b.images))
	}

	clipErr = errors.New("the clipboard holds no text or image")
	u.sidebarEvent(&st, sidebar.AttachImage{WorkspaceID: "w", PaneID: "p"})
	tick(2)
	if u.replyImg != nil || len(b.images) != 1 {
		t.Fatalf("a failed read sent an image: %d", len(b.images))
	}

	// An old daemon takes no image.
	b.level = proto.Since(proto.PasteImage{}) - 1
	u.sidebarEvent(&st, sidebar.AttachImage{WorkspaceID: "w"})
	if u.replyImg != nil {
		t.Fatal("an old daemon was sent an image")
	}
}
