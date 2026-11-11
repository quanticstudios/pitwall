package mdview

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/x/richtext"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// MaxDocBytes is the largest markdown file the reader opens.
const MaxDocBytes = 10 << 20

// pollEvery is how often an open file is checked for changes.
const pollEvery = 500 * time.Millisecond

// View is the reader: one markdown file over the panes, with Back
// through the files its links opened.
type View struct {
	// Invalidate asks for a frame. The file watcher and image loads call
	// it from their goroutines.
	Invalidate func()
	// OpenURL opens a link that is not a markdown file on this machine.
	OpenURL func(string)

	on      bool
	path    string
	history []entry
	scroll  int    // pixels from the top
	anchor  string // a heading to scroll to once laid out
	note    string // why the last link did not open
	moved   bool   // a link or Back changed the file or the place mid-frame

	mu     sync.Mutex
	doc    []Block
	gen    int // counts loads and reloads
	stamp  stamp
	ctx    context.Context
	stop   context.CancelFunc
	imgs   map[string]*picture
	imgGen int

	// The UI goroutine's alone.
	heights  []int
	measured measure
	texts    map[int]*richtext.InteractiveText
	textsGen int
	clicks   map[int]*widget.Clickable // image links
	closeBtn widget.Clickable
	backBtn  widget.Clickable
	tag      int // takes the reader's keys and presses
	focus    bool
	scr      gesture.Scroll
	bar      int // the scrollbar's strip
	mono     float32
	scratch  op.Ops // measures block heights
	cellOps  op.Ops // measures table cells, inside a block being measured
	cellText richtext.InteractiveText
	viewH    int
}

type entry struct {
	path   string
	scroll int
}

type stamp struct {
	mod  time.Time
	size int64
}

type measure struct {
	width, gen, imgGen int
	th                 *theme.Theme
}

type picture struct {
	img  paint.ImageOp
	size image.Point
	err  string
	done bool
	url  bool
}

// On reports whether the reader is open.
func (v *View) On() bool { return v.on }

// Path is the file the reader shows.
func (v *View) Path() string { return v.path }

// Open shows the markdown file at path from its heading frag, or its
// top for "", with no history to go back through.
func (v *View) Open(path, frag string) error {
	b, st, err := readDoc(path)
	if err != nil {
		return errors.New(plainErr(err))
	}
	v.history, v.scroll, v.note, v.anchor = nil, 0, "", frag
	v.load(path, b, st)
	v.on, v.focus = true, true
	return nil
}

// Close hides the reader and stops watching its file.
func (v *View) Close() {
	v.on, v.history, v.note = false, nil, ""
	v.mu.Lock()
	if v.stop != nil {
		v.stop()
	}
	v.doc, v.imgs, v.stop = nil, nil, nil
	v.mu.Unlock()
}

// Back shows the file before this one, where it was scrolled to.
func (v *View) Back() {
	if len(v.history) == 0 {
		return
	}
	h := v.history[len(v.history)-1]
	b, st, err := readDoc(h.path)
	if err != nil {
		v.note = err.Error()
		return
	}
	v.history = v.history[:len(v.history)-1]
	v.load(h.path, b, st)
	v.scroll, v.note = h.scroll, ""
}

// navigate opens a markdown file a link in this one points to.
func (v *View) navigate(path, frag string) {
	if path == v.path {
		v.anchor, v.moved = frag, true
		return
	}
	b, st, err := readDoc(path)
	if err != nil {
		v.note = "Cannot open " + filepath.Base(path) + ": " + plainErr(err)
		return
	}
	v.history = append(v.history, entry{v.path, v.scroll})
	v.load(path, b, st)
	v.scroll, v.anchor, v.note = 0, frag, ""
}

func (v *View) load(path string, b []byte, st stamp) {
	doc := Parse(b)
	ctx, stop := context.WithCancel(context.Background())
	v.mu.Lock()
	if v.stop != nil {
		v.stop()
	}
	v.path, v.doc, v.stamp, v.ctx, v.stop = path, doc, st, ctx, stop
	v.gen++
	v.moved = true
	for k, p := range v.imgs {
		if !p.done {
			delete(v.imgs, k) // its load was cancelled
		}
	}
	v.mu.Unlock()
	go v.watch(ctx, path)
}

func readDoc(path string) ([]byte, stamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, stamp{}, err
	}
	b, err := readCapped(path, MaxDocBytes)
	if err != nil {
		return nil, stamp{}, err
	}
	return b, stamp{fi.ModTime(), fi.Size()}, nil
}

func (v *View) watch(ctx context.Context, path string) {
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if v.reload(ctx, path) && v.Invalidate != nil {
			v.Invalidate()
		}
	}
}

// reload reads path again when its time or size changed since the last
// read, keeping the scroll position. Local images are read again too.
// An editor that saves by renaming leaves no file for a moment; the next
// poll finds the new one.
func (v *View) reload(ctx context.Context, path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	v.mu.Lock()
	same := v.stamp == stamp{fi.ModTime(), fi.Size()}
	v.mu.Unlock()
	if same {
		return false
	}
	b, st, err := readDoc(path)
	if err != nil {
		return false
	}
	doc := Parse(b)
	v.mu.Lock()
	defer v.mu.Unlock()
	if ctx.Err() != nil || v.path != path {
		return false
	}
	v.doc, v.stamp = doc, st
	v.gen++
	for k, p := range v.imgs {
		if !p.url {
			delete(v.imgs, k)
		}
	}
	return true
}

// picture is the image at w, loading it the first time.
func (v *View) picture(w Where, svgWidth int) *picture {
	key := w.Path + w.URL
	v.mu.Lock()
	defer v.mu.Unlock()
	if p := v.imgs[key]; p != nil {
		return p
	}
	if v.imgs == nil {
		v.imgs = map[string]*picture{}
	}
	p := &picture{url: w.URL != ""}
	v.imgs[key] = p
	ctx := v.ctx
	go func() {
		img, err := LoadImage(ctx, w, svgWidth)
		v.mu.Lock()
		if err != nil {
			p.err = plainErr(err)
		} else {
			p.img, p.size = paint.NewImageOp(img), img.Bounds().Size()
		}
		p.done = true
		v.imgGen++
		v.mu.Unlock()
		if v.Invalidate != nil {
			v.Invalidate()
		}
	}()
	return p
}

// plainErr is err without the path an os error repeats.
func plainErr(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		err = pe.Err
	}
	return err.Error()
}

// Keys runs the reader's keys. The app calls it before its own
// shortcuts, as the review view's.
func (v *View) Keys(gtx gl.Context) {
	if !v.on {
		return
	}
	// why: Gio gives key focus only to a tag with a FocusFilter.
	filters := []event.Filter{key.FocusFilter{Target: &v.tag}, key.Filter{Name: key.NameEscape}}
	for _, n := range []key.Name{"J", "K", key.NameUpArrow, key.NameDownArrow, key.NamePageUp, key.NamePageDown, key.NameHome, key.NameEnd, key.NameDeleteBackward} {
		filters = append(filters, key.Filter{Focus: &v.tag, Name: n})
	}
	filters = append(filters,
		key.Filter{Focus: &v.tag, Name: key.NameSpace, Optional: key.ModShift},
		key.Filter{Focus: &v.tag, Name: "G", Optional: key.ModShift})
	line := gtx.Dp(40)
	page := max(v.viewH-gtx.Dp(48), line)
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch e.Name {
		case key.NameEscape:
			v.Close()
			return
		case "J", key.NameDownArrow:
			v.scroll += line
		case "K", key.NameUpArrow:
			v.scroll -= line
		case key.NamePageDown:
			v.scroll += page
		case key.NamePageUp:
			v.scroll -= page
		case key.NameSpace:
			if e.Modifiers.Contain(key.ModShift) {
				v.scroll -= page
			} else {
				v.scroll += page
			}
		case key.NameHome:
			v.scroll = 0
		case key.NameEnd:
			v.scroll = math.MaxInt32
		case "G":
			v.scroll = 0
			if e.Modifiers.Contain(key.ModShift) {
				v.scroll = math.MaxInt32
			}
		case key.NameDeleteBackward:
			v.Back()
		}
	}
}

// Layout draws the reader over gtx's whole area.
func (v *View) Layout(gtx gl.Context, th *theme.Theme) {
	if !v.on {
		return
	}
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: size}.Op())
	hh := gtx.Dp(44)
	v.body(gtx, th, image.Rect(0, hh+1, size.X, size.Y))
	v.header(gtx, th, image.Rect(0, 0, size.X, hh))
	if !v.on {
		return // the close button
	}
	if v.moved {
		v.moved = false
		gtx.Execute(op.InvalidateCmd{}) // drawn with the old file this frame
	}
	if v.focus {
		v.focus = false
		// why: a click that opened the reader came after Keys polled
		// this frame, and Gio focuses only a tag polled for focus.
		for {
			if _, ok := gtx.Event(key.FocusFilter{Target: &v.tag}); !ok {
				break
			}
		}
		gtx.Execute(key.FocusCmd{Tag: &v.tag})
	}
}

func (v *View) header(gtx gl.Context, th *theme.Theme, r image.Rectangle) {
	for v.closeBtn.Clicked(gtx) {
		v.Close()
		return
	}
	for v.backBtn.Clicked(gtx) {
		v.Back()
	}
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect(r).Op())
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(r.Min.X, r.Max.Y), Max: image.Pt(r.Max.X, r.Max.Y+1)}.Op())
	icon := func(name string) func(gtx gl.Context, c color.NRGBA, size int) {
		return func(gtx gl.Context, c color.NRGBA, size int) { sidebar.Icon(gtx, name, size, c) }
	}
	pad := gtx.Dp(theme.SpaceS)
	cgtx := gtx
	cgtx.Constraints = gl.Constraints{Max: r.Size()}
	// The close button, right.
	m := op.Record(gtx.Ops)
	cd := kit.IconButton(cgtx, th, &v.closeBtn, kit.Ghost, kit.Medium, icon("x"))
	call := m.Stop()
	t := op.Offset(image.Pt(r.Max.X-pad-cd.Size.X, r.Min.Y+(r.Dy()-cd.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	t.Pop()
	x := r.Min.X + pad
	if len(v.history) > 0 {
		m := op.Record(gtx.Ops)
		bd := kit.IconButton(cgtx, th, &v.backBtn, kit.Ghost, kit.Medium, icon("chevron-left"))
		call := m.Stop()
		t := op.Offset(image.Pt(x, r.Min.Y+(r.Dy()-bd.Size.Y)/2)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		t.Pop()
		x += bd.Size.X + gtx.Dp(theme.SpaceXS)
	} else {
		x += gtx.Dp(theme.SpaceXS)
	}
	maxX := r.Max.X - 2*pad - cd.Size.X
	name, dir := filepath.Base(v.path), tildeDir(filepath.Dir(v.path))
	x += label(gtx, th, image.Pt(x, r.Min.Y), r.Dy(), maxX-x, semibold(th.UIFont), th.Sp(theme.Large), th.Fg, name) + gtx.Dp(theme.SpaceS)
	if v.note != "" {
		label(gtx, th, image.Pt(x, r.Min.Y), r.Dy(), maxX-x, th.UIFont, th.Sp(theme.Body), th.Red, v.note)
	} else {
		label(gtx, th, image.Pt(x, r.Min.Y), r.Dy(), maxX-x, th.UIFont, th.Sp(theme.Body), th.TextQuiet, dir)
	}
}

// tildeDir is dir with the home folder as ~.
func tildeDir(dir string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, dir); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "~"
			}
			return filepath.Join("~", rel)
		}
	}
	return dir
}

// label draws one line of s at x, centered in a row h high, cut to maxW;
// it returns its width.
func label(gtx gl.Context, th *theme.Theme, at image.Point, h, maxW int, f font.Font, size unit.Sp, c color.NRGBA, s string) int {
	if maxW <= 0 {
		return 0
	}
	lg := gtx
	lg.Constraints = gl.Constraints{Max: image.Pt(maxW, h)}
	m := op.Record(gtx.Ops)
	d := widget.Label{MaxLines: 1, Truncator: "…"}.Layout(lg, th.Shaper, f, size, s, colorCall(gtx, c))
	call := m.Stop()
	t := op.Offset(image.Pt(at.X, at.Y+(h-d.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	t.Pop()
	return d.Size.X
}

func semibold(f font.Font) font.Font { f.Weight = font.SemiBold; return f }

func colorCall(gtx gl.Context, c color.NRGBA) op.CallOp {
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: c}.Add(gtx.Ops)
	return m.Stop()
}

// bodySize is the reading text's size: the type scale's Title step, 16px
// at the default UI text size.
func bodySize(th *theme.Theme) unit.Sp { return th.Sp(theme.Title) }

func (v *View) body(gtx gl.Context, th *theme.Theme, area image.Rectangle) {
	v.mu.Lock()
	doc, gen, imgGen := v.doc, v.gen, v.imgGen
	v.mu.Unlock()
	if v.mono == 0 || v.measured.th != th {
		v.mono = MonoScale(th)
	}
	if v.textsGen != gen {
		v.texts, v.clicks, v.textsGen = map[int]*richtext.InteractiveText{}, map[int]*widget.Clickable{}, gen
	}
	body := gtx.Sp(bodySize(th))
	side := gtx.Dp(theme.SpaceXL)
	col := min(area.Dx()-2*side, body*36) // about 72 characters
	if col < gtx.Dp(120) {
		return
	}
	if m := (measure{col, gen, imgGen, th}); m != v.measured || len(v.heights) != len(doc) {
		v.measured = m
		v.heights = v.heights[:0]
		mgtx := gtx.Disabled()
		for i := range doc {
			v.scratch.Reset()
			mgtx.Ops = &v.scratch
			mgtx.Constraints = gl.Constraints{Max: image.Pt(col, 1<<24)}
			v.heights = append(v.heights, v.block(mgtx, th, i, &doc[i]).Size.Y)
		}
	}
	top, bottom := gtx.Dp(theme.SpaceXL), gtx.Dp(64)
	ys := make([]int, len(doc)+1) // each block's top in the document
	y := top
	for i := range doc {
		ys[i] = y
		y += v.heights[i] + gap(doc, i, body)
	}
	ys[len(doc)] = y
	total := y + bottom
	v.viewH = area.Dy()
	maxScroll := max(total-area.Dy(), 0)
	if v.anchor != "" {
		for i := range doc {
			if doc[i].Kind == Heading && doc[i].Slug == strings.ToLower(v.anchor) {
				v.scroll = ys[i] - top
				break
			}
		}
		v.anchor = ""
	}

	defer clip.Rect(area).Push(gtx.Ops).Pop()
	v.scroll = min(max(v.scroll, 0), maxScroll)
	d := v.scr.Update(gtx.Metric, gtx.Source, gtx.Now, gesture.Vertical, pointer.ScrollRange{}, pointer.ScrollRange{Min: -v.scroll, Max: maxScroll - v.scroll})
	v.scroll = min(max(v.scroll+d, 0), maxScroll)
	v.scr.Add(gtx.Ops)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &v.tag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if _, ok := ev.(pointer.Event); ok {
			gtx.Execute(key.FocusCmd{Tag: &v.tag})
		}
	}
	event.Op(gtx.Ops, &v.tag)

	x0 := area.Min.X + (area.Dx()-col)/2
	for i := range doc {
		y := area.Min.Y + ys[i] - v.scroll
		next := area.Min.Y + ys[i+1] - v.scroll
		if next < area.Min.Y || y > area.Max.Y {
			continue
		}
		v.quoteBars(gtx, th, doc, i, image.Pt(x0, y), next-y)
		t := op.Offset(image.Pt(x0, y)).Push(gtx.Ops)
		bgtx := gtx
		bgtx.Constraints = gl.Constraints{Max: image.Pt(col, 1<<24)}
		v.block(bgtx, th, i, &doc[i])
		t.Pop()
	}
	if len(doc) == 0 {
		label(gtx, th, image.Pt(x0, area.Min.Y+top), body*2, col, th.UIFont, bodySize(th), th.TextQuiet, "This file is empty.")
	}
	v.scrollbar(gtx, th, area, total, maxScroll)
}

// gap is the space after block i.
func gap(doc []Block, i, body int) int {
	if i+1 >= len(doc) {
		return 0
	}
	b, n := &doc[i], &doc[i+1]
	switch {
	case n.Kind == Heading && n.Level <= 2:
		return body * 2
	case n.Kind == Heading:
		return body * 3 / 2
	case b.Kind == Heading:
		return body * 3 / 4
	case b.Indent > 0 && n.Indent > 0 && n.Kind == Para && b.Kind == Para:
		return body / 4
	}
	return body
}

// quoteBars draws the block quote bars beside block i, joined to the
// next block's when it is in the same quote.
func (v *View) quoteBars(gtx gl.Context, th *theme.Theme, doc []Block, i int, at image.Point, h int) {
	for q := 1; q <= doc[i].Quote; q++ {
		hh := v.heights[i]
		if i+1 < len(doc) && doc[i+1].Quote >= q {
			hh = h
		}
		x := at.X + (q-1)*gtx.Dp(16)
		paint.FillShape(gtx.Ops, th.BorderStrong, clip.Rect{Min: image.Pt(x, at.Y), Max: image.Pt(x+gtx.Dp(3), at.Y+hh)}.Op())
	}
}

func (v *View) scrollbar(gtx gl.Context, th *theme.Theme, area image.Rectangle, total, maxScroll int) {
	if maxScroll <= 0 {
		return
	}
	w, in := gtx.Dp(6), gtx.Dp(3)
	track := area.Dy() - 2*in
	thumb := min(track, max(gtx.Dp(32), track*area.Dy()/total))
	free := max(track-thumb, 1)
	strip := image.Rect(area.Max.X-gtx.Dp(14), area.Min.Y, area.Max.X, area.Max.Y)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &v.bar, Kinds: pointer.Press | pointer.Drag})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok {
			p := int(e.Position.Y) - area.Min.Y - in - thumb/2
			v.scroll = min(max(p*maxScroll/free, 0), maxScroll)
		}
	}
	top := area.Min.Y + in + free*v.scroll/maxScroll
	r := image.Rect(area.Max.X-in-w, top, area.Max.X-in, top+thumb)
	paint.FillShape(gtx.Ops, theme.Mix(th.Bg, th.Fg, 0.22), clip.UniformRRect(r, w/2).Op(gtx.Ops))
	cl := clip.Rect(strip).Push(gtx.Ops)
	event.Op(gtx.Ops, &v.bar)
	pointer.CursorDefault.Add(gtx.Ops)
	cl.Pop()
}

// indent is how far block b sits right of the column's edge.
func indent(gtx gl.Context, b *Block) int {
	return b.Quote*gtx.Dp(16) + b.Indent*gtx.Dp(24)
}

// block draws block i at the origin, gtx.Constraints.Max.X wide, and
// returns its size.
func (v *View) block(gtx gl.Context, th *theme.Theme, i int, b *Block) gl.Dimensions {
	in := indent(gtx, b)
	w := gtx.Constraints.Max.X - in
	if w <= 0 {
		return gl.Dimensions{}
	}
	t := op.Offset(image.Pt(in, 0)).Push(gtx.Ops)
	defer t.Pop()
	gtx.Constraints = gl.Constraints{Max: image.Pt(w, 1<<24)}
	body := bodySize(th)
	fg := th.Fg
	if b.Quote > 0 {
		fg = th.Muted
	}
	var d gl.Dimensions
	switch b.Kind {
	case Para:
		d = v.text(gtx, th, i*1024, b.Spans, body, fg, false)
		v.marker(gtx, th, b, d.Size.Y)
	case Heading:
		scale := map[int]float32{1: 1.75, 2: 1.45, 3: 1.2, 4: 1.05}[b.Level]
		c := fg
		if scale == 0 {
			scale = 1
			if b.Level == 6 {
				c = th.Muted
			}
		}
		d = v.text(gtx, th, i*1024, b.Spans, body*unit.Sp(scale), c, true)
		if b.Level <= 2 {
			y := d.Size.Y + gtx.Dp(theme.SpaceS)
			paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, y), Max: image.Pt(w, y+1)}.Op())
			d.Size.Y = y + 1
		}
	case Code:
		d = v.code(gtx, th, b.Text)
	case Mermaid:
		if b.Graph != nil {
			d = v.graph(gtx, th, b.Graph)
			break
		}
		nh := gtx.Sp(th.Sp(theme.Body)) * 3 / 2
		label(gtx, th, image.Point{}, nh, w, th.UIFont, th.Sp(theme.Body), th.TextQuiet, b.Note)
		ct := op.Offset(image.Pt(0, nh)).Push(gtx.Ops)
		cd := v.code(gtx, th, b.Text)
		ct.Pop()
		d = gl.Dimensions{Size: image.Pt(w, nh+cd.Size.Y)}
	case Table:
		d = v.table(gtx, th, i, b)
	case Rule:
		h := gtx.Dp(theme.SpaceS)
		paint.FillShape(gtx.Ops, th.BorderStrong, clip.Rect{Min: image.Pt(0, h), Max: image.Pt(w, h+1)}.Op())
		d.Size = image.Pt(w, 2*h+1)
	case Images:
		d = v.images(gtx, th, i, b)
	}
	d.Size.X = w
	return d
}

// marker draws a list item's bullet, number or task box left of it.
func (v *View) marker(gtx gl.Context, th *theme.Theme, b *Block, h int) {
	if b.Marker == "" {
		return
	}
	gw := gtx.Dp(22)
	line := gtx.Sp(bodySize(th)) * 7 / 5
	if b.Task != 0 {
		s := gtx.Dp(14)
		r := image.Rect(-gw+gtx.Dp(2), (line-s)/2, -gw+gtx.Dp(2)+s, (line-s)/2+s)
		rr := clip.UniformRRect(r, gtx.Dp(3))
		if b.Task == 2 {
			paint.FillShape(gtx.Ops, th.Primary, rr.Op(gtx.Ops))
			t := op.Offset(r.Min.Add(image.Pt(gtx.Dp(1), gtx.Dp(1)))).Push(gtx.Ops)
			sidebar.Icon(gtx, "check", s-gtx.Dp(2), th.OnPrimary)
			t.Pop()
		} else {
			paint.FillShape(gtx.Ops, th.Muted, clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(gtx.Dp(1.5))}.Op())
		}
		return
	}
	lg := gtx
	lg.Constraints = gl.Constraints{Min: image.Pt(gw-gtx.Dp(6), 0), Max: image.Pt(gw-gtx.Dp(6), line)}
	t := op.Offset(image.Pt(-gw, 0)).Push(gtx.Ops)
	widget.Label{Alignment: text.End, MaxLines: 1, LineHeightScale: 1.4}.Layout(lg, th.Shaper, th.UIFont, bodySize(th), b.Marker, colorCall(gtx, th.Muted))
	t.Pop()
}

// text draws spans wrapped to the width, taking link clicks; key names
// its click state.
func (v *View) text(gtx gl.Context, th *theme.Theme, key int, spans []Span, size unit.Sp, c color.NRGBA, heading bool) gl.Dimensions {
	st := v.texts[key]
	if st == nil {
		st = &richtext.InteractiveText{}
		if v.texts != nil {
			v.texts[key] = st
		}
	}
	for {
		span, ev, ok := st.Update(gtx)
		if !ok {
			break
		}
		if ev.Type == richtext.Click {
			if u, _ := span.Get("url").(string); u != "" {
				v.follow(u)
			}
		}
	}
	rs := v.rich(th, spans, size, c, heading)
	return richtext.TextStyle{State: st, Styles: rs, Shaper: th.Shaper, LineHeightScale: 1.4}.Layout(gtx)
}

// rich is spans styled for the reader.
func (v *View) rich(th *theme.Theme, spans []Span, size unit.Sp, c color.NRGBA, heading bool) []richtext.SpanStyle {
	link := th.Readable(th.Blue, th.Bg)
	code := th.Readable(theme.Mix(th.Fg, th.Purple, 0.45), th.Bg)
	out := make([]richtext.SpanStyle, 0, len(spans))
	for _, s := range spans {
		f, sz, col := th.UIFont, size, c
		if heading {
			f.Weight = font.SemiBold
		}
		if s.Bold {
			f.Weight = font.Bold
		}
		if s.Italic {
			f.Style = font.Italic
		}
		if s.Mono {
			f, sz, col = th.MonoFont, size*unit.Sp(v.mono), code
		}
		if s.Strike {
			col = th.TextQuiet
		}
		ss := richtext.SpanStyle{Font: f, Size: sz, Color: col, Content: Detab(s.Text)}
		if s.Link != "" {
			ss.Color, ss.Interactive = link, true
			ss.Set("url", s.Link)
		}
		out = append(out, ss)
	}
	return GluePunct(out)
}

// follow opens a link clicked in the file: a heading in it, another
// markdown file in the reader, anything else as a pane's link opens.
func (v *View) follow(link string) {
	if frag, ok := strings.CutPrefix(link, "#"); ok {
		v.anchor, v.moved = frag, true
		return
	}
	dir := filepath.Dir(v.path)
	if p, frag, ok := LinkPath(link, dir); ok {
		v.navigate(p, frag)
		return
	}
	if !strings.Contains(link, ":") {
		p, _, _ := strings.Cut(link, "#")
		link = FileURL(Resolve(p, dir)) // a file beside this one
	}
	if openable(link) && v.OpenURL != nil {
		v.OpenURL(link)
	}
}

func (v *View) code(gtx gl.Context, th *theme.Theme, s string) gl.Dimensions {
	w := gtx.Constraints.Max.X
	pad := gtx.Dp(theme.SpaceM)
	lg := gtx
	lg.Constraints = gl.Constraints{Max: image.Pt(max(w-2*pad, 1), 1<<24)}
	m := op.Record(gtx.Ops)
	d := widget.Label{LineHeightScale: 1.4}.Layout(lg, th.Shaper, th.MonoFont, bodySize(th)*0.875, Detab(s), colorCall(gtx, th.Fg))
	call := m.Stop()
	r := image.Rect(0, 0, w, d.Size.Y+2*pad)
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.UniformRRect(r, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	t := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	t.Pop()
	return gl.Dimensions{Size: r.Max}
}

func (v *View) table(gtx gl.Context, th *theme.Theme, i int, b *Block) gl.Dimensions {
	w := gtx.Constraints.Max.X
	cols := 0
	for _, r := range b.Rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return gl.Dimensions{}
	}
	pad := gtx.Dp(theme.SpaceS)
	size := bodySize(th) * 0.9375
	// Natural widths, measured without wrapping.
	natural := make([]int, cols)
	mgtx := gtx.Disabled()
	for _, r := range b.Rows {
		for c, cell := range r {
			v.cellOps.Reset()
			mgtx.Ops = &v.cellOps
			mgtx.Constraints = gl.Constraints{Max: image.Pt(1<<20, 1<<20)}
			d := richtext.TextStyle{State: &v.cellText, Styles: v.rich(th, cell, size, th.Fg, false), Shaper: th.Shaper}.Layout(mgtx)
			natural[c] = max(natural[c], d.Size.X+2*pad+1)
		}
	}
	widths := fitColumns(natural, w, gtx.Dp(64))
	tw := 0
	for _, cw := range widths {
		tw += cw
	}
	y := 0
	line := th.BorderSubtle
	for ri, r := range b.Rows {
		h := 0
		var calls []op.CallOp
		var dims []gl.Dimensions
		for c := range cols {
			var cell []Span
			if c < len(r) {
				cell = r[c]
			}
			cg := gtx
			cg.Constraints = gl.Constraints{Min: image.Pt(widths[c]-2*pad, 0), Max: image.Pt(widths[c]-2*pad, 1<<24)}
			al := text.Start
			if c < len(b.Align) {
				al = map[byte]text.Alignment{'c': text.Middle, 'r': text.End}[b.Align[c]]
			}
			m := op.Record(gtx.Ops)
			key := i*1024 + 1 + ri*cols + c
			st := v.texts[key]
			if st == nil {
				st = &richtext.InteractiveText{}
				if v.texts != nil {
					v.texts[key] = st
				}
			}
			for {
				span, ev, ok := st.Update(gtx)
				if !ok {
					break
				}
				if u, _ := span.Get("url").(string); ev.Type == richtext.Click && u != "" {
					v.follow(u)
				}
			}
			d := richtext.TextStyle{State: st, Styles: v.rich(th, cell, size, th.Fg, ri == 0), Shaper: th.Shaper, Alignment: al, LineHeightScale: 1.2}.Layout(cg)
			calls, dims = append(calls, m.Stop()), append(dims, d)
			h = max(h, d.Size.Y)
		}
		rh := h + 2*pad
		if ri == 0 {
			paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.Rect{Min: image.Pt(0, y), Max: image.Pt(tw, y+rh)}.Op())
		}
		x := 0
		for c := range cols {
			t := op.Offset(image.Pt(x+pad, y+pad)).Push(gtx.Ops)
			calls[c].Add(gtx.Ops)
			t.Pop()
			x += widths[c]
		}
		paint.FillShape(gtx.Ops, line, clip.Rect{Min: image.Pt(0, y+rh), Max: image.Pt(tw, y+rh+1)}.Op())
		y += rh + 1
	}
	// The outline and the column lines.
	paint.FillShape(gtx.Ops, line, clip.Stroke{Path: clip.Rect{Max: image.Pt(tw, y)}.Path(), Width: 1}.Op())
	x := 0
	for c := 0; c < cols-1; c++ {
		x += widths[c]
		paint.FillShape(gtx.Ops, line, clip.Rect{Min: image.Pt(x, 0), Max: image.Pt(x+1, y)}.Op())
	}
	return gl.Dimensions{Size: image.Pt(w, y+1)}
}

// fitColumns shares w between columns of the natural widths: each gets
// its natural width when all fit, else the narrow ones keep theirs and
// the rest share what is left, never under least.
func fitColumns(natural []int, w, least int) []int {
	out := make([]int, len(natural))
	sum := 0
	for _, n := range natural {
		sum += n
	}
	if sum <= w {
		copy(out, natural)
		return out
	}
	left, open := w, len(natural)
	fixed := make([]bool, len(natural))
	for changed := true; changed && open > 0; {
		changed = false
		share := left / open
		for c, n := range natural {
			if !fixed[c] && n <= share {
				out[c], fixed[c] = n, true
				left -= n
				open--
				changed = true
			}
		}
	}
	for c := range natural {
		if !fixed[c] {
			out[c] = max(left/max(open, 1), least)
		}
	}
	return out
}

func (v *View) images(gtx gl.Context, th *theme.Theme, i int, b *Block) gl.Dimensions {
	w := gtx.Constraints.Max.X
	sp := gtx.Dp(theme.SpaceS)
	x, y, rowH := 0, 0, 0
	dir := filepath.Dir(v.path)
	for k, im := range b.Imgs {
		where, err := ResolveImage(im.Src, dir)
		var p *picture
		if err == nil {
			p = v.picture(where, w)
		}
		v.mu.Lock()
		var ready bool
		var pic picture
		if p != nil {
			pic, ready = *p, p.done && p.err == ""
		}
		v.mu.Unlock()
		var sz image.Point
		var draw func(at image.Point)
		if ready {
			sz = image.Pt(gtx.Dp(unit.Dp(pic.size.X)), gtx.Dp(unit.Dp(pic.size.Y)))
			if sz.X > w {
				sz = image.Pt(w, sz.Y*w/sz.X)
			}
			draw = func(at image.Point) {
				t := op.Offset(at).Push(gtx.Ops)
				cl := clip.Rect{Max: sz}.Push(gtx.Ops)
				s := float32(sz.X) / float32(pic.size.X)
				a := op.Affine(f32.Affine2D{}.Scale(f32.Point{}, f32.Pt(s, s))).Push(gtx.Ops)
				pic.img.Add(gtx.Ops)
				paint.PaintOp{}.Add(gtx.Ops)
				a.Pop()
				cl.Pop()
				t.Pop()
			}
		} else {
			name := im.Alt
			if name == "" {
				name = filepath.Base(im.Src)
			}
			s := "Loading " + name + "…"
			switch {
			case err != nil:
				s = fmt.Sprintf("Image %s not shown: %s", name, plainErr(err))
			case pic.done:
				s = fmt.Sprintf("Image %s not shown (%s): %s", name, im.Src, pic.err)
			}
			lg := gtx
			lg.Constraints = gl.Constraints{Max: image.Pt(w, 1<<24)}
			m := op.Record(gtx.Ops)
			d := widget.Label{}.Layout(lg, th.Shaper, th.UIFont, th.Sp(theme.Body), s, colorCall(gtx, th.TextQuiet))
			call := m.Stop()
			sz = d.Size
			draw = func(at image.Point) {
				t := op.Offset(at).Push(gtx.Ops)
				call.Add(gtx.Ops)
				t.Pop()
			}
		}
		if x > 0 && x+sz.X > w {
			x, y, rowH = 0, y+rowH+sp, 0
		}
		at := image.Pt(x, y)
		draw(at)
		if im.Link != "" {
			c := v.clicks[i*1024+k]
			if c == nil {
				c = &widget.Clickable{}
				if v.clicks != nil {
					v.clicks[i*1024+k] = c
				}
			}
			for c.Clicked(gtx) {
				v.follow(im.Link)
			}
			t := op.Offset(at).Push(gtx.Ops)
			cg := gtx
			cg.Constraints = gl.Exact(sz)
			c.Layout(cg, func(gtx gl.Context) gl.Dimensions {
				pointer.CursorPointer.Add(gtx.Ops)
				return gl.Dimensions{Size: sz}
			})
			t.Pop()
		}
		x += sz.X + sp
		rowH = max(rowH, sz.Y)
	}
	return gl.Dimensions{Size: image.Pt(w, y+rowH)}
}

// graph draws a flowchart on a panel, scaled down to the width.
func (v *View) graph(gtx gl.Context, th *theme.Theme, g *Graph) gl.Dimensions {
	w := gtx.Constraints.Max.X
	pad := gtx.Dp(theme.SpaceL)
	fs := th.Sp(theme.Body)
	panel := th.SurfaceSecondary
	fill := theme.Mix(panel, th.Primary, 0.14)
	ring := theme.Mix(panel, th.Primary, 0.65)
	edge := th.Muted

	lgtx := gtx
	lgtx.Constraints = gl.Constraints{Max: image.Pt(gtx.Dp(180), 1<<20)}
	n := len(g.Nodes)
	calls := make([]op.CallOp, n)
	texts := make([]image.Point, n)
	sizes := make([]image.Point, n)
	px, py := gtx.Dp(14), gtx.Dp(8)
	for k, nd := range g.Nodes {
		m := op.Record(gtx.Ops)
		d := widget.Label{Alignment: text.Middle}.Layout(lgtx, th.Shaper, th.UIFont, fs, nd.Label, colorCall(gtx, th.Fg))
		calls[k], texts[k] = m.Stop(), d.Size
		s := d.Size.Add(image.Pt(2*px, 2*py))
		switch nd.Shape {
		case Diamond:
			s = image.Pt(d.Size.X+2*d.Size.Y+2*px, 5*d.Size.Y/2+2*py)
		case Circle:
			side := max(s.X, s.Y)
			s = image.Pt(side, side)
		case Stadium:
			s.X += s.Y / 2
		}
		sizes[k] = s
	}
	elabels := make([]op.CallOp, len(g.Edges))
	esizes := make([]image.Point, len(g.Edges))
	widest := 0
	for k, e := range g.Edges {
		if e.Label == "" {
			continue
		}
		m := op.Record(gtx.Ops)
		d := widget.Label{Alignment: text.Middle}.Layout(lgtx, th.Shaper, th.UIFont, th.Sp(theme.Small), e.Label, colorCall(gtx, th.Muted))
		elabels[k], esizes[k] = m.Stop(), d.Size
		widest = max(widest, d.Size.X)
	}
	rank := gtx.Dp(44)
	if widest > 0 {
		if g.Dir == "LR" || g.Dir == "RL" {
			rank = max(rank, widest+gtx.Dp(32))
		} else {
			rank = max(rank, 3*gtx.Sp(th.Sp(theme.Small))+gtx.Dp(24))
		}
	}
	pl := g.Layout(sizes, gtx.Dp(28), rank)
	scale := min(float32(1), float32(w-2*pad)/float32(max(pl.Size.X, 1)))
	dw, dh := int(float32(pl.Size.X)*scale), int(float32(pl.Size.Y)*scale)
	r := image.Rect(0, 0, w, dh+2*pad)
	paint.FillShape(gtx.Ops, panel, clip.UniformRRect(r, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
	cl := clip.Rect(r).Push(gtx.Ops)
	defer cl.Pop()
	a := op.Affine(f32.Affine2D{}.Scale(f32.Point{}, f32.Pt(scale, scale)).Offset(f32.Pt(float32((w-dw)/2), float32(pad)))).Push(gtx.Ops)
	defer a.Pop()

	stroke := float32(gtx.Dp(1.5))
	head := float32(gtx.Dp(8))
	for k, e := range g.Edges {
		pts := pl.Lines[k]
		if len(pts) < 2 {
			continue
		}
		fp := make([]f32.Point, len(pts))
		for j, p := range pts {
			fp[j] = f32.Pt(float32(p.X), float32(p.Y))
		}
		width := stroke
		if e.Thick {
			width *= 2
		}
		if e.Head {
			tip, from := fp[len(fp)-1], fp[len(fp)-2]
			dir := unitVec(tip.Sub(from))
			fp[len(fp)-1] = tip.Sub(dir.Mul(head * 0.8))
			arrowHead(gtx, edge, tip, dir, head)
		}
		polyline(gtx, edge, fp, width, e.Dotted)
	}
	for k, nd := range g.Nodes {
		nr := pl.Nodes[k]
		shape(gtx, nd.Shape, nr, fill, ring, stroke)
		c := nr.Min.Add(nr.Size().Sub(texts[k]).Div(2))
		t := op.Offset(c).Push(gtx.Ops)
		calls[k].Add(gtx.Ops)
		t.Pop()
	}
	for k := range g.Edges {
		if esizes[k] == (image.Point{}) || len(pl.Lines[k]) < 2 {
			continue
		}
		pts := pl.Lines[k]
		m := len(pts) / 2
		mid := pts[m-1].Add(pts[m]).Div(2)
		if len(pts)%2 == 1 {
			mid = pts[m]
		}
		lr := image.Rectangle{Min: mid.Sub(esizes[k].Div(2)), Max: mid.Sub(esizes[k].Div(2)).Add(esizes[k])}
		paint.FillShape(gtx.Ops, panel, clip.UniformRRect(lr.Inset(-gtx.Dp(3)), gtx.Dp(3)).Op(gtx.Ops))
		t := op.Offset(lr.Min).Push(gtx.Ops)
		elabels[k].Add(gtx.Ops)
		t.Pop()
	}
	return gl.Dimensions{Size: r.Max}
}

func unitVec(p f32.Point) f32.Point {
	l := float32(math.Hypot(float64(p.X), float64(p.Y)))
	if l == 0 {
		return f32.Point{}
	}
	return p.Div(l)
}

func polyline(gtx gl.Context, c color.NRGBA, pts []f32.Point, width float32, dotted bool) {
	var p clip.Path
	p.Begin(gtx.Ops)
	if !dotted {
		p.MoveTo(pts[0])
		for _, q := range pts[1:] {
			p.LineTo(q)
		}
	} else {
		dash, space := width*3, width*2.5
		for k := 1; k < len(pts); k++ {
			a, b := pts[k-1], pts[k]
			d := unitVec(b.Sub(a))
			l := float32(math.Hypot(float64(b.X-a.X), float64(b.Y-a.Y)))
			for s := float32(0); s < l; s += dash + space {
				p.MoveTo(a.Add(d.Mul(s)))
				p.LineTo(a.Add(d.Mul(min(s+dash, l))))
			}
		}
	}
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: p.End(), Width: width}.Op())
}

func arrowHead(gtx gl.Context, c color.NRGBA, tip, dir f32.Point, size float32) {
	n := f32.Pt(-dir.Y, dir.X)
	base := tip.Sub(dir.Mul(size))
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(tip)
	p.LineTo(base.Add(n.Mul(size / 2)))
	p.LineTo(base.Sub(n.Mul(size / 2)))
	p.Close()
	paint.FillShape(gtx.Ops, c, clip.Outline{Path: p.End()}.Op())
}

func shape(gtx gl.Context, s Shape, r image.Rectangle, fill, ring color.NRGBA, width float32) {
	var path clip.PathSpec
	switch s {
	case Diamond:
		c := r.Min.Add(r.Max).Div(2)
		var p clip.Path
		p.Begin(gtx.Ops)
		p.MoveTo(f32.Pt(float32(c.X), float32(r.Min.Y)))
		p.LineTo(f32.Pt(float32(r.Max.X), float32(c.Y)))
		p.LineTo(f32.Pt(float32(c.X), float32(r.Max.Y)))
		p.LineTo(f32.Pt(float32(r.Min.X), float32(c.Y)))
		p.Close()
		path = p.End()
	case Circle:
		path = clip.Ellipse(r).Path(gtx.Ops)
	default:
		rad := gtx.Dp(4)
		switch s {
		case Round:
			rad = gtx.Dp(12)
		case Stadium:
			rad = r.Dy() / 2
		}
		path = clip.UniformRRect(r, rad).Path(gtx.Ops)
	}
	paint.FillShape(gtx.Ops, fill, clip.Outline{Path: path}.Op())
	paint.FillShape(gtx.Ops, ring, clip.Stroke{Path: path, Width: width}.Op())
}
