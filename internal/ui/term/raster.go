package term

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg" // bitmap glyph formats
	_ "image/png"
	"io"
	"log"
	"math"
	"os"
	"strings"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/fontscan"
	"github.com/go-text/typesetting/language"
	"github.com/go-text/typesetting/shaping"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/math/fixed"
)

// Text is rasterized on the CPU into one image per row. Gio's vector text
// rebuilds glyph outlines for every new row and costs about 2.5 µs of CPU per
// path per frame; a row image is one draw call.

// fonts is the system font index shared by every View. Layout runs on the
// UI goroutine only, so it needs no lock. Tests set it before first use.
var fonts *fontscan.FontMap

func fontMap() *fontscan.FontMap {
	if fonts == nil {
		fonts = fontscan.NewFontMap(log.New(io.Discard, "", 0))
		dir, _ := os.UserCacheDir()
		if err := fonts.UseSystemFonts(dir); err != nil {
			log.Printf("term: system fonts: %v", err)
		}
	}
	return fonts
}

// families is the font query for th.MonoFont's typeface list, then any
// monospace font, then color emoji.
//
// Emoji always goes last: Noto Color Emoji has glyphs for the digits, so
// listing it before a text font draws numbers as faint color bitmaps.
func families(typeface string) []string {
	var fs []string
	for _, f := range strings.Split(typeface, ",") {
		if f = strings.TrimSpace(f); f != "" && f != "emoji" {
			fs = append(fs, f, nerdAlias(f))
		}
	}
	return append(fs, "monospace", "emoji")
}

// nerdAlias is the short family name fontscan indexes Nerd Fonts v3 under
// ("JetBrainsMono Nerd Font" is found only as "JetBrainsMono NF"); fontconfig
// knows both. Returns name unchanged for other fonts.
func nerdAlias(name string) string {
	for long, short := range map[string]string{" Nerd Font Mono": " NFM", " Nerd Font Propo": " NFP"} {
		if strings.HasSuffix(name, long) {
			return strings.TrimSuffix(name, long) + short
		}
	}
	if strings.HasSuffix(name, " Nerd Font") {
		return strings.TrimSuffix(name, " Nerd Font") + " NF"
	}
	return name
}

// face resolves the face for rune r in style f (bit 0 bold, bit 1 italic),
// falling back to other fonts when the mono font lacks r.
func (v *View) face(f uint8, r rune) *font.Face {
	fm := fontMap()
	asp := font.Aspect{Style: font.StyleNormal, Weight: font.WeightNormal}
	if f&1 != 0 {
		asp.Weight = font.WeightBold
	}
	if f&2 != 0 {
		asp.Style = font.StyleItalic
	}
	fm.SetQuery(fontscan.Query{Families: v.families, Aspect: asp})
	fm.SetScript(language.LookupScript(r))
	return fm.ResolveFace(r)
}

func (v *View) shape(face *font.Face, runes []rune) shaping.Output {
	return v.shaper.Shape(shaping.Input{
		Text: runes, RunEnd: len(runes), Direction: di.DirectionLTR,
		Face: face, Size: v.ppem, Script: language.LookupScript(runes[0]),
	})
}

// glyphImg is one grapheme rasterized at its place in a cell: off is the
// top-left of the image relative to the cell's top-left. Outline glyphs
// keep coverage in a and take the cell's color; bitmap glyphs (color
// emoji) keep premultiplied pixels in rgba.
type glyphImg struct {
	off  image.Point
	w, h int
	a    []uint8
	rgba *image.RGBA
}

type glyphKey struct {
	font uint8 // style bits, plus the cell width << 2
	s    string
}

// glyph shapes and rasterizes grapheme s in style f, centered in its width
// cells and snapped to whole pixels so fallback glyphs of other widths stay
// on the grid. Results are cached for the View's lifetime at this size.
func (v *View) glyph(f uint8, s string, width int) *glyphImg {
	k := glyphKey{f | uint8(width)<<2, s}
	if g, ok := v.glyphs[k]; ok {
		return g
	}
	g := v.rasterize(f, s, width)
	v.glyphs[k] = g
	return g
}

func (v *View) rasterize(f uint8, s string, width int) *glyphImg {
	runes := []rune(s)
	face := v.face(f, runes[0])
	if face == nil {
		return &glyphImg{}
	}
	out := v.shape(face, runes)
	ox := ((fixed.I(width*v.cell.X) - out.Advance) / 2).Round()
	scale := float32(v.ppem) / 64 / float32(face.Upem())
	by := float32(v.baseline)

	// Graphemes in a color font draw their bitmaps; the rest draw outlines.
	// ponytail: a grapheme mixing both kinds draws only its bitmaps.
	type bm struct {
		r   image.Rectangle
		img image.Image
	}
	var bms []bm
	var outlines []font.GlyphOutline
	var origins [][2]float32
	pen := fixed.Int26_6(0)
	for _, sg := range out.Glyphs {
		gx := float32(ox) + fixedF(pen+sg.XOffset)
		gy := by - fixedF(sg.YOffset)
		pen += sg.XAdvance
		switch d := face.GlyphData(sg.GlyphID).(type) {
		case font.GlyphOutline:
			outlines, origins = append(outlines, d), append(origins, [2]float32{gx, gy})
		case font.GlyphSVG:
			outlines, origins = append(outlines, d.Outline), append(origins, [2]float32{gx, gy})
		case font.GlyphBitmap:
			img, _, err := image.Decode(bytes.NewReader(d.Data))
			if err != nil {
				continue
			}
			x0 := int(gx + fixedF(sg.XBearing) + 0.5)
			y0 := int(gy - fixedF(sg.YBearing) + 0.5)
			r := image.Rect(x0, y0, x0+sg.Width.Round(), y0-sg.Height.Round())
			if !r.Empty() {
				bms = append(bms, bm{r, img})
			}
		}
	}

	if len(bms) > 0 {
		var b image.Rectangle
		for _, m := range bms {
			b = b.Union(m.r)
		}
		dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
		for _, m := range bms {
			xdraw.BiLinear.Scale(dst, m.r.Sub(b.Min), m.img, m.img.Bounds(), draw.Over, nil)
		}
		return &glyphImg{off: b.Min, w: b.Dx(), h: b.Dy(), rgba: dst}
	}

	// Walk every segment in pixel space, once for bounds and once to draw.
	walk := func(fn func(op ot.SegmentOp, pts []float32)) {
		var pts [6]float32
		for i, o := range outlines {
			for _, sgm := range o.Segments {
				n := 1
				switch sgm.Op {
				case ot.SegmentOpQuadTo:
					n = 2
				case ot.SegmentOpCubeTo:
					n = 3
				}
				for j := range n {
					pts[2*j] = origins[i][0] + sgm.Args[j].X*scale
					pts[2*j+1] = origins[i][1] - sgm.Args[j].Y*scale
				}
				fn(sgm.Op, pts[:2*n])
			}
		}
	}
	x0, y0, x1, y1 := float32(1e9), float32(1e9), float32(-1e9), float32(-1e9)
	walk(func(_ ot.SegmentOp, pts []float32) {
		for i := 0; i < len(pts); i += 2 {
			x0, x1 = min(x0, pts[i]), max(x1, pts[i])
			y0, y1 = min(y0, pts[i+1]), max(y1, pts[i+1])
		}
	})
	if x0 >= x1 || y0 >= y1 {
		return &glyphImg{}
	}
	b := image.Rect(floor(x0), floor(y0), floor(x1)+1, floor(y1)+1)
	z := &v.raster
	z.Reset(b.Dx(), b.Dy())
	dx, dy := float32(b.Min.X), float32(b.Min.Y)
	open := false
	walk(func(op ot.SegmentOp, p []float32) {
		switch op {
		case ot.SegmentOpMoveTo:
			if open {
				z.ClosePath()
			}
			z.MoveTo(p[0]-dx, p[1]-dy)
			open = true
		case ot.SegmentOpLineTo:
			z.LineTo(p[0]-dx, p[1]-dy)
		case ot.SegmentOpQuadTo:
			z.QuadTo(p[0]-dx, p[1]-dy, p[2]-dx, p[3]-dy)
		case ot.SegmentOpCubeTo:
			z.CubeTo(p[0]-dx, p[1]-dy, p[2]-dx, p[3]-dy, p[4]-dx, p[5]-dy)
		}
	})
	if open {
		z.ClosePath()
	}
	mask := image.NewAlpha(image.Rect(0, 0, b.Dx(), b.Dy()))
	z.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	return &glyphImg{off: b.Min, w: b.Dx(), h: b.Dy(), a: mask.Pix}
}

func fixedF(x fixed.Int26_6) float32 { return float32(x) / 64 }

func floor(x float32) int { return int(math.Floor(float64(x))) }

// fillRect fills r in img with c, blending when c is translucent (faint).
func fillRect(img *image.RGBA, r image.Rectangle, c color.NRGBA) {
	r = r.Intersect(img.Rect)
	if r.Empty() {
		return
	}
	if c.A != 0xff {
		for y := r.Min.Y; y < r.Max.Y; y++ {
			p := img.Pix[img.PixOffset(r.Min.X, y):][:r.Dx()*4]
			for i := 0; i < len(p); i += 4 {
				blend(p[i:i+4], c, uint32(c.A))
			}
		}
		return
	}
	row := img.Pix[img.PixOffset(r.Min.X, r.Min.Y):][:r.Dx()*4]
	for i := 0; i < len(row); i += 4 {
		row[i], row[i+1], row[i+2], row[i+3] = c.R, c.G, c.B, 0xff
	}
	for y := r.Min.Y + 1; y < r.Max.Y; y++ {
		copy(img.Pix[img.PixOffset(r.Min.X, y):], row)
	}
}

// blit draws g with its cell's top-left at (x, y) in img, in color c for
// outline glyphs. Blending is in sRGB space like xterm.js and Ghostty on
// Linux, which keeps light text on a dark background thin.
func blit(img *image.RGBA, x, y int, g *glyphImg, c color.NRGBA) {
	r := image.Rect(x+g.off.X, y+g.off.Y, x+g.off.X+g.w, y+g.off.Y+g.h)
	cl := r.Intersect(img.Rect)
	if cl.Empty() {
		return
	}
	if g.rgba != nil {
		draw.Draw(img, cl, g.rgba, cl.Min.Sub(r.Min), draw.Over)
		return
	}
	ca := uint32(c.A)
	for py := cl.Min.Y; py < cl.Max.Y; py++ {
		m := g.a[(py-r.Min.Y)*g.w+cl.Min.X-r.Min.X:][:cl.Dx()]
		p := img.Pix[img.PixOffset(cl.Min.X, py):]
		for i, a := range m {
			if a == 0 {
				continue
			}
			a := uint32(a) * ca / 0xff
			q := p[i*4 : i*4+4]
			if a == 0xff {
				q[0], q[1], q[2] = c.R, c.G, c.B
				continue
			}
			blend(q, c, a)
		}
	}
}

// blend mixes c over the opaque pixel q at coverage a (0-255).
func blend(q []uint8, c color.NRGBA, a uint32) {
	na := 0xff - a
	q[0] = uint8((uint32(q[0])*na + uint32(c.R)*a + 0x7f) / 0xff)
	q[1] = uint8((uint32(q[1])*na + uint32(c.G)*a + 0x7f) / 0xff)
	q[2] = uint8((uint32(q[2])*na + uint32(c.B)*a + 0x7f) / 0xff)
}
