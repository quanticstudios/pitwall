package main

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// TestImagesCache checks an image is asked for once, and that past the
// cap the images of panes the window does not draw go, even while their
// stale frames still place them, and the drawn ones stay.
func TestImagesCache(t *testing.T) {
	b := &backend{view: map[string]bool{"a": true}, frames: map[string]proto.Frame{
		"a": {Grid: vt.Grid{Images: []vt.Placement{{Image: 1}}}},
		"b": {Grid: vt.Grid{Images: []vt.Placement{{Image: 2}}}}, // a tab not shown
	}}
	c := &b.imgs
	if !c.ask(1) || c.ask(1) {
		t.Fatal("asked twice, or not at all")
	}
	if _, ok := c.get(1); ok {
		t.Fatal("got an image before it came")
	}
	half := make([]byte, imageCap/2)
	c.put(vt.Image{Key: 1, W: 1, H: 1, Pix: half}, b.keepImages)
	c.put(vt.Image{Key: 2, W: 1, H: 1, Pix: half}, b.keepImages)
	c.put(vt.Image{Key: 3, W: 1, H: 1, Pix: half}, b.keepImages)
	for key, want := range map[uint64]bool{1: true, 2: false, 3: true} {
		if _, ok := c.get(key); ok != want {
			t.Errorf("image %d kept %v, want %v", key, ok, want)
		}
	}
	if c.bytes > imageCap {
		t.Errorf("%d bytes kept, over the cap", c.bytes)
	}
	if !c.ask(2) {
		t.Error("a dropped image is not asked for again")
	}
	c.put(vt.Image{Key: 4}, b.keepImages)
	if _, ok := c.get(4); ok || c.ask(4) {
		t.Error("an image the daemon lacks is drawn or asked for again")
	}
}
