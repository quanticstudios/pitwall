package session

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestImageFile(t *testing.T) {
	dir := t.TempDir()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	pic, txt := filepath.Join(dir, "a.png"), filepath.Join(dir, "a.txt")
	os.WriteFile(pic, b.Bytes(), 0o600)
	os.WriteFile(txt, []byte("not a picture"), 0o600)
	a, ok := ImageFile(pic, "", 2)
	if !ok || a.ID != "image-2" || a.Kind != "image" || a.MimeType != "image/png" || a.Name != "image-2.png" || a.SizeBytes != int64(b.Len()) || a.Data == "" {
		t.Errorf("png: %v %+v", ok, a)
	}
	if a, ok := ImageFile(pic, "image/jpeg", 1); !ok || a.MimeType != "image/jpeg" {
		t.Errorf("the transcript's type wins: %+v", a)
	}
	for _, p := range []string{txt, filepath.Join(dir, "gone.png"), dir} {
		if _, ok := ImageFile(p, "", 1); ok {
			t.Errorf("%s read as a picture", p)
		}
	}
}
