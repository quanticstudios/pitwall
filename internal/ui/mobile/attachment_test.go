package mobile

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"testing"

	"github.com/quanticstudios/pitwall/internal/remote"
)

type attachmentAPI struct {
	fakeAPI
	uploads, sends int
	text, image    string
}

func (a *attachmentAPI) Upload(context.Context, string, []byte) (string, error) {
	a.uploads++
	return "image-test.png", nil
}
func (a *attachmentAPI) SendImage(_ context.Context, _ string, text, image string) (uint64, error) {
	a.sends++
	a.text, a.image = text, image
	return 1, a.err
}
func TestComposerAttachment(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2, 1)))
	a, err := newAttachment(b.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newAttachment([]byte("text")); err == nil {
		t.Fatal("accepted text")
	}
	c := newChat("p")
	if d := c.attachments(C{}, nil, chatEnv{}); d.Size != (image.Point{}) {
		t.Fatal("old daemon shows attachment controls")
	}
	c.cmp.attachment = a
	c.cmp.removeImage.Click()
	c.imageClicks(C{}, chatEnv{})
	if c.cmp.attachment != nil {
		t.Fatal("remove kept the attachment")
	}
	c.cmp.attachment = a
	c.cmp.notSent = "old failure"
	c.cmp.attach.Click()
	c.imageClicks(C{}, chatEnv{Attach: true, PickImage: func(_ bool, done func([]byte, error)) { done(b.Bytes(), nil) }})
	if c.cmp.attachment == nil || c.cmp.notSent != "" {
		t.Fatal("successful picture did not clear the error")
	}
	a = c.cmp.attachment
	c.cmp.attach.Click()
	c.imageClicks(C{}, chatEnv{Attach: true, PickImage: func(_ bool, done func([]byte, error)) { done([]byte("invalid"), nil) }})
	if c.cmp.attachment != a {
		t.Fatal("invalid replacement lost the selected picture")
	}
	api := &attachmentAPI{}
	e := chatEnv{Online: true, Tab: remote.Tab{Send: true}}
	e.Do = func(call func(context.Context, chatAPI) error, done func(error)) {
		done(call(context.Background(), api))
	}
	api.err = errors.New("offline")
	c.sendDraft(e)
	if c.cmp.attachment != a || c.cmp.sending {
		t.Fatal("lost failed attachment")
	}
	api.err = nil
	c.sendDraft(e)
	if c.cmp.attachment != nil || api.uploads != 1 || api.sends != 2 || api.image != "image-test.png" {
		t.Fatalf("attachment %+v calls %+v", c.cmp.attachment, api)
	}
	if len(c.data.Pending) != 1 || c.data.Pending[0].Message() != "Picture" {
		t.Fatalf("image-only pending %+v", c.data.Pending)
	}
}
