package vt

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

// testPNG is a w x h PNG, red with a blue top left pixel.
func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	m := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(m.Pix); i += 4 {
		copy(m.Pix[i:], []byte{0xff, 0, 0, 0xff})
	}
	m.SetNRGBA(0, 0, color.NRGBA{0, 0, 0xff, 0xff})
	var b bytes.Buffer
	if err := png.Encode(&b, m); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// apc sends data as kitty's icat does: base64 in chunks of size, the
// first with ctl and m=1, the rest with m alone.
func apc(ctl string, data []byte, size int) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for first := true; first || enc != ""; first = false {
		n := min(size, len(enc))
		chunk := enc[:n]
		enc = enc[n:]
		m := "m=0"
		if enc != "" {
			m = "m=1"
		}
		if first {
			fmt.Fprintf(&b, "\x1b_G%s,%s;%s\x1b\\", ctl, m, chunk)
		} else {
			fmt.Fprintf(&b, "\x1b_G%s;%s\x1b\\", m, chunk)
		}
	}
	return b.String()
}

func newGfx(t *testing.T) (*emulator, *replyBuf) {
	t.Helper()
	r := &replyBuf{}
	e := New(20, 6, r).(*emulator)
	t.Cleanup(func() { e.Close() })
	e.SetCellSize(10, 20)
	return e, r
}

// TestGraphicsPlaceAndScroll sends a chunked PNG with a=T, checks its
// placement, pixels, reply and cursor, then scrolls it into history and
// deletes it.
func TestGraphicsPlaceAndScroll(t *testing.T) {
	e, r := newGfx(t)
	e.Write([]byte("ab"))
	// 25x30 pixels at 10x20 cells: 3 columns, 2 rows. Each chunk is cut in
	// two Writes, as PTY reads cut them.
	s := apc("a=T,f=100,i=7", testPNG(t, 25, 30), 64)
	half := len(s) / 2
	e.Write([]byte(s[:half]))
	e.Write([]byte(s[half:] + "x"))
	r.waitFor(t, "\x1b_Gi=7;OK\x1b\\")

	g := e.Snapshot()
	if len(g.Images) != 1 {
		t.Fatalf("images %+v", g.Images)
	}
	p := g.Images[0]
	if p.X != 2 || p.Y != 0 || p.Cols != 3 || p.Rows != 2 || p.Src != image.Rect(0, 0, 25, 30) {
		t.Errorf("placement %+v", p)
	}
	// The cursor ends on the image's last row, past it; then x is printed.
	if row(g, 1) != "     x" {
		t.Errorf("row 1 %q, cursor %+v", row(g, 1), g.Cursor)
	}
	img, ok := e.Image(p.Image)
	if !ok || img.W != 25 || img.H != 30 || !bytes.Equal(img.Pix[:8], []byte{0, 0, 0xff, 0xff, 0xff, 0, 0, 0xff}) {
		t.Fatalf("image %v %dx%d %v", ok, img.W, img.H, img.Pix[:8])
	}

	e.Write([]byte("\r\n\r\n\r\n\r\n\r\n")) // one row of it scrolls off the top
	g = e.Snapshot()
	if len(g.Images) != 1 || g.Images[0].Y != -1 {
		t.Fatalf("after scrolling: %+v", g.Images)
	}
	e.Write([]byte("\r\n"))
	if g = e.Snapshot(); len(g.Images) != 0 {
		t.Fatalf("scrolled out of view: %+v", g.Images)
	}
	if g = e.SnapshotAt(2); len(g.Images) != 1 || g.Images[0].Y != 0 {
		t.Fatalf("in history: %+v", g.Images)
	}

	e.Write([]byte("\x1b_Ga=d,d=I,i=7\x1b\\"))
	if g = e.SnapshotAt(2); len(g.Images) != 0 {
		t.Fatalf("after delete: %+v", g.Images)
	}
	if _, ok := e.Image(p.Image); ok {
		t.Error("d=I kept the image")
	}
}

// TestGraphicsPut transmits RGB without showing it, places it twice with
// a=p and c=, then clears the screen.
func TestGraphicsPut(t *testing.T) {
	e, r := newGfx(t)
	rgb := bytes.Repeat([]byte{1, 2, 3}, 4*2)
	e.Write([]byte(apc("a=t,f=24,s=4,v=2,i=3", rgb, 4096)))
	r.waitFor(t, "i=3;OK")
	if g := e.Snapshot(); len(g.Images) != 0 {
		t.Fatalf("a=t placed: %+v", g.Images)
	}
	e.Write([]byte("\x1b_Ga=p,i=3,p=1,c=4,C=1\x1b\\\x1b_Ga=p,i=3,p=1,c=2,C=1,q=1\x1b\\\x1b_Ga=p,i=9\x1b\\"))
	// q=1 silenced the second OK.
	r.waitFor(t, "\x1b_Gi=3,p=1;OK\x1b\\\x1b_Gi=9;ENOENT")
	g := e.Snapshot()
	// The same placement id replaced the first; c=2 of a 4x2 image at
	// 10x20 cells is one row.
	if len(g.Images) != 1 || g.Images[0].Cols != 2 || g.Images[0].Rows != 1 || g.Cursor.X != 0 {
		t.Fatalf("placements %+v, cursor %+v", g.Images, g.Cursor)
	}
	if img, _ := e.Image(g.Images[0].Image); !bytes.Equal(img.Pix[:4], []byte{1, 2, 3, 0xff}) {
		t.Errorf("RGB pixel %v", img.Pix[:4])
	}
	e.Write([]byte("\x1b[2J"))
	if g = e.Snapshot(); len(g.Images) != 0 {
		t.Fatalf("ED 2 kept %+v", g.Images)
	}
	e.Write([]byte("\x1b_Ga=p,i=3\x1b\\"))
	r.waitFor(t, "i=3;OK") // lower-case placements' deletes keep the image
}

// TestGraphicsQuery checks a=q answers and stores nothing, refuses file
// transmission, and that with graphics off nothing answers.
func TestGraphicsQuery(t *testing.T) {
	e, r := newGfx(t)
	e.Write([]byte("\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\"))
	r.waitFor(t, "\x1b_Gi=31;OK\x1b\\")
	e.Write([]byte("\x1b_Ga=p,i=31\x1b\\"))
	r.waitFor(t, "i=31;ENOENT")
	e.Write([]byte("\x1b_Gi=32,a=q,t=f;L2V0Yy9wYXNzd2Q=\x1b\\"))
	r.waitFor(t, "i=32;EINVAL")
	// An upload cut off by a new command is dropped, not joined to it.
	e.Write([]byte("\x1b_Ga=t,f=24,s=1,v=1,i=33,m=1;AA\x1b\\\x1b_Ga=p,i=33\x1b\\"))
	r.waitFor(t, "i=33;ENOENT")
	// Ids use all of uint32; c= past every limit is capped, not wrapped.
	e.Write([]byte("\x1b_Gi=4000000000,s=1,v=1,a=T,f=24,c=99999999999,r=1,C=1;AAAA\x1b\\"))
	r.waitFor(t, "\x1b_Gi=4000000000;OK\x1b\\")
	if g := e.Snapshot(); len(g.Images) != 1 || g.Images[0].Cols != maxPlaceCells {
		t.Fatalf("placements %+v", g.Images)
	}
	e.Write([]byte("\x1b[16t"))
	r.waitFor(t, "\x1b[6;20;10t")

	off := &replyBuf{}
	o := New(20, 6, off).(*emulator)
	defer o.Close()
	o.SetGraphics(false)
	o.Write([]byte("\x1b_Gi=31,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\\x1b[c"))
	off.waitFor(t, "\x1b[?") // DA1 answers; the graphics query did not
	time.Sleep(20 * time.Millisecond)
	off.mu.Lock()
	defer off.mu.Unlock()
	if strings.Contains(off.b.String(), "_G") {
		t.Errorf("graphics off answered %q", off.b.String())
	}
}

// TestGraphicsCaps checks a pane's cap evicts its oldest image, and the
// global one another pane's.
func TestGraphicsCaps(t *testing.T) {
	e, _ := newGfx(t)
	// 2048x2048 RGBA is 16MB: four fill a pane's 64MB. Compressed, it is
	// a short sequence.
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(make([]byte, 2048*2048*4))
	zw.Close()
	big := func(e *emulator, id int) uint64 {
		e.Write([]byte(apc(fmt.Sprintf("a=T,f=32,o=z,s=2048,v=2048,i=%d,c=1,r=1,q=2", id), z.Bytes(), 4096)))
		g := e.Snapshot()
		return g.Images[len(g.Images)-1].Image
	}
	first := big(e, 1)
	for id := 2; id <= 4; id++ {
		big(e, id)
	}
	if _, ok := e.Image(first); !ok {
		t.Fatal("evicted below the pane cap")
	}
	big(e, 5)
	if _, ok := e.Image(first); ok {
		t.Fatal("the pane cap kept the oldest image")
	}
	if g := e.Snapshot(); len(g.Images) != 4 {
		t.Fatalf("placements %d, want 4", len(g.Images))
	}

	// Other panes fill the global cap: e's images, the oldest, go.
	var others []*emulator
	for range 4 {
		o, _ := newGfx(t)
		others = append(others, o)
		for id := 1; id <= 4; id++ {
			big(o, id)
		}
	}
	e.Write(nil)
	if g := e.Snapshot(); len(g.Images) != 0 {
		t.Fatalf("global cap kept %d placements", len(g.Images))
	}
	pictures.Lock()
	n := pictures.bytes
	pictures.Unlock()
	if n > allImageBytes {
		t.Errorf("global bytes %d over the cap", n)
	}
	for _, o := range others {
		o.st.gfx.releaseAll()
	}
	e.st.gfx.releaseAll()
}
