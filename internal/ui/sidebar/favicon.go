package sidebar

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	xdraw "golang.org/x/image/draw"

	"github.com/quanticstudios/pitwall/internal/model"
)

// A repo's own icon, its favicon or logo, stands in for the folder glyph on
// its group's header and leads its tabs' rows, with the agent's mark as a
// badge on its corner. Only PNG and ICO files count: an SVG needs a
// rasterizer pitwall does not have.

// faviconPaths are where a repo keeps its icon, best first; apps/* covers
// a monorepo's web apps.
var faviconPaths = []string{
	"favicon.ico", "favicon.png",
	"public/favicon.ico", "public/favicon.png", "public/icon.png", "public/apple-touch-icon.png", "public/logo.png",
	"static/favicon.ico", "static/favicon.png",
	"app/favicon.ico", "app/icon.png", "src/app/favicon.ico", "src/app/icon.png",
	"assets/favicon.ico", "assets/favicon.png", "assets/icon.png", "assets/logo.png",
	"build/icon.png", "logo.png", "icon.png", ".github/logo.png",
	"apps/*/public/favicon.ico", "apps/*/public/favicon.png", "apps/*/app/favicon.ico",
}

// maxFavicon caps the file read: a favicon is kilobytes.
const maxFavicon = 1 << 20

// findFavicon is the first icon under root that decodes, or nil.
func findFavicon(root string) image.Image {
	for _, p := range faviconPaths {
		matches, _ := filepath.Glob(filepath.Join(root, p))
		for _, m := range matches {
			if img := readFavicon(m); img != nil {
				return img
			}
		}
	}
	return nil
}

func readFavicon(path string) image.Image {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxFavicon {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if bytes.HasPrefix(b, pngMagic) {
		img, _ := png.Decode(bytes.NewReader(b))
		return img
	}
	return decodeICO(b)
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// decodeICO decodes an ICO file's largest image: a PNG one, or a 32-bit
// bitmap. Other bitmaps, with palettes or no alpha, give nil.
func decodeICO(b []byte) image.Image {
	le := binary.LittleEndian
	if len(b) < 6 || le.Uint16(b[0:]) != 0 || le.Uint16(b[2:]) != 1 {
		return nil
	}
	var best []byte
	bestW := 0
	for i := range int(le.Uint16(b[4:])) {
		e := 6 + 16*i
		if e+16 > len(b) {
			return nil
		}
		w := int(b[e])
		if w == 0 {
			w = 256
		}
		size, off := int(le.Uint32(b[e+8:])), int(le.Uint32(b[e+12:]))
		if off < 0 || size <= 0 || off+size > len(b) || w <= bestW {
			continue
		}
		best, bestW = b[off:off+size], w
	}
	if best == nil {
		return nil
	}
	if bytes.HasPrefix(best, pngMagic) {
		img, _ := png.Decode(bytes.NewReader(best))
		return img
	}
	// A BITMAPINFOHEADER, then bottom-up BGRA rows; the height counts the
	// AND mask after them too.
	if len(best) < 40 || le.Uint32(best) != 40 || le.Uint16(best[14:]) != 32 {
		return nil
	}
	w, h := int(int32(le.Uint32(best[4:]))), int(int32(le.Uint32(best[8:])))/2
	px := best[40:]
	if w <= 0 || h <= 0 || w > 256 || h > 256 || len(px) < w*h*4 {
		return nil
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	noAlpha := true
	for y := range h {
		row := px[(h-1-y)*w*4:]
		for x := range w {
			o := img.PixOffset(x, y)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = row[x*4+2], row[x*4+1], row[x*4], row[x*4+3]
			noAlpha = noAlpha && row[x*4+3] == 0
		}
	}
	if noAlpha { // an old icon keeps its transparency in the AND mask: draw it whole
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	}
	return img
}

// favicon is a repo's icon scaled to px pixels, or none.
type favicon struct {
	src image.Image
	px  int
	op  paint.ImageOp
}

// favicon is root's icon, looked up the first time root shows and kept
// for the window's life, misses too.
// ponytail: read on the UI goroutine and never refreshed; move the lookup
// off it, or recheck, if a slow disk or a changed icon ever matters.
func (s *Sidebar) favicon(root string) *favicon {
	if root == "" {
		return nil
	}
	if s.favicons == nil {
		s.favicons = map[string]*favicon{}
	}
	f, seen := s.favicons[root]
	if !seen {
		if img := findFavicon(root); img != nil {
			f = &favicon{src: img}
		}
		s.favicons[root] = f
	}
	return f
}

// draw paints the icon in a size-pixel square with rounded corners.
func (f *favicon) draw(gtx layout.Context, size int) layout.Dimensions {
	if f.px != size {
		dst := image.NewNRGBA(image.Rect(0, 0, size, size))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), f.src, f.src.Bounds(), xdraw.Over, nil)
		f.px, f.op = size, paint.NewImageOp(dst)
	}
	defer clip.UniformRRect(image.Rectangle{Max: image.Pt(size, size)}, size/5).Push(gtx.Ops).Pop()
	f.op.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	return layout.Dimensions{Size: image.Pt(size, size)}
}

// badged draws the repo's icon in the 12dp slot a row's mark takes, grown
// to 14dp as the mark is, and the agent's mark on its lower right corner
// on a disc of the row's color.
func badged(gtx layout.Context, f *favicon, mark func(gtx layout.Context, size int), base color.NRGBA) layout.Dimensions {
	sz := gtx.Dp(12)
	off := op.Offset(image.Pt(-gtx.Dp(1), -gtx.Dp(1))).Push(gtx.Ops)
	f.draw(gtx, gtx.Dp(14))
	off.Pop()
	d, m := gtx.Dp(11), gtx.Dp(8)
	c := image.Pt(sz, sz) // the disc's centre: the slot's corner
	paint.FillShape(gtx.Ops, base, clip.Ellipse{Min: c.Sub(image.Pt(d/2, d/2)), Max: c.Add(image.Pt(d-d/2, d-d/2))}.Op(gtx.Ops))
	off = op.Offset(c.Sub(image.Pt(m/2, m/2))).Push(gtx.Ops)
	mark(gtx, m)
	off.Pop()
	return layout.Dimensions{Size: image.Pt(sz, sz)}
}

// tabFavicon is the icon of the repo ws is in; a folder outside a repo has
// none, so a home folder's stray icon.png never shows.
func (s *Sidebar) tabFavicon(ws model.Workspace) *favicon {
	if ws.Branch == "" {
		return nil
	}
	return s.favicon(cmp.Or(ws.RepoRoot, ws.Path))
}

// groupFavicon is a git group's repo icon, unless the user picked an icon
// for the group.
func (s *Sidebar) groupFavicon(p model.Project) *favicon {
	if p.Icon != "" || p.Kind != model.ProjectGit {
		return nil
	}
	return s.favicon(p.Root)
}
