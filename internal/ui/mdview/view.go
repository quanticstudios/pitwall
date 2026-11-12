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

	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// MaxDocBytes is the largest markdown file the reader opens.
const MaxDocBytes = 10 << 20

// pollEvery is how often an open file is checked for changes.
const pollEvery = 500 * time.Millisecond

// maxLoads is how many images load at once. Each may hold MaxPixels
// decoded while it loads.
const maxLoads = 2

// maxHeld is how many bytes of decoded images the reader keeps. Images of
// files read before go first, least recently shown first; an image of
// the file shown that does not fit is not shown.
var maxHeld = 256 << 20

// loadImage loads an image for the reader; tests replace it.
var loadImage = LoadImage

// View is the reader: one markdown file over the panes, with Back
// through the files its links opened.
type View struct {
	// Invalidate asks for a frame. The file watcher and image loads call
	// it from their goroutines.
	Invalidate func()
	// OpenURL opens a link that is not a markdown file on this machine.
	OpenURL func(string)
	// Fetch, when set, reads the files the reader shows, and their images
	// that are not on the web, in place of this machine's disk: the phone
	// reads them from the computer. It runs off the window's goroutine.
	// Paths stay as the links wrote them, ~/ too, for Fetch to resolve,
	// and no file is watched for changes.
	Fetch func(ctx context.Context, path string) ([]byte, error)
	// Size is the text's size; zero is the type scale's Title step.
	Size unit.Sp

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
	held   int       // bytes of decoded images in imgs
	visit  int       // counts the files shown, for picture.used
	queue  []loadJob // images waiting for a loader
	loads  int       // loader goroutines running
	// With Fetch: the file is on its way, or failed says why it did not
	// come.
	loading bool
	failed  string

	// The UI goroutine's alone.
	d        Doc
	docGen   int // the gen d shows
	heights  []int
	measured measure
	closeBtn widget.Clickable
	backBtn  widget.Clickable
	tag      int // takes the reader's keys and presses
	focus    bool
	scr      gesture.Scroll
	bar      int    // the scrollbar's strip
	scratch  op.Ops // measures block heights
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
	img   paint.ImageOp
	size  image.Point
	err   string
	done  bool
	url   bool
	bytes int // decoded, counted in View.held
	used  int // the View.visit it was last asked for in
}

type loadJob struct {
	ctx      context.Context
	w        Where
	svgWidth int
	p        *picture
}

// On reports whether the reader is open.
func (v *View) On() bool { return v.on }

// Path is the file the reader shows.
func (v *View) Path() string { return v.path }

// HasBack reports whether Back has a file to go back to.
func (v *View) HasBack() bool { return len(v.history) > 0 }

// Open shows the markdown file at path from its heading frag, or its
// top for "", with no history to go back through. With Fetch it opens at
// once and the file follows.
func (v *View) Open(path, frag string) error {
	if v.Fetch != nil {
		v.history, v.note = nil, ""
		v.on, v.focus = true, true
		v.fetch(path, frag, 0)
		return nil
	}
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
	v.doc, v.imgs, v.stop, v.queue, v.held = nil, nil, nil, nil, 0
	v.mu.Unlock()
}

// Back shows the file before this one, where it was scrolled to.
func (v *View) Back() {
	if len(v.history) == 0 {
		return
	}
	h := v.history[len(v.history)-1]
	if v.Fetch != nil {
		v.history = v.history[:len(v.history)-1]
		v.fetch(h.path, "", h.scroll)
		return
	}
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
	if v.Fetch != nil {
		v.history = append(v.history, entry{v.path, v.scroll})
		v.fetch(path, frag, 0)
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
	v.visit++
	v.moved = true
	for k, p := range v.imgs {
		if !p.done || p.err != "" {
			delete(v.imgs, k) // its load was cancelled, or it gets another try
		}
	}
	v.queue = nil
	v.mu.Unlock()
	go v.watch(ctx, path)
}

// fetch shows path through Fetch, scrolled to scroll or to the heading
// frag once it comes.
func (v *View) fetch(path, frag string, scroll int) {
	ctx, stop := context.WithCancel(context.Background())
	v.mu.Lock()
	if v.stop != nil {
		v.stop()
	}
	v.path, v.doc, v.ctx, v.stop, v.loading, v.failed = path, nil, ctx, stop, true, ""
	v.gen++
	v.visit++
	v.moved = true
	for k, p := range v.imgs {
		if !p.done || p.err != "" {
			delete(v.imgs, k)
		}
	}
	v.queue = nil
	v.mu.Unlock()
	v.scroll, v.anchor, v.note = scroll, frag, ""
	go func() {
		b, err := v.Fetch(ctx, path)
		var doc []Block
		if err == nil {
			doc = Parse(b)
		}
		v.mu.Lock()
		if ctx.Err() != nil { // closed, or another file shown
			v.mu.Unlock()
			return
		}
		v.loading = false
		if err != nil {
			v.failed = plainErr(err)
		} else {
			v.doc = doc
			v.gen++
		}
		v.mu.Unlock()
		if v.Invalidate != nil {
			v.Invalidate()
		}
	}()
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
// read, keeping the scroll position. Local images are read again too,
// and remote ones that failed get another try.
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
	v.visit++ // a remote image the file no longer shows can make room
	for k, p := range v.imgs {
		if !p.url || p.err != "" {
			v.held -= p.bytes
			delete(v.imgs, k)
		}
	}
	q := v.queue[:0]
	for _, j := range v.queue {
		if v.imgs[j.w.Path+j.w.URL] == j.p {
			q = append(q, j)
		}
	}
	v.queue = q
	return true
}

// picture is the image at w, loading it the first time.
func (v *View) picture(w Where, svgWidth int) *picture {
	key := w.Path + w.URL
	v.mu.Lock()
	defer v.mu.Unlock()
	if p := v.imgs[key]; p != nil {
		p.used = v.visit
		return p
	}
	if v.imgs == nil {
		v.imgs = map[string]*picture{}
	}
	p := &picture{url: w.URL != "", used: v.visit}
	v.imgs[key] = p
	v.queue = append(v.queue, loadJob{v.ctx, w, svgWidth, p})
	if v.loads < maxLoads {
		v.loads++
		go v.loader()
	}
	return p
}

// loader loads the queued images, one at a time, until none is left.
func (v *View) loader() {
	for {
		v.mu.Lock()
		if len(v.queue) == 0 {
			v.loads--
			v.mu.Unlock()
			return
		}
		j := v.queue[0]
		v.queue = v.queue[1:]
		v.mu.Unlock()
		if j.ctx.Err() != nil {
			continue // another file is shown
		}
		img, err := v.loadImg(j.ctx, j.w, j.svgWidth)
		v.mu.Lock()
		if v.imgs[j.w.Path+j.w.URL] != j.p { // Close, load or reload dropped it meanwhile
			v.mu.Unlock()
			continue
		}
		p := j.p
		if err != nil {
			p.err = plainErr(err)
		} else if n := img.Bounds().Dx() * img.Bounds().Dy() * 4; !v.hold(n) {
			p.err = fmt.Sprintf("the reader keeps at most %d MiB of images", maxHeld>>20)
		} else {
			p.img, p.size, p.bytes = paint.NewImageOp(img), img.Bounds().Size(), n
		}
		p.done = true
		v.imgGen++
		v.mu.Unlock()
		if v.Invalidate != nil {
			v.Invalidate()
		}
	}
}

// loadImg loads the image at w, a local one through Fetch when set.
func (v *View) loadImg(ctx context.Context, w Where, svgWidth int) (image.Image, error) {
	if v.Fetch == nil || w.URL != "" {
		return loadImage(ctx, w, svgWidth)
	}
	b, err := v.Fetch(ctx, w.Path)
	if err != nil {
		return nil, err
	}
	return Decode(b, svgWidth)
}

// hold makes room for n more bytes of images, dropping images of files
// shown before, least recently shown first, and reports whether n fits.
// v.mu is held.
func (v *View) hold(n int) bool {
	for v.held+n > maxHeld {
		old, oldest := "", v.visit
		for k, p := range v.imgs {
			if p.bytes > 0 && p.used < oldest {
				old, oldest = k, p.used
			}
		}
		if old == "" {
			return false
		}
		v.held -= v.imgs[old].bytes
		delete(v.imgs, old)
	}
	v.held += n
	return true
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

// Body draws the file over gtx's whole area without the reader's
// header, for the phone, which draws its own.
func (v *View) Body(gtx gl.Context, th *theme.Theme) {
	if !v.on {
		return
	}
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: size}.Op())
	v.body(gtx, th, image.Rectangle{Max: size})
	if v.moved {
		v.moved = false
		gtx.Execute(op.InvalidateCmd{})
	}
}

func (v *View) body(gtx gl.Context, th *theme.Theme, area image.Rectangle) {
	v.mu.Lock()
	doc, gen, imgGen, loading, failed := v.doc, v.gen, v.imgGen, v.loading, v.failed
	v.mu.Unlock()
	if v.docGen != gen || v.d.Picture == nil {
		v.d.Set(doc)
		v.d.Follow, v.d.Picture, v.docGen = v.follow, v.pic, gen
	}
	v.d.Size = v.Size
	v.d.prep(gtx, th)
	body := gtx.Sp(v.d.size(th))
	side := gtx.Dp(theme.SpaceXL)
	col := min(area.Dx()-2*side, body*36) // about 72 characters
	if col < gtx.Dp(120) {
		return
	}
	x0 := area.Min.X + (area.Dx()-col)/2
	top, bottom := gtx.Dp(theme.SpaceXL), gtx.Dp(64)
	if loading || failed != "" {
		// why: the scroll stays as Back set it until the file comes.
		s := "Loading " + filepath.Base(v.path) + "…"
		if failed != "" {
			s = "Cannot open " + filepath.Base(v.path) + ": " + failed
		}
		lg := gtx
		lg.Constraints = gl.Constraints{Max: image.Pt(col, area.Dy())}
		t := op.Offset(image.Pt(x0, area.Min.Y+top)).Push(gtx.Ops)
		widget.Label{}.Layout(lg, th.Shaper, th.UIFont, v.d.size(th), s, colorCall(gtx, th.TextQuiet))
		t.Pop()
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
			v.heights = append(v.heights, v.d.Block(mgtx, th, i).Size.Y)
		}
	}
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

	for i := range doc {
		y := area.Min.Y + ys[i] - v.scroll
		next := area.Min.Y + ys[i+1] - v.scroll
		if next < area.Min.Y || y > area.Max.Y {
			continue
		}
		v.d.bars(gtx, th, i, image.Pt(x0, y), v.heights[i], next-y)
		t := op.Offset(image.Pt(x0, y)).Push(gtx.Ops)
		bgtx := gtx
		bgtx.Constraints = gl.Constraints{Max: image.Pt(col, 1<<24)}
		v.d.Block(bgtx, th, i)
		t.Pop()
	}
	if len(doc) == 0 {
		label(gtx, th, image.Pt(x0, area.Min.Y+top), body*2, col, th.UIFont, v.d.size(th), th.TextQuiet, "This file is empty.")
	}
	v.scrollbar(gtx, th, area, total, maxScroll)
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

// follow opens a link clicked in the file: a heading in it, another
// markdown file in the reader, anything else as a pane's link opens.
func (v *View) follow(link string) {
	if frag, ok := strings.CutPrefix(link, "#"); ok {
		v.anchor, v.moved = frag, true
		return
	}
	dir := filepath.Dir(v.path)
	home := v.Fetch == nil
	if p, frag, ok := linkPath(link, dir, home); ok {
		v.navigate(p, frag)
		return
	}
	if !strings.Contains(link, ":") {
		if !home {
			return // a file beside this one, on the computer
		}
		p, _, _ := strings.Cut(link, "#")
		link = FileURL(Resolve(p, dir))
	}
	if openable(link) && v.OpenURL != nil {
		v.OpenURL(link)
	}
}

// pic is what image im shows, for a block width pixels wide, loading it
// the first time.
func (v *View) pic(im Img, width int) Pic {
	name := imgName(im)
	where, err := resolveImage(im.Src, filepath.Dir(v.path), v.Fetch == nil)
	if err != nil {
		return Pic{Note: fmt.Sprintf("Image %s not shown: %s", name, plainErr(err))}
	}
	p := v.picture(where, width)
	v.mu.Lock()
	defer v.mu.Unlock()
	switch {
	case p.done && p.err == "":
		return Pic{Img: p.img, Size: p.size}
	case p.done:
		return Pic{Note: fmt.Sprintf("Image %s not shown (%s): %s", name, im.Src, p.err)}
	}
	return Pic{Note: "Loading " + name + "…"}
}
