package mobile

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"net/url"
	"slices"
	"strings"
	"testing"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// clipDesc writes pairs of kind and value as PhotoPick.describe does,
// with Android's Uri.encode, which escapes spaces as %20.
func clipDesc(kv ...string) string {
	var b strings.Builder
	for i := 0; i < len(kv); i += 2 {
		b.WriteString(kv[i] + " " + strings.ReplaceAll(url.QueryEscape(kv[i+1]), "+", "%20") + "\n")
	}
	return b.String()
}

func TestClipPictures(t *testing.T) {
	const gallery = "content://com.sec.android.gallery3d.provider/picker/item/42"
	const doc = "content://com.android.providers.downloads.documents/document/notes.txt"
	for _, c := range []struct {
		name, desc string
		uris       []string
		err        error
	}{
		{"item uri under an image MIME", clipDesc("mime", "image/jpeg", "item", "", "uri", gallery), []string{gallery}, nil},
		{"item uri under a text MIME", clipDesc("mime", "text/plain", "item", "", "uri", gallery, "text", "IMG_0042.jpg"), []string{gallery}, nil},
		{"chrome copy image", clipDesc("mime", "image/png", "item", "", "uri", "content://org.chromium.chrome.FileProvider/images/screenshot/1.png"), []string{"content://org.chromium.chrome.FileProvider/images/screenshot/1.png"}, nil},
		{"uri list", clipDesc("mime", "text/uri-list", "item", "", "text", "# copied\r\nfile:///sdcard/Pictures/a b.png\r\n"), []string{"file:///sdcard/Pictures/a b.png"}, nil},
		{"text that is a content uri", clipDesc("mime", "text/plain", "item", "", "text", " "+gallery+" "), []string{gallery}, nil},
		{"second item", clipDesc("mime", "text/plain", "item", "", "text", "a caption", "item", "", "uri", gallery), []string{gallery}, nil},
		{"html img", clipDesc("mime", "text/html", "item", "", "text", "", "html", `<p>x</p><IMG alt="a" src="content://media/external/images/media/7?a=1&amp;b=2">`), []string{"content://media/external/images/media/7?a=1&b=2"}, nil},
		{"html img with a web source", clipDesc("mime", "text/html", "item", "", "text", "a cat", "html", `<img src="https://example.com/cat.png">`), nil, errClipText},
		{"plain text", clipDesc("mime", "text/plain", "item", "", "text", "hello"), nil, errClipText},
		{"web link item", clipDesc("mime", "text/plain", "item", "", "uri", "https://example.com/cat.png", "text", "https://example.com/cat.png"), nil, errClipLink},
		{"empty item", clipDesc("mime", "text/plain", "item", "", "text", ""), nil, errClipEmpty},
		{"no clip", "", nil, errClipEmpty},
		// Java tries each in order and stops at the first that reads as a picture.
		{"document before a picture", clipDesc("mime", "text/plain", "mime", "image/png", "item", "", "uri", doc, "item", "", "uri", gallery), []string{doc, gallery}, nil},
		{"uri list of two", clipDesc("mime", "text/uri-list", "item", "", "text", "file:///gone.png\r\n"+gallery+"\r\n"), []string{"file:///gone.png", gallery}, nil},
		{"same uri as item and text", clipDesc("mime", "text/plain", "item", "", "uri", gallery, "text", gallery, "html", `<img src="`+doc+`">`), []string{gallery, doc}, nil},
	} {
		uris, err := clipPictures(c.desc)
		if !slices.Equal(uris, c.uris) || !errors.Is(err, c.err) {
			t.Errorf("%s: got %q, %v; want %q, %v", c.name, uris, err, c.uris, c.err)
		}
	}
	if got := clipShape(clipDesc("mime", "image/png", "item", "", "uri", "content://x/1", "text", "secret")); got != "mime image/png; item; uri content; text" {
		t.Errorf("shape %q", got)
	}
}

func TestCheckPicture(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 1, 1)))
	for _, c := range []struct {
		data []byte
		mime string
		ok   bool
	}{
		{b.Bytes(), "", true},
		{b.Bytes(), "application/octet-stream", true}, // bytes win over a wrong type
		{[]byte("%PDF-1.7"), "application/pdf", false},
		{[]byte("%PDF-1.7"), "image/png", true}, // newAttachment rejects it later
		{[]byte("%PDF-1.7"), "", true},
	} {
		if err := checkPicture(c.data, c.mime); (err == nil) != c.ok {
			t.Errorf("%q %s: %v", c.data[:4], c.mime, err)
		}
	}
}

// The field's long-press Paste attaches a picture, pastes text into the
// field, and says what an empty clipboard held.
func TestComposerPaste(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2, 1)))
	clip := func(data []byte, err error) chatEnv {
		return chatEnv{Attach: true, PickImage: func(paste bool, done func([]byte, error)) {
			if !paste {
				t.Fatal("Paste opened the picker")
			}
			done(data, err)
		}}
	}
	c := newChat("p")
	c.paste(C{}, clip(b.Bytes(), nil))
	if c.cmp.attachment == nil || c.cmp.loadingImage || c.cmp.pasteText {
		t.Fatalf("picture not attached: %+v", c.cmp)
	}
	c = newChat("p")
	c.paste(C{}, clip(nil, errClipText))
	if c.cmp.attachment != nil || !c.cmp.pasteText || c.cmp.notSent != "" {
		t.Fatalf("text paste: %+v", c.cmp)
	}
	c = newChat("p")
	c.paste(C{}, clip(nil, errClipEmpty))
	if c.cmp.notSent != "the clipboard is empty" {
		t.Fatalf("empty: %q", c.cmp.notSent)
	}
	c.cmp.pasteImage.Click()
	c.imageClicks(C{}, clip(nil, errClipText))
	if c.cmp.pasteText || c.cmp.notSent != errClipText.Error() {
		t.Fatalf("the clipboard button pasted text: %+v", c.cmp)
	}
}

// A model item's tap still picks the model now that the menu also serves Paste.
func TestMenuItemClick(t *testing.T) {
	c := newChat("p")
	c.cmp.menu = "model"
	c.cmp.item("model:sonnet").Click()
	gtx := layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(400, 800))}
	c.menus(gtx, theme.Dark(), chatEnv{Models: []string{"sonnet"}}, 600)
	if p := c.cmp.pending("model"); p == nil || p.value != "sonnet" || c.cmp.menu != "" {
		t.Fatalf("pick %+v, menu %q", p, c.cmp.menu)
	}
}

// A keyboard picture goes to the composer it started on. Send waits for it,
// and a chat opened before it is read does not get it.
func TestKeyboardInsert(t *testing.T) {
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 2, 1)))
	tab := remote.Tab{Item: remote.Item{Pane: "%1"}, ID: "t1", Attach: true, Send: true}
	a := &App{w: new(app.Window), ui: make(chan func(), 16), links: map[string]*link{}}
	a.fleet = phone.NewFleet([]phone.Machine{{ID: "m1", Name: "box"}, {ID: "m2", Name: "box2"}})
	for _, m := range a.fleet.Machines {
		a.fleet.Set(m.ID, []remote.Tab{tab})
		a.links[m.ID] = &link{m: m, conn: new(phone.Conn), hello: phone.Hello{Upload: true}}
	}
	gtx := layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(400, 800))}
	env := chatEnv{Tab: tab, Open: true, Online: true, Attach: true, Do: func(func(context.Context, chatAPI) error, func(error)) {
		t.Fatal("Send went before the keyboard picture was read")
	}}

	a.openChat(phone.NS("m1", "t1"))
	a.chat.cmp.ed.SetText("a caption")
	a.insertStart(1)
	a.chat.cmp.send.Click()
	a.chat.composer(gtx, theme.Dark(), env)
	a.insertImage(1, b.Bytes(), nil)
	if k := a.chat.cmp; k.attachment == nil || k.inserts != 0 || k.sending || k.notSent != "" {
		t.Fatalf("picture not attached: attachment %v, inserts %d, sending %v, %q", k.attachment != nil, k.inserts, k.sending, k.notSent)
	}

	a.openChat(phone.NS("m1", "t1"))
	a.insertStart(2)
	a.openChat(phone.NS("m2", "t1"))
	a.insertImage(2, b.Bytes(), nil)
	if k := a.chat.cmp; k.attachment != nil || k.inserts != 0 || k.notSent != "" {
		t.Fatalf("another chat got the picture: attachment %v, inserts %d, %q", k.attachment != nil, k.inserts, k.notSent)
	}
	if len(a.inserts) != 0 {
		t.Fatalf("inserts left: %v", a.inserts)
	}
}
