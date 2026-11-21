package chatin

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// TestRewindKeepsDraft: a rewind that went through puts its prompt and its
// image under the draft, which stays, the image renumbered past the
// draft's own; its note shows quietly until the text changes. A rewind of
// another turn, and a refused one, leave the box alone.
func TestRewindKeepsDraft(t *testing.T) {
	var c Composer
	c.AddImage(pngOf(4, 4))
	c.ed.Insert("my draft")
	draft := c.Text()
	img := session.Attachment{ID: "image-1", Kind: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(pngOf(4, 4))}
	c.AwaitRewind("t1", "fix the bug", []session.Attachment{img})

	c.Result(Result{Req: proto.ChatRewind{Chat: "c", Turn: "t2"}}, time.Now())
	if c.Text() != draft || c.rewind == nil {
		t.Fatalf("an answer for another turn changed the box: %q", c.Text())
	}
	c.Result(Result{Req: proto.ChatRewind{Chat: "c", Turn: "t1"}, Note: "Rewound; saved in commit abc"}, time.Now())
	want := draft + "\nfix the bug " + Token(2)
	if c.Text() != want || len(c.Images()) != 2 || c.rewind != nil {
		t.Fatalf("box %q with %d images, want %q with 2", c.Text(), len(c.Images()), want)
	}
	if c.note != "Rewound; saved in commit abc" || !c.noteOK {
		t.Errorf("note %q ok %v", c.note, c.noteOK)
	}

	var d Composer
	d.SetText("keep")
	d.AwaitRewind("t1", "gone", nil)
	d.Result(Result{Req: proto.ChatRewind{Chat: "c", Turn: "t1"}, Message: "files changed"}, time.Now())
	if d.Text() != "keep" || d.note != "files changed" || d.noteOK {
		t.Errorf("a refused rewind: box %q note %q ok %v", d.Text(), d.note, d.noteOK)
	}

	var e Composer
	e.AwaitRewind("t1", "alone", nil)
	e.Result(Result{Req: proto.ChatRewind{Chat: "c", Turn: "t1"}}, time.Now())
	if e.Text() != "alone" || e.note != "" {
		t.Errorf("an empty box: %q, note %q", e.Text(), e.note)
	}
}
