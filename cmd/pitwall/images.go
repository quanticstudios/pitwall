package main

import (
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// imageCap is how many bytes of image pixels a window keeps, the daemon's
// own cap for every pane's images.
const imageCap = 256 << 20

// images is the window's copy of the images its frames place, by key. A
// nil entry was asked for and has not come; one with W 0, the daemon no
// longer has. Guarded by backend.mu.
type images struct {
	m     map[uint64]*vt.Image
	bytes int
}

// ask reports whether key is new, and marks it asked for.
func (c *images) ask(key uint64) bool {
	if c.m == nil {
		c.m = map[uint64]*vt.Image{}
	}
	if _, ok := c.m[key]; ok {
		return false
	}
	c.m[key] = nil
	return true
}

// put stores img. Past imageCap it drops the images no frame in frames
// places; one scrolled back into view is asked for again.
func (c *images) put(img vt.Image, frames map[string]proto.Frame) {
	if c.m == nil {
		c.m = map[uint64]*vt.Image{}
	}
	if old := c.m[img.Key]; old != nil {
		c.bytes -= len(old.Pix)
	}
	c.m[img.Key] = &img
	c.bytes += len(img.Pix)
	if c.bytes <= imageCap {
		return
	}
	used := map[uint64]bool{img.Key: true}
	for _, f := range frames {
		for _, p := range f.Grid.Images {
			used[p.Image] = true
		}
	}
	for k, i := range c.m {
		if i != nil && !used[k] {
			c.bytes -= len(i.Pix)
			delete(c.m, k)
		}
	}
}

// get returns key's image once it came.
func (c *images) get(key uint64) (vt.Image, bool) {
	if i := c.m[key]; i != nil && i.W > 0 {
		return *i, true
	}
	return vt.Image{}, false
}

// forget drops the asks the old connection never answered.
func (c *images) forget() {
	for k, i := range c.m {
		if i == nil {
			delete(c.m, k)
		}
	}
}

// Image implements app.Imager: the pixels of an image pane's frame
// places, asked for once until they come.
func (b *backend) Image(pane string, key uint64) (vt.Image, bool) {
	b.mu.Lock()
	img, ok := b.imgs.get(key)
	ask := !ok && b.imgs.ask(key)
	b.mu.Unlock()
	if ask {
		b.Send(proto.Image{Pane: pane, Key: key})
	}
	return img, ok
}
