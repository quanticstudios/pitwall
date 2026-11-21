// Package chatin is what the desktop and phone chat composers share: the
// draft's image tokens, image decoding, model names, permission modes,
// the context meter, and the desktop composer that draws them.
package chatin

import (
	"encoding/base64"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/session"
)

// Image is a picture in a draft: its number, the bytes it sends and its
// thumbnail.
type Image struct {
	N     int
	Data  []byte // PNG, JPEG or WebP, as Fit leaves it
	Mime  string
	Thumb paint.ImageOp
	side  int // Thumb's side in pixels, 0 before it is made
}

// Draft is what a composer holds besides its text: the images whose
// [Image #N] tokens stand in the text, and its context parts (Part).
// Numbers only go up within a draft, so removing #1 leaves #2 as it is.
// An image stays on the shelf when its token goes, as T3 Code's do; a
// part goes with its token.
type Draft struct {
	Images []Image
	Parts  []Part
	last   int    // the highest image number given out
	seq    [4]int // the highest part number given out, by kind
}

// Token is image n's token in the text.
func Token(n int) string { return "[Image #" + strconv.Itoa(n) + "]" }

// glue gives each token in s a no-break space, as the composer's editor
// holds it, so a token wraps as one word; unglue takes it back out. The
// space is one rune either way, so rune offsets hold in both.
func glue(s string) string   { return glued.Replace(s) }
func unglue(s string) string { return unglued.Replace(s) }

var (
	glued   = strings.NewReplacer("[Image #", "[Image\u00a0#", "[Quote #", "[Quote\u00a0#", "[Terminal #", "[Terminal\u00a0#", "[Pasted text #", "[Pasted\u00a0text\u00a0#")
	unglued = strings.NewReplacer("[Image\u00a0#", "[Image #", "[Quote\u00a0#", "[Quote #", "[Terminal\u00a0#", "[Terminal #", "[Pasted\u00a0text\u00a0#", "[Pasted text #")
)

// Add numbers img and returns the token to put in the text.
func (d *Draft) Add(img Image) string {
	d.last++
	img.N = d.last
	d.Images = append(d.Images, img)
	return Token(img.N)
}

// Sync drops the parts whose tokens text no longer holds. Images stay:
// only their thumbnail's x takes one out.
func (d *Draft) Sync(text string) {
	d.Parts = slices.DeleteFunc(d.Parts, func(p Part) bool { return !strings.Contains(text, p.Token()) })
}

// Mentioned reports whether text holds image n's token.
func Mentioned(text string, n int) bool { return strings.Contains(text, Token(n)) }

// Clear empties d; the next image is #1 again.
func (d *Draft) Clear() { *d = Draft{} }

// Ordered is d's images in the order their tokens stand in text, then
// those whose tokens went, in the order they came.
func (d *Draft) Ordered(text string) []Image {
	out := slices.Clone(d.Images)
	at := func(im Image) int {
		if i := strings.Index(text, Token(im.N)); i >= 0 {
			return i
		}
		return len(text)
	}
	slices.SortStableFunc(out, func(a, b Image) int { return at(a) - at(b) })
	return out
}

// TokenSpan is the rune range of image n's token in text, with the space
// after it, or -1, -1 when text has no such token.
func TokenSpan(text string, n int) (start, end int) { return tokenSpan(text, Token(n)) }

// tokenSpan is the rune range of tok in text, with the space after it,
// or -1, -1.
func tokenSpan(text, tok string) (start, end int) {
	i := strings.Index(text, tok)
	if i < 0 {
		return -1, -1
	}
	j := i + len(tok)
	if strings.HasPrefix(text[j:], " ") {
		j++
	}
	return utf8.RuneCountInString(text[:i]), utf8.RuneCountInString(text[:j])
}

// TokenBefore is the start, in runes, of the token of one of d's images
// or parts that ends at rune caret of text, or -1.
func (d *Draft) TokenBefore(text string, caret int) int {
	r := []rune(text)
	if caret < 0 || caret > len(r) {
		return -1
	}
	head := string(r[:caret])
	for _, im := range d.Images {
		if tok := Token(im.N); strings.HasSuffix(head, tok) {
			return caret - utf8.RuneCountInString(tok)
		}
	}
	for _, p := range d.Parts {
		if tok := p.Token(); strings.HasSuffix(head, tok) {
			return caret - utf8.RuneCountInString(tok)
		}
	}
	return -1
}

// Attachments are images as a ChatSend carries them.
func Attachments(images []Image) []session.Attachment {
	var out []session.Attachment
	for _, im := range images {
		ext := strings.TrimPrefix(im.Mime, "image/")
		out = append(out, session.Attachment{
			ID: "image-" + strconv.Itoa(im.N), Kind: "image", MimeType: im.Mime,
			Name: "image-" + strconv.Itoa(im.N) + "." + ext, SizeBytes: int64(len(im.Data)),
			Data: base64.StdEncoding.EncodeToString(im.Data),
		})
	}
	return out
}
