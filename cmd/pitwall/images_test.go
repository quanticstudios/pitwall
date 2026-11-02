package main

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// TestImagesCache checks an image is asked for once, and that past the
// cap the images no frame shows go while the shown ones stay.
func TestImagesCache(t *testing.T) {
	var c images
	if !c.ask(1) || c.ask(1) {
		t.Fatal("asked twice, or not at all")
	}
	if _, ok := c.get(1); ok {
		t.Fatal("got an image before it came")
	}
	half := make([]byte, imageCap/2)
	frames := map[string]proto.Frame{"a": {Grid: vt.Grid{Images: []vt.Placement{{Image: 1}}}}}
	c.put(vt.Image{Key: 1, W: 1, H: 1, Pix: half}, frames)
	c.put(vt.Image{Key: 2, W: 1, H: 1, Pix: half}, frames)
	c.put(vt.Image{Key: 3, W: 1, H: 1, Pix: half}, frames)
	for key, want := range map[uint64]bool{1: true, 2: false, 3: true} {
		if _, ok := c.get(key); ok != want {
			t.Errorf("image %d kept %v, want %v", key, ok, want)
		}
	}
	if !c.ask(2) {
		t.Error("a dropped image is not asked for again")
	}
	c.put(vt.Image{Key: 4}, frames)
	if _, ok := c.get(4); ok || c.ask(4) {
		t.Error("an image the daemon lacks is drawn or asked for again")
	}
}
