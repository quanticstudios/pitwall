package app

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// A reply from the sheet goes to the daemon once the agent waits, images and
// text. To a busy agent it waits for the turn to end.
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
	if len(b.sent) != 0 || len(b.delivered) != 1 {
		t.Fatalf("typed %q, delivered %+v", b.sent, b.delivered)
	}
	if got := b.delivered[0]; got.Pane != "p" || got.Text != "use a.go\nthen b.go" || len(got.Images) != 1 || got.Images[0] != "/s/paste/image-1.png" {
		t.Errorf("delivered %+v", got)
	}

	// An image alone goes with no text.
	u.sidebarEvent(&st, sidebar.Reply{WorkspaceID: "w", PaneID: "p", Images: []string{"/s/b.png"}})
	waitSends(t, u, &st)
	if len(b.delivered) != 2 || b.delivered[1].Text != "" || b.delivered[1].Images[0] != "/s/b.png" || u.notice != "Sent to claude" {
		t.Fatalf("image only: delivered %+v, notice %q", b.delivered[1:], u.notice)
	}

	// Having typed in the pane, the user may have a draft that holds it.
	st.Drafting = map[string]bool{"p": true}
	u.sidebarEvent(&st, sidebar.Reply{WorkspaceID: "w", PaneID: "p", Text: "more"})
	waitSends(t, u, &st)
	if u.notice != "Goes to claude once its input box is empty" {
		t.Errorf("notice %q", u.notice)
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
