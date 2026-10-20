package sidebar

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	xdraw "golang.org/x/image/draw"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A repo's own icon, its favicon or logo, stands in for the folder glyph on
// its group's header and leads its tabs' rows, with the agent's mark as a
// badge on its corner. It reads SVG, PNG and ICO files. A repo with none
// gets a tile with its name's first letter.

// faviconPaths are where a repo keeps its icon, best first, an SVG before
// a bitmap: .pitwall/icon.* is one the user put there to pick it, apps/*
// covers a monorepo's web apps.
var faviconPaths = []string{
	".pitwall/icon.svg", ".pitwall/icon.png", ".pitwall/icon.ico",
	"favicon.svg", "favicon.ico", "favicon.png",
	"public/favicon.svg", "public/favicon.ico", "public/favicon.png", "public/icon.svg", "public/icon.png", "public/apple-touch-icon.png", "public/logo.svg", "public/logo.png",
	"static/favicon.svg", "static/favicon.ico", "static/favicon.png",
	"app/icon.svg", "app/favicon.ico", "app/icon.png", "src/app/icon.svg", "src/app/favicon.ico", "src/app/icon.png",
	"assets/favicon.svg", "assets/favicon.ico", "assets/favicon.png", "assets/icon.svg", "assets/icon.png", "assets/logo.svg", "assets/logo.png",
	"build/icon.png", "logo.svg", "logo.png", "icon.svg", "icon.png", ".github/logo.svg", ".github/logo.png",
	"apps/*/public/favicon.svg", "apps/*/public/favicon.ico", "apps/*/public/favicon.png", "apps/*/app/icon.svg", "apps/*/app/favicon.ico",
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
	switch {
	case bytes.HasPrefix(b, pngMagic):
		img, _ := png.Decode(bytes.NewReader(b))
		return img
	case strings.EqualFold(filepath.Ext(path), ".svg"):
		return decodeSVG(b)
	}
	return decodeICO(b)
}

// svgPx is the size an SVG is drawn at before it is scaled to the row's.
const svgPx = 96

// decodeSVG draws an SVG icon in a svgPx square. Elements oksvg does not
// know are skipped; a file it cannot read, or one that makes it panic,
// gives nil, since any repo can hold one.
func decodeSVG(b []byte) (img image.Image) {
	defer func() {
		if recover() != nil {
			img = nil
		}
	}()
	icon, err := oksvg.ReadIconStream(bytes.NewReader(b), oksvg.IgnoreErrorMode)
	if err != nil || icon.ViewBox.W <= 0 || icon.ViewBox.H <= 0 {
		return nil
	}
	icon.SetTarget(0, 0, svgPx, svgPx)
	dst := image.NewRGBA(image.Rect(0, 0, svgPx, svgPx))
	icon.Draw(rasterx.NewDasher(svgPx, svgPx, rasterx.NewScannerGV(svgPx, svgPx, dst, dst.Bounds())), 1)
	return dst
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

	// With no src: a tile of col with letter on it.
	letter string
	col    color.NRGBA
	th     *theme.Theme
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
	if f.src == nil {
		return f.tile(gtx, size)
	}
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

// tabFavicon is the icon of the repo ws is in, or its letter tile; a folder
// outside a repo has none, so a home folder's stray icon.png never shows.
func (s *Sidebar) tabFavicon(v *view, ws model.Workspace) *favicon {
	if ws.Branch == "" {
		return nil
	}
	if f := s.favicon(cmp.Or(ws.RepoRoot, ws.Path)); f != nil {
		return f
	}
	// The tile is the repo's, named as its group or its main checkout, not
	// the worktree's folder.
	name, colorID := filepath.Base(cmp.Or(ws.WorktreeRoot, ws.RepoRoot, ws.Path)), ""
	for _, p := range v.st.Projects {
		if p.ID == ws.ProjectID {
			name, colorID = p.Name, p.Color
		}
	}
	return letterFavicon(v.th, name, colorID)
}

// groupFavicon is a git group's repo icon, or its letter tile, unless the
// user picked an icon for the group.
func (s *Sidebar) groupFavicon(th *theme.Theme, p model.Project) *favicon {
	if p.Icon != "" || p.Kind != model.ProjectGit {
		return nil
	}
	if f := s.favicon(p.Root); f != nil {
		return f
	}
	return letterFavicon(th, p.Name, p.Color)
}

// letterFavicon is a tile with name's first letter or digit, in the color
// colorID names, else one picked from name so a repo keeps its color.
func letterFavicon(th *theme.Theme, name, colorID string) *favicon {
	i := strings.IndexFunc(name, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
	if i < 0 {
		return nil
	}
	if colorID == "" {
		h := fnv.New32a()
		h.Write([]byte(name))
		colorID = tabColorIDs[1+int(h.Sum32()%uint32(len(tabColorIDs)-1))]
	}
	r, _ := utf8.DecodeRuneInString(name[i:])
	return &favicon{letter: string(unicode.ToUpper(r)), col: th.ProjectColor(colorID), th: th}
}

// tile draws f's letter on its color, dark on a light color.
func (f *favicon) tile(gtx layout.Context, size int) layout.Dimensions {
	rect := image.Rectangle{Max: image.Pt(size, size)}
	paint.FillShape(gtx.Ops, f.col, clip.UniformRRect(rect, size/5).Op(gtx.Ops))
	fg := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	if int(f.col.R)*299+int(f.col.G)*587+int(f.col.B)*114 > 160*1000 {
		fg = color.NRGBA{R: 20, G: 22, B: 26, A: 255}
	}
	m := op.Record(gtx.Ops)
	d := label(gtx, f.th, semibold(f.th.UIFont), unit.Sp(float32(size)*0.68/gtx.Metric.PxPerSp), fg, f.letter)
	call := m.Stop()
	defer op.Offset(image.Pt((size-d.Size.X)/2, (size-d.Size.Y)/2)).Push(gtx.Ops).Pop()
	call.Add(gtx.Ops)
	return layout.Dimensions{Size: rect.Max}
}
