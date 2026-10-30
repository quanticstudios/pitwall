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
	"regexp"
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
	"github.com/quanticstudios/pitwall/internal/repoicon"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// A repo's own icon, its favicon or logo, stands in for the folder glyph on
// its group's header and leads its tabs' rows, with the agent's mark as a
// badge on its corner. It reads SVG, PNG and ICO files. A repo with none
// gets a tile with its name's first letter.

// findFavicon is the first of root's icon files that decodes, or nil.
func findFavicon(root string) image.Image {
	for _, f := range repoicon.Files(root) {
		if b, err := os.ReadFile(f); err == nil {
			if img := decodeFavicon(f, b); img != nil {
				return img
			}
		}
	}
	return nil
}

// decodeFavicon decodes b, the bytes of the icon file name, or gives nil.
func decodeFavicon(name string, b []byte) image.Image {
	switch {
	case bytes.HasPrefix(b, pngMagic):
		img, _ := png.Decode(bytes.NewReader(b))
		return img
	case strings.EqualFold(filepath.Ext(name), ".svg"):
		return decodeSVG(b)
	}
	return decodeICO(b)
}

// svgPx is the size an SVG is drawn at before it is scaled to the row's.
const svgPx = 96

var oneScale = regexp.MustCompile(`scale\(\s*([-+]?[0-9.]+(?:[eE][-+]?[0-9]+)?)\s*\)`)

// decodeSVG draws an SVG icon in a svgPx square. Elements oksvg does not
// know are skipped; a file it cannot read, or one that makes it panic,
// gives nil, since any repo can hold one.
func decodeSVG(b []byte) (img image.Image) {
	defer func() {
		if recover() != nil {
			img = nil
		}
	}()
	// workaround(oksvg be6e8873101c): it reads scale(s) as scale(s, 0),
	// which flattens the drawing to nothing; spell out the second factor.
	b = oneScale.ReplaceAll(b, []byte("scale(${1} ${1})"))
	icon, err := oksvg.ReadIconStream(bytes.NewReader(b), oksvg.IgnoreErrorMode)
	if err != nil || icon.ViewBox.W <= 0 || icon.ViewBox.H <= 0 {
		return nil
	}
	// workaround(oksvg be6e8873101c): SetTarget moves by the viewBox's
	// origin in pixels, not in its own units, so an offset viewBox drew
	// shifted; scale after moving instead.
	vb := icon.ViewBox
	icon.Transform = rasterx.Identity.Scale(svgPx/vb.W, svgPx/vb.H).Translate(-vb.X, -vb.Y)
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
// for the window's life, misses too. With RepoIcons it is nil, uncached,
// until the daemon answers.
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
		var img image.Image
		if s.RepoIcons == nil {
			img = findFavicon(root)
		} else {
			r, ok := s.RepoIcons(root)
			if !ok {
				return nil
			}
			for _, file := range r.Files {
				if img = decodeFavicon(file.Name, file.Data); img != nil {
					break
				}
			}
		}
		if img != nil {
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

// tabFavicon is the icon of the repo the tab's shell or agent is in now,
// or its letter tile; a folder outside a repo has none, so a home
// folder's stray icon.png never shows.
func (s *Sidebar) tabFavicon(v *view, ws model.Workspace) *favicon {
	root := s.tabRoot(v, ws)
	if root == "" {
		return nil
	}
	if f := s.favicon(root); f != nil {
		return f
	}
	// In its own repo the tile is its group's, or its main checkout's for
	// a worktree, not the worktree folder's.
	name, colorID := filepath.Base(root), ""
	if root == ws.RepoRoot {
		name = filepath.Base(cmp.Or(ws.WorktreeRoot, root))
		for _, p := range v.st.Projects {
			if p.ID == ws.ProjectID {
				name, colorID = p.Name, p.Color
			}
		}
	}
	return letterFavicon(v.th, name, colorID)
}

// tabRoot is the repo the tab's shell or agent is in now, "" outside one.
func (s *Sidebar) tabRoot(v *view, ws model.Workspace) string {
	root, ok := s.repoRoot(v.st.LivePath(ws))
	if !ok { // the daemon has not answered, or is too old to: the tab's own repo
		root = ws.RepoRoot
	}
	return root
}

// mainCheckout is the repo a tab in root is in, a worktree counting as
// its main checkout: the one pitwall made it from, or the folder holding
// the .worktrees it sits in.
// ponytail: a worktree elsewhere counts as a repo of its own; ask git for
// its common dir if that ever matters.
func mainCheckout(ws model.Workspace, root string) string {
	if ws.WorktreeRoot != "" && root == ws.RepoRoot {
		return ws.WorktreeRoot
	}
	if root != "" && filepath.Base(filepath.Dir(root)) == ".worktrees" {
		return filepath.Dir(filepath.Dir(root))
	}
	return root
}

// groupRoot is group p's repo: a git group's own, or the one all of a
// plain group's tabs are in; "" for none.
func (s *Sidebar) groupRoot(v *view, p model.Project) string {
	switch {
	case p.Kind == model.ProjectGit:
		return p.Root
	case p.Kind != model.ProjectGroup:
		return ""
	}
	root := ""
	for i, ws := range v.groupTabs(p.ID) {
		r := mainCheckout(ws, s.tabRoot(v, ws))
		if r == "" || i > 0 && r != root {
			return ""
		}
		root = r
	}
	return root
}

// repoRoot is the folder at or above dir that holds .git, or "" outside
// a repo; kept per dir for the window's life. ok is false while
// RepoIcons has no answer.
func (s *Sidebar) repoRoot(dir string) (root string, ok bool) {
	if dir == "" {
		return "", true
	}
	if root, ok := s.roots[dir]; ok {
		return root, true
	}
	if s.RepoIcons == nil {
		root = repoicon.Root(dir)
	} else {
		r, ok := s.RepoIcons(dir)
		if !ok {
			return "", false
		}
		root = r.Root
	}
	if s.roots == nil {
		s.roots = map[string]string{}
	}
	s.roots[dir] = root
	return root, true
}

// groupFavicon is the icon of groupRoot's repo, or its letter tile,
// unless the user picked an icon for the group.
func (s *Sidebar) groupFavicon(v *view, p model.Project) *favicon {
	if p.Icon != "" {
		return nil
	}
	return s.repoTile(v, p)
}

// repoTile is the icon group p shows with no icon picked: its repo's, or
// the letter tile of a repo without one; nil with no repo.
func (s *Sidebar) repoTile(v *view, p model.Project) *favicon {
	root := s.groupRoot(v, p)
	if root == "" {
		return nil
	}
	if f := s.favicon(root); f != nil {
		return f
	}
	return letterFavicon(v.th, p.Name, p.Color)
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
