package panel

import (
	"bytes"
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"sync"

	"gioui.org/op/paint"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	"github.com/quanticstudios/pitwall/internal/session"
)

// thumbHeight is the tallest a prompt's thumbnail is decoded, in pixels:
// twice the 96 dp it draws at, for a HiDPI screen.
const thumbHeight = 192

// thumb is an attachment's image, decoded off the window's goroutine.
type thumb struct {
	ready bool
	img   paint.ImageOp
	size  image.Point // 0 when the data did not decode
}

// thumbs caches prompt images by item and attachment id.
type thumbs struct {
	mu sync.Mutex
	m  map[string]*thumb
}

// get is a's thumbnail, started on first ask; done runs once it is
// ready, from another goroutine.
func (c *thumbs) get(key string, a *session.Attachment, done func()) *thumb {
	return c.getSized(key, a, thumbHeight, done)
}

// getSized is get for an image decoded at most h pixels tall.
func (c *thumbs) getSized(key string, a *session.Attachment, h int, done func()) *thumb {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*thumb{}
	}
	if t := c.m[key]; t != nil {
		return t
	}
	t := &thumb{}
	c.m[key] = t
	data := a.Data
	go func() {
		img, size := decodeThumb(data, h)
		c.mu.Lock()
		t.img, t.size, t.ready = img, size, true
		c.mu.Unlock()
		if done != nil {
			done()
		}
	}()
	return t
}

// snapshot is t's fields read under the lock.
func (c *thumbs) snapshot(t *thumb) thumb {
	c.mu.Lock()
	defer c.mu.Unlock()
	return *t
}

// decodeThumb decodes base64 image data and scales it to at most height
// pixels tall.
func decodeThumb(data string, height int) (paint.ImageOp, image.Point) {
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return paint.ImageOp{}, image.Point{}
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil || src.Bounds().Empty() {
		return paint.ImageOp{}, image.Point{}
	}
	sb := src.Bounds()
	h := min(height, sb.Dy())
	w := max(1, sb.Dx()*h/sb.Dy())
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, xdraw.Src, nil)
	return paint.NewImageOp(dst), dst.Bounds().Size()
}
