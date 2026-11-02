package vt

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io"
	"math/rand/v2"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/charmbracelet/x/ansi"
	xvt "github.com/charmbracelet/x/vt"
)

// Placement is an image drawn over the grid, from the kitty graphics
// protocol. Its top left corner is cell (X, Y), Y below 0 when it starts
// above the view; it covers Cols x Rows cells, with Src, the part of the
// image shown, scaled to fill them.
type Placement struct {
	Image      uint64 // Image.Key
	X, Y       int
	Cols, Rows int
	Src        image.Rectangle
}

// Image is a picture a program sent: W*H pixels of non-premultiplied RGBA
// in Pix, which nobody changes. Key is unique among the images of every
// emulator in the process, and random between processes, so a client can
// cache images by it across panes and reconnects.
type Image struct {
	Key  uint64
	W, H int
	Pix  []byte
}

// Image caps, in decoded bytes: one image (it has to fit a proto message),
// one pane's, every pane's. Past a cap the oldest images go.
const (
	maxImageBytes  = 32 << 20
	paneImageBytes = 64 << 20
	allImageBytes  = 256 << 20
	maxImages      = 512  // a pane's
	maxPlacements  = 4096 // a pane's
	maxImageSide   = 8192 // a GPU texture side every driver takes
	maxPlaceCells  = 1000 // c and r
)

// defaultCell is the cell size in pixels until a window reports its own.
var defaultCell = image.Pt(8, 16)

// picture is a stored image. pix goes nil when the global cap evicts it,
// which can happen from another pane's goroutine.
type picture struct {
	id   uint32 // the program's i=, 0 for an image it cannot name again
	key  uint64
	w, h int
	pix  atomic.Pointer[[]byte]
}

func (p *picture) size() int { return p.w * p.h * 4 }

// pictures is every pane's images, oldest first, for allImageBytes.
var pictures struct {
	sync.Mutex
	all   []*picture
	bytes int
}

var nextKey atomic.Uint64

func init() { nextKey.Store(rand.Uint64() >> 1) }

// keep counts p in the global cap, evicting the oldest images past it.
func keep(p *picture) {
	pictures.Lock()
	defer pictures.Unlock()
	pictures.all = append(pictures.all, p)
	pictures.bytes += p.size()
	for pictures.bytes > allImageBytes && len(pictures.all) > 1 {
		o := pictures.all[0]
		pictures.all = pictures.all[1:]
		pictures.bytes -= o.size()
		o.pix.Store(nil)
	}
}

// release frees ps' pixels and drops them from the global cap.
func release(ps ...*picture) {
	pictures.Lock()
	defer pictures.Unlock()
	for _, p := range ps {
		p.pix.Store(nil)
		if i := slices.Index(pictures.all, p); i >= 0 {
			pictures.all = slices.Delete(pictures.all, i, i+1)
			pictures.bytes -= p.size()
		}
	}
}

// place is one placement of a picture. line numbers rows as
// ScrollbackPushed does, so a placement scrolls with its text into
// history.
type place struct {
	img        *picture
	id         uint32 // the program's p=, 0 for none
	line       uint64 // the row of its top edge
	col        int
	cols, rows int
	src        image.Rectangle
	alt        bool // on the alt screen
}

// graphics is a pane's kitty graphics state. It lives in state, under the
// emulator's lock.
type graphics struct {
	off    bool
	cell   image.Point // a cell in pixels, from the window
	imgs   []*picture  // oldest first
	places []*place    // oldest first
	bytes  int
	upload *gcmd // a chunked transmission waiting for its last chunk
	// move is where a placement moved the cursor, applied once the
	// parser is back between sequences: down rows, then to column col.
	move *[2]int
}

// gcmd is one graphics command: its control keys and payload.
type gcmd struct {
	keys map[byte]string
	data []byte // base64, as sent
}

func (c *gcmd) str(k byte, def byte) byte {
	if v := c.keys[k]; v != "" {
		return v[0]
	}
	return def
}

func (c *gcmd) num(k byte) int {
	n, _ := strconv.ParseInt(c.keys[k], 10, 64)
	return int(max(min(n, 1<<32), -1<<32))
}

// parseGraphics reads "key=value,..." control data.
func parseGraphics(ctl []byte) map[byte]string {
	keys := map[byte]string{}
	for kv := range bytes.SplitSeq(ctl, []byte(",")) {
		if len(kv) >= 2 && kv[1] == '=' {
			keys[kv[0]] = string(kv[2:])
		}
	}
	return keys
}

// registerGraphics handles APC G, the kitty graphics protocol: direct
// transmission (chunked or not) of RGB, RGBA and PNG images, placing them
// at the cursor, deleting them and queries. Files, temp files and shared
// memory are refused: a pane must not make the daemon read paths. See
// https://sw.kovidgoyal.net/kitty/graphics-protocol/. With graphics off it
// answers nothing, as a terminal without them does.
func registerGraphics(e *xvt.Emulator, st *state) {
	e.RegisterApcHandler(func(data []byte) bool {
		if len(data) == 0 || data[0] != 'G' {
			return false
		}
		g := &st.gfx
		if g.off {
			return true
		}
		ctl, payload, _ := bytes.Cut(data[1:], []byte(";"))
		c := &gcmd{keys: parseGraphics(ctl)}
		if u := g.upload; u != nil && !continues(c) {
			g.upload = nil // a new command abandons an unfinished upload
		}
		if u := g.upload; u != nil {
			if len(u.data)+len(payload) > base64.StdEncoding.EncodedLen(maxImageBytes) {
				g.upload = nil
				g.reply(e, u, "EFBIG:image too large")
				return true
			}
			u.data = append(u.data, payload...)
			if c.keys['q'] != "" {
				u.keys['q'] = c.keys['q']
			}
			if c.keys['m'] == "1" {
				return true
			}
			g.upload, c = nil, u
		} else {
			c.data = slices.Clone(payload)
			if c.keys['m'] == "1" {
				g.upload = c
				return true
			}
		}
		g.run(e, st, c)
		return true
	})
	// CSI 14 t and 16 t report the text area and a cell in pixels, for
	// programs that size images without TIOCGWINSZ.
	e.RegisterCsiHandler('t', func(p ansi.Params) bool {
		n, _, _ := p.Param(0, 0)
		cell := st.gfx.cellSize()
		switch n {
		case 14:
			_, _ = fmt.Fprintf(e.InputPipe(), "\x1b[4;%d;%dt", cell.Y*e.Height(), cell.X*e.Width())
		case 16:
			_, _ = fmt.Fprintf(e.InputPipe(), "\x1b[6;%d;%dt", cell.Y, cell.X)
		default:
			return false
		}
		return true
	})
}

// continues reports whether c can be the next chunk of an upload: chunks
// after the first carry m= and q= alone.
func continues(c *gcmd) bool {
	for k := range c.keys {
		if k != 'm' && k != 'q' {
			return false
		}
	}
	return true
}

func (g *graphics) cellSize() image.Point {
	if g.cell.X > 0 && g.cell.Y > 0 {
		return g.cell
	}
	return defaultCell
}

// reply answers c unless it named no image or its q= silences msg.
func (g *graphics) reply(e *xvt.Emulator, c *gcmd, msg string) {
	id := c.num('i')
	q := c.num('q')
	if id <= 0 || q >= 1 && msg == "OK" || q >= 2 {
		return
	}
	pid := ""
	if p := c.num('p'); p > 0 {
		pid = ",p=" + strconv.Itoa(p)
	}
	_, _ = fmt.Fprintf(e.InputPipe(), "\x1b_Gi=%d%s;%s\x1b\\", id, pid, msg)
}

// rowLine is the row number of the screen's row y, as place.line counts. x/vt
// holds lines that scrolled off during this Write until Write moves them
// into history.
func rowLine(e *xvt.Emulator, st *state, y int) uint64 {
	return st.hist.pushed + uint64(e.Scrollback().Len()) + uint64(y)
}

func (g *graphics) run(e *xvt.Emulator, st *state, c *gcmd) {
	a := c.str('a', 't')
	switch a {
	case 't', 'T', 'q':
		if t := c.str('t', 'd'); t != 'd' {
			g.reply(e, c, "EINVAL:only direct transmission (t=d) is supported")
			return
		}
		if c.num('I') != 0 {
			g.reply(e, c, "EINVAL:image numbers (I=) are not supported")
			return
		}
		p, err := decodeGraphics(c)
		if err != nil {
			g.reply(e, c, err.Error())
			return
		}
		if a == 'q' {
			g.reply(e, c, "OK")
			return
		}
		p.id = uint32(max(0, c.num('i')))
		g.store(p)
		if a == 'T' {
			g.place(e, st, p, c)
		}
		g.reply(e, c, "OK")
	case 'p':
		p := g.find(c.num('i'))
		if p == nil {
			g.reply(e, c, "ENOENT:no image with this id")
			return
		}
		g.place(e, st, p, c)
		g.reply(e, c, "OK")
	case 'd':
		g.delete(e, st, c)
	}
}

func (g *graphics) find(id int) *picture {
	if id <= 0 {
		return nil
	}
	for _, p := range slices.Backward(g.imgs) {
		if p.id == uint32(id) && p.pix.Load() != nil {
			return p
		}
	}
	return nil
}

// store adds p, replacing an image of the same id with its placements,
// and evicts the oldest images past the pane's caps.
func (g *graphics) store(p *picture) {
	if p.id != 0 {
		g.drop(func(o *picture) bool { return o.id == p.id })
	}
	g.imgs = append(g.imgs, p)
	g.bytes += p.size()
	keep(p)
	for len(g.imgs) > 1 && (g.bytes > paneImageBytes || len(g.imgs) > maxImages) {
		o := g.imgs[0]
		g.drop(func(x *picture) bool { return x == o })
	}
}

// drop removes the images gone matches, and their placements.
func (g *graphics) drop(gone func(*picture) bool) {
	var freed []*picture
	g.imgs = slices.DeleteFunc(g.imgs, func(p *picture) bool {
		if gone(p) {
			freed = append(freed, p)
			g.bytes -= p.size()
			return true
		}
		return false
	})
	if len(freed) == 0 {
		return
	}
	g.places = slices.DeleteFunc(g.places, func(pl *place) bool { return slices.Contains(freed, pl.img) })
	release(freed...)
}

// place puts p at the cursor and moves the cursor past it unless C=1.
func (g *graphics) place(e *xvt.Emulator, st *state, p *picture, c *gcmd) {
	src := image.Rect(c.num('x'), c.num('y'), 0, 0)
	src.Max = src.Min.Add(image.Pt(c.num('w'), c.num('h')))
	if src.Dx() <= 0 {
		src.Max.X = p.w
	}
	if src.Dy() <= 0 {
		src.Max.Y = p.h
	}
	src = src.Intersect(image.Rect(0, 0, p.w, p.h))
	if src.Empty() {
		return
	}
	cell := g.cellSize()
	cols, rows := c.num('c'), c.num('r')
	switch {
	case cols <= 0 && rows <= 0:
		cols, rows = ceilDiv(src.Dx(), cell.X), ceilDiv(src.Dy(), cell.Y)
	case rows <= 0:
		rows = ceilDiv(cols*cell.X*src.Dy(), src.Dx()*cell.Y)
	case cols <= 0:
		cols = ceilDiv(rows*cell.Y*src.Dx(), src.Dy()*cell.X)
	}
	cols, rows = min(max(cols, 1), maxPlaceCells), min(max(rows, 1), maxPlaceCells)
	pos := e.CursorPosition()
	pl := &place{img: p, id: uint32(max(0, c.num('p'))), line: rowLine(e, st, pos.Y), col: pos.X,
		cols: cols, rows: rows, src: src, alt: e.IsAltScreen()}
	if pl.id != 0 {
		g.places = slices.DeleteFunc(g.places, func(o *place) bool { return o.img == p && o.id == pl.id })
	}
	g.places = append(g.places, pl)
	if len(g.places) > maxPlacements {
		g.places = slices.Delete(g.places, 0, len(g.places)-maxPlacements)
	}
	if c.num('C') != 1 {
		// As kitty: the cursor ends on the image's last row, past its
		// last column.
		g.move = &[2]int{rows - 1, pos.X + cols}
	}
}

func ceilDiv(a, b int) int { return (a + b - 1) / max(b, 1) }

// applyMove moves the cursor where the last placement left it. Write calls
// it once the parser is back between sequences: x/vt cannot take input
// from inside a handler.
func (g *graphics) applyMove(e *xvt.Emulator) {
	m := g.move
	if m == nil {
		return
	}
	g.move = nil
	var b bytes.Buffer
	for range m[0] {
		b.WriteString("\x1bD") // IND scrolls at the bottom margin, as kitty does
	}
	if m[1] >= e.Width() {
		b.WriteString("\r\x1bD")
	} else {
		fmt.Fprintf(&b, "\x1b[%dG", m[1]+1)
	}
	_, _ = e.Write(b.Bytes())
}

// delete runs a=d. Lower-case targets remove placements; upper-case ones
// also free images left without one.
func (g *graphics) delete(e *xvt.Emulator, st *state, c *gcmd) {
	d := c.str('d', 'a')
	free := d >= 'A' && d <= 'Z'
	d |= 0x20
	pos := e.CursorPosition()
	alt := e.IsAltScreen()
	top := rowLine(e, st, 0)
	// at reports whether pl covers cell (x, y) of the screen, 1-based as
	// in x= and y=; 0 matches any column or row.
	at := func(pl *place, x, y int) bool {
		row := int64(top) + int64(y) - 1
		return (x == 0 || x-1 >= pl.col && x-1 < pl.col+pl.cols) &&
			(y == 0 || row >= int64(pl.line) && row < int64(pl.line)+int64(pl.rows))
	}
	var gone func(*place) bool
	var named func(*picture) bool // images the command names whether placed or not
	switch d {
	case 'a':
		gone = func(pl *place) bool {
			return pl.alt == alt && int64(pl.line)+int64(pl.rows) > int64(top) && pl.line < top+uint64(e.Height())
		}
	case 'i':
		id, pid := uint32(max(0, c.num('i'))), uint32(max(0, c.num('p')))
		gone = func(pl *place) bool { return pl.img.id == id && (pid == 0 || pl.id == pid) }
		named = func(p *picture) bool { return p.id == id }
	case 'c':
		gone = func(pl *place) bool { return pl.alt == alt && at(pl, pos.X+1, pos.Y+1) }
	case 'p':
		gone = func(pl *place) bool { return pl.alt == alt && at(pl, c.num('x'), c.num('y')) }
	case 'x':
		gone = func(pl *place) bool { return pl.alt == alt && at(pl, c.num('x'), 0) }
	case 'y':
		gone = func(pl *place) bool { return pl.alt == alt && at(pl, 0, c.num('y')) }
	case 'r':
		lo, hi := uint32(max(0, c.num('x'))), uint32(max(0, c.num('y')))
		gone = func(pl *place) bool { return pl.img.id >= lo && pl.img.id <= hi }
		named = func(p *picture) bool { return p.id >= lo && p.id <= hi }
	default:
		return // n (image numbers), f (frames), q and z (z-index) are not kept
	}
	touched := map[*picture]bool{}
	g.places = slices.DeleteFunc(g.places, func(pl *place) bool {
		if gone(pl) {
			touched[pl.img] = true
			return true
		}
		return false
	})
	if free {
		g.dropUnplaced(func(p *picture) bool { return touched[p] || named != nil && named(p) })
	}
	g.dropUnplaced(func(p *picture) bool { return p.id == 0 })
}

// dropUnplaced frees the images which matches and no placement shows.
func (g *graphics) dropUnplaced(which func(*picture) bool) {
	g.drop(func(p *picture) bool {
		return which(p) && !slices.ContainsFunc(g.places, func(pl *place) bool { return pl.img == p })
	})
}

// clear removes placements: those on the screen showing (ED 2), all
// (RIS), or those in history (ED 3).
func (g *graphics) clear(e *xvt.Emulator, st *state, what string) {
	top, alt := rowLine(e, st, 0), e.IsAltScreen()
	g.places = slices.DeleteFunc(g.places, func(pl *place) bool {
		switch what {
		case "screen":
			return pl.alt == alt && int64(pl.line)+int64(pl.rows) > int64(top)
		case "history":
			return !pl.alt && pl.line < top
		}
		return true
	})
	g.dropUnplaced(func(p *picture) bool { return p.id == 0 })
}

// prune drops what Write left unreachable: alt screen placements once the
// main screen is back, placements that scrolled out of history, and
// images the global cap evicted.
func (g *graphics) prune(e *xvt.Emulator, st *state) {
	if len(g.places) == 0 && len(g.imgs) == 0 {
		return
	}
	alt := e.IsAltScreen()
	oldest := st.hist.pushed - uint64(st.hist.len())
	g.places = slices.DeleteFunc(g.places, func(pl *place) bool {
		return pl.alt && !alt || pl.line+uint64(pl.rows) <= oldest || pl.img.pix.Load() == nil
	})
	g.drop(func(p *picture) bool { return p.pix.Load() == nil })
	g.dropUnplaced(func(p *picture) bool { return p.id == 0 })
}

// view lists the placements in a view off lines above the live screen,
// rows tall, oldest first.
func (g *graphics) view(e *xvt.Emulator, st *state, off, rows int) []Placement {
	if len(g.places) == 0 {
		return nil
	}
	alt := e.IsAltScreen()
	top := int64(rowLine(e, st, 0)) - int64(off)
	var out []Placement
	for _, pl := range g.places {
		y := int64(pl.line) - top
		if pl.alt != alt || y >= int64(rows) || y+int64(pl.rows) <= 0 || pl.img.pix.Load() == nil {
			continue
		}
		out = append(out, Placement{Image: pl.img.key, X: pl.col, Y: int(y), Cols: pl.cols, Rows: pl.rows, Src: pl.src})
	}
	return out
}

// image returns the stored image with key.
func (g *graphics) image(key uint64) (Image, bool) {
	for _, p := range g.imgs {
		if p.key == key {
			if pix := p.pix.Load(); pix != nil {
				return Image{Key: key, W: p.w, H: p.h, Pix: *pix}, true
			}
		}
	}
	return Image{}, false
}

// decodeGraphics turns c's payload into a picture, its error a reply.
func decodeGraphics(c *gcmd) (*picture, error) {
	raw, err := base64.StdEncoding.DecodeString(string(c.data))
	if err != nil {
		// Some programs leave the padding off.
		raw, err = base64.RawStdEncoding.DecodeString(string(bytes.TrimRight(c.data, "=")))
	}
	if err != nil {
		return nil, fmt.Errorf("EINVAL:bad base64")
	}
	if c.str('o', 0) == 'z' {
		zr, err := zlib.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("EINVAL:bad zlib data")
		}
		raw, err = io.ReadAll(io.LimitReader(zr, maxImageBytes+1))
		if err != nil || len(raw) > maxImageBytes {
			return nil, fmt.Errorf("EINVAL:bad zlib data or image too large")
		}
	}
	w, h := c.num('s'), c.num('v')
	p := &picture{key: nextKey.Add(1)}
	var pix []byte
	switch f := c.num('f'); f {
	case 0, 24, 32:
		bpp := 4
		if f == 24 {
			bpp = 3
		}
		if w <= 0 || h <= 0 || w > maxImageSide || h > maxImageSide || w*h*4 > maxImageBytes {
			return nil, fmt.Errorf("EINVAL:bad or too large size s=%d v=%d", w, h)
		}
		if len(raw) < w*h*bpp {
			return nil, fmt.Errorf("ENODATA:need %d bytes, got %d", w*h*bpp, len(raw))
		}
		pix = raw[:w*h*bpp]
		if bpp == 3 {
			pix = make([]byte, w*h*4)
			for i := range w * h {
				copy(pix[i*4:], raw[i*3:i*3+3])
				pix[i*4+3] = 0xff
			}
		} else {
			pix = slices.Clone(pix)
		}
	case 100:
		cfg, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("EBADPNG:%v", err)
		}
		w, h = cfg.Width, cfg.Height
		if w <= 0 || h <= 0 || w > maxImageSide || h > maxImageSide || w*h*4 > maxImageBytes {
			return nil, fmt.Errorf("EINVAL:image too large")
		}
		m, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("EBADPNG:%v", err)
		}
		n, ok := m.(*image.NRGBA)
		if !ok || n.Stride != w*4 || n.Rect.Min != (image.Point{}) {
			n = image.NewNRGBA(image.Rect(0, 0, w, h))
			draw.Draw(n, n.Rect, m, m.Bounds().Min, draw.Src)
		}
		pix = n.Pix
	default:
		return nil, fmt.Errorf("EINVAL:unsupported format f=%d", f)
	}
	p.w, p.h = w, h
	p.pix.Store(&pix)
	return p, nil
}

// releaseAll frees every image of g, once its emulator is gone.
func (g *graphics) releaseAll() { release(g.imgs...) }

// leaveAlt drops the alt screen's placements when mode m switches screens:
// the alt screen starts blank each time.
func (g *graphics) leaveAlt(m ansi.Mode) {
	switch m {
	case ansi.ModeAltScreen, ansi.ModeAltScreenSaveCursor, ansi.DECMode(47):
		g.places = slices.DeleteFunc(g.places, func(pl *place) bool { return pl.alt })
		g.dropUnplaced(func(p *picture) bool { return p.id == 0 })
	}
}
