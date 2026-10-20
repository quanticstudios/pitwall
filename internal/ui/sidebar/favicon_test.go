package sidebar

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// ico wraps entries, each a PNG or a bitmap of width w, in an ICO file.
func ico(entries [][]byte, widths []int) []byte {
	le := binary.LittleEndian
	b := make([]byte, 6+16*len(entries))
	le.PutUint16(b[2:], 1)
	le.PutUint16(b[4:], uint16(len(entries)))
	for i, e := range entries {
		h := b[6+16*i:]
		h[0] = byte(widths[i])
		le.PutUint32(h[8:], uint32(len(e)))
		le.PutUint32(h[12:], uint32(len(b)))
		b = append(b, e...)
	}
	return b
}

func pngOf(t *testing.T, w int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, w))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// bmp32 is a w-by-w 32-bit ICO bitmap of one BGRA color, its AND mask left out.
func bmp32(w int, c color.NRGBA) []byte {
	le := binary.LittleEndian
	b := make([]byte, 40, 40+w*w*4)
	le.PutUint32(b, 40)
	le.PutUint32(b[4:], uint32(w))
	le.PutUint32(b[8:], uint32(2*w))
	le.PutUint16(b[12:], 1)
	le.PutUint16(b[14:], 32)
	for range w * w {
		b = append(b, c.B, c.G, c.R, c.A)
	}
	return b
}

func TestFavicon(t *testing.T) {
	red, blue := color.NRGBA{R: 255, A: 255}, color.NRGBA{B: 255, A: 255}
	at := func(img image.Image) color.NRGBA { return color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA) }

	// The largest entry wins, a PNG or a bitmap.
	if img := decodeICO(ico([][]byte{pngOf(t, 16, red), pngOf(t, 32, blue)}, []int{16, 32})); img == nil || img.Bounds().Dx() != 32 || at(img) != blue {
		t.Fatalf("PNG entries: %v", img)
	}
	if img := decodeICO(ico([][]byte{bmp32(16, red)}, []int{16})); img == nil || img.Bounds().Dx() != 16 || at(img) != red {
		t.Fatalf("bitmap entry: %v", img)
	}
	if img := decodeICO(ico([][]byte{bmp32(16, color.NRGBA{G: 255})}, []int{16})); img == nil || at(img).A != 255 {
		t.Fatalf("a bitmap with no alpha draws whole: %v", img)
	}
	if decodeICO([]byte("not an icon")) != nil {
		t.Fatal("garbage decoded")
	}

	write := func(root, rel string, b []byte) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := t.TempDir()
	write(repo, "apps/web/public/favicon.ico", ico([][]byte{pngOf(t, 16, blue)}, []int{16}))
	if img := findFavicon(repo); img == nil || at(img) != blue {
		t.Fatalf("a monorepo app's favicon: %v", img)
	}
	write(repo, "public/favicon.png", pngOf(t, 16, red))
	if img := findFavicon(repo); img == nil || at(img) != red {
		t.Fatalf("the repo's own favicon comes first: %v", img)
	}

	var s Sidebar
	if s.tabFavicon(model.Workspace{Path: repo, RepoRoot: repo}) != nil {
		t.Fatal("a tab outside a repo got an icon")
	}
	if s.tabFavicon(model.Workspace{Path: repo, RepoRoot: repo, Branch: "main"}) == nil {
		t.Fatal("a tab in the repo got none")
	}
	if s.groupFavicon(model.Project{Root: repo, Kind: model.ProjectGit, Icon: "rocket"}) != nil {
		t.Fatal("a group's own icon lost to the favicon")
	}
}
