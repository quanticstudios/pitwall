package app

import (
	"image"
	"slices"
	"testing"
)

func TestFileURIs(t *testing.T) {
	got := fileURIs("# files\r\nfile:///tmp/a%20b.png\r\nfile://localhost/tmp/%E7%94%BB%E5%83%8F.png\n" +
		"https://example.com/image.png\nfile://other/tmp/a.png\nfile:///tmp/%zz\nfile:relative.png\nfile:///tmp/a%00b\nfile:///tmp/a?query\n")
	if want := []string{"/tmp/a b.png", "/tmp/画像.png"}; !slices.Equal(got, want) {
		t.Errorf("decoded %q, want %q", got, want)
	}
}

func TestFileDropEvent(t *testing.T) {
	// Native drops use the same content coordinates as a frame. Distinct
	// drops must survive coalescing until the app consumes them.
	w := Window{}
	w.lastFrame.off = image.Pt(0, 28)
	w.viewport.Min = image.Pt(4, 6)
	for _, path := range []string{"a.png", "b.png"} {
		w.processEvent(FileDropEvent{Position: image.Pt(14, 54), Paths: []string{path}})
	}
	for _, path := range []string{"a.png", "b.png"} {
		e, ok := w.nextEvent()
		drop, isDrop := e.(FileDropEvent)
		if !ok || !isDrop || drop.Position != image.Pt(10, 20) || !slices.Equal(drop.Paths, []string{path}) {
			t.Fatalf("drop: %+v, %v", e, ok)
		}
	}
}
