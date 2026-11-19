package panel

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"testing"
)

// TestDecodeThumb: an image attachment scales to the thumbnail height,
// keeping its shape; data that is not an image gives no size.
func TestDecodeThumb(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 400, 800))); err != nil {
		t.Fatal(err)
	}
	_, size := decodeThumb(base64.StdEncoding.EncodeToString(b.Bytes()))
	if size != image.Pt(96, thumbHeight) {
		t.Fatalf("size %v", size)
	}
	if _, size := decodeThumb(base64.StdEncoding.EncodeToString([]byte("not an image"))); size != (image.Point{}) {
		t.Fatalf("garbage decoded to %v", size)
	}
}
