package vt

import (
	"bytes"
	"fmt"
	"image/color"
	"io"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	xvt "github.com/charmbracelet/x/vt"
)

var _ NewFunc = New

// syncTimeout caps how long a synchronized-output frame (mode 2026) may hide
// the live screen. Ghostty uses the same one second.
const syncTimeout = time.Second

const kittyStackMax = 64

// replyCap bounds reply bytes waiting for the PTY. A child that floods
// queries without reading its input cannot use the replies anyway.
const replyCap = 1 << 20

var modeSync = ansi.DECMode(2026)

type emulator struct {
	mu sync.Mutex
	e  *xvt.Emulator
	st *state
}

// state is what the x/vt callbacks and extra handlers record. It is kept
// apart from emulator so nothing the reply goroutine reaches points back at
// emulator, which lets the cleanup in New fire once a pane drops it.
type state struct {
	title     string
	hidden    bool
	shape     CursorShape
	modes     map[ansi.Mode]bool
	kitty     [2][]uint8 // flag stacks for the main and alt screens
	syncing   bool       // mode 2026 is set
	syncStart time.Time
	syncTimer *time.Timer // fires wake when a frame outlives syncTimeout
	wake      func()      // set by SetDirtyFunc
	last      *Grid       // the previous Snapshot, shown while syncing
	hist      history
	term      byte          // the last byte of the chunk x/vt is parsing: BEL or ESC when it ends an OSC
	dropped   atomic.Uint64 // reply bytes dropped at replyCap
}

// New returns an Emulator backed by github.com/charmbracelet/x/vt.
func New(cols, rows int, reply io.Writer) Emulator {
	if reply == nil {
		reply = io.Discard
	}
	e := xvt.NewEmulator(max(cols, 1), max(rows, 1))
	// x/vt's scrollback only collects lines until Write moves them into
	// st.hist, so it never drops one.
	e.SetScrollbackSize(math.MaxInt)
	e.SetDefaultForegroundColor(rgb(DefaultPalette.Fg))
	e.SetDefaultBackgroundColor(rgb(DefaultPalette.Bg))
	e.SetDefaultCursorColor(rgb(DefaultPalette.Cursor))
	st := &state{modes: map[ansi.Mode]bool{}}
	e.SetCallbacks(xvt.Callbacks{
		Title:            func(s string) { st.title = s },
		CursorVisibility: func(v bool) { st.hidden = !v },
		CursorStyle:      func(s xvt.CursorStyle, _ bool) { st.shape = CursorShape(s) },
		EnableMode: func(m ansi.Mode) {
			if m == modeSync && !st.syncing {
				st.syncStart = time.Now()
				st.armSync()
			}
			st.modes[m] = true
			st.syncing = st.modes[modeSync]
		},
		DisableMode: func(m ansi.Mode) {
			if m == modeSync && st.syncTimer != nil {
				st.syncTimer.Stop()
			}
			st.modes[m] = false
			st.syncing = st.modes[modeSync]
		},
	})
	registerKitty(e, st)
	registerColorQueries(e, st)
	e.RegisterCsiHandler('J', func(p ansi.Params) bool {
		if n, _, _ := p.Param(0, 0); n == 3 && !e.IsAltScreen() {
			st.hist.clear()
		}
		return false // x/vt clears the screen and its own scrollback
	})
	e.RegisterCsiHandler('n', func(p ansi.Params) bool {
		// x/vt answers DSR 5 with the DEC form CSI ? 0 n; xterm sends CSI 0 n.
		if n, _, _ := p.Param(0, 0); n != 5 {
			return false
		}
		_, _ = io.WriteString(e.InputPipe(), "\x1b[0n")
		return true
	})
	// Puts 2026 in the mode table so DECRQM reports it as supported (reset)
	// instead of unrecognized.
	_, _ = e.WriteString("\x1b[?2026l")

	go pumpReplies(e, reply, &st.dropped)
	t := &emulator{e: e, st: st}
	runtime.AddCleanup(t, func(w io.Writer) { _ = w.(*io.PipeWriter).Close() }, e.InputPipe())
	return t
}

// SetDirtyFunc sets f to be called when what Snapshot returns changes
// without new input: a synchronized-output frame outlived syncTimeout. Call
// it before the first Write. Panes find it through an interface check, so
// NewFunc stays as it is.
func (t *emulator) SetDirtyFunc(f func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.st.wake = f
}

func (st *state) armSync() {
	switch {
	case st.wake == nil:
	case st.syncTimer == nil:
		st.syncTimer = time.AfterFunc(syncTimeout, st.wake)
	default:
		st.syncTimer.Reset(syncTimeout)
	}
}

// Write cuts p after every BEL and ESC so the OSC handlers know which
// terminator ended a query, then moves lines that scrolled off into history.
func (t *emulator) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexAny(p, "\x07\x1b") + 1
		if i == 0 {
			i = len(p)
		}
		t.st.term = p[i-1]
		if _, err := t.e.Write(p[:i]); err != nil {
			return n - len(p), err
		}
		p = p[i:]
	}
	if sb := t.e.Scrollback(); sb.Len() > 0 {
		lines := sb.Lines()
		for _, l := range lines {
			t.st.hist.push(l)
		}
		clear(lines) // Clear keeps the backing array, which would pin the lines
		sb.Clear()
	}
	return n, nil
}

func (t *emulator) Resize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.e.Resize(max(cols, 1), max(rows, 1))
	t.st.last = nil
}

// Snapshot copies the screen. While a synchronized-output frame is open it
// returns the previous snapshot instead, so a reader never sees half a frame.
// Copying at the frame start would be exact but costs a full grid copy per
// frame the app draws; the previous snapshot is always a complete frame.
// Snapshots share no memory with the emulator; the cached one is shared with
// whoever got it first, which is safe because a Grid is never mutated.
func (t *emulator) Snapshot() Grid {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshot()
}

// SnapshotAt is Snapshot with the view moved off lines up into history:
// history fills the top rows and the live screen shifts down. off is clamped
// to the history length and ignored on the alt screen. The cursor moves with
// the screen and is hidden once it leaves the view.
func (t *emulator) SnapshotAt(off int) Grid {
	t.mu.Lock()
	defer t.mu.Unlock()
	g := t.snapshot()
	h := &t.st.hist
	off = min(off, h.len())
	if off <= 0 || g.AltScreen {
		return g
	}
	out := g
	out.Cells = make([]Cell, len(g.Cells))
	for y := range g.Rows {
		row := out.Cells[y*g.Cols : (y+1)*g.Cols]
		if src := y - off; src >= 0 {
			copy(row, g.Cells[src*g.Cols:])
		} else {
			h.at(h.len() + src).fill(row)
		}
	}
	out.Cursor.Y += off
	if out.Cursor.Y >= g.Rows {
		out.Cursor.Visible = false
	}
	return out
}

// ScrollbackLen is the number of history lines; 0 on the alt screen.
func (t *emulator) ScrollbackLen() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.e.IsAltScreen() {
		return 0
	}
	return t.st.hist.len()
}

// ScrollbackPushed counts every line that ever entered history, so a viewer
// can tell how far the screen moved even once the oldest lines drop out.
func (t *emulator) ScrollbackPushed() uint64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.st.hist.pushed
}

func (t *emulator) snapshot() Grid {
	st := t.st
	if st.syncing && st.last != nil && time.Since(st.syncStart) < syncTimeout {
		return *st.last
	}
	g := snapshot(t.e, st)
	st.last = &g
	return g
}

func (t *emulator) Modes() Modes {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.st.modes
	out := Modes{
		AppCursorKeys:  m[ansi.ModeCursorKeys],
		AppKeypad:      m[ansi.ModeNumericKeypad],
		BracketedPaste: m[ansi.ModeBracketedPaste],
		FocusEvents:    m[ansi.ModeFocusEvent],
		MouseSGR:       m[ansi.ModeMouseExtSgr],
		KittyKeyboard:  top(*kittyStack(t.e, t.st)),
	}
	switch {
	case m[ansi.ModeMouseAnyEvent]:
		out.Mouse = MouseAny
	case m[ansi.ModeMouseButtonEvent]:
		out.Mouse = MouseButton
	case m[ansi.ModeMouseNormal]:
		out.Mouse = MouseNormal
	case m[ansi.ModeMouseX10]:
		out.Mouse = MouseX10
	}
	return out
}

func snapshot(e *xvt.Emulator, st *state) Grid {
	w, h := e.Width(), e.Height()
	g := Grid{Cols: w, Rows: h, Cells: make([]Cell, w*h), Title: st.title, AltScreen: e.IsAltScreen()}
	for y := range h {
		for x := range w {
			c := e.CellAt(x, y)
			if c == nil {
				g.Cells[y*w+x] = Cell{Content: " ", Width: 1}
				continue
			}
			g.Cells[y*w+x] = Cell{
				Content: c.Content,
				Width:   uint8(c.Width),
				FG:      toColor(c.Style.Fg),
				BG:      toColor(c.Style.Bg),
				Attrs:   toAttr(c.Style.Attrs, c.Style.Underline != ansi.UnderlineNone),
			}
		}
	}
	p := e.CursorPosition()
	g.Cursor = Cursor{X: p.X, Y: p.Y, Visible: !st.hidden, Shape: st.shape}
	return g
}

func toColor(c color.Color) Color {
	switch c := c.(type) {
	case nil:
		return 0
	case ansi.BasicColor:
		return PaletteFlag | Color(c)
	case ansi.IndexedColor:
		return PaletteFlag | Color(c)
	}
	r, g, b, _ := c.RGBA()
	return RGBFlag | Color(r>>8)<<16 | Color(g>>8)<<8 | Color(b>>8)
}

func toAttr(a uint8, underline bool) Attr {
	var out Attr
	for _, m := range [...]struct {
		uv uint8
		a  Attr
	}{
		{uv.AttrBold, Bold}, {uv.AttrFaint, Faint}, {uv.AttrItalic, Italic},
		{uv.AttrBlink | uv.AttrRapidBlink, Blink}, {uv.AttrReverse, Reverse},
		{uv.AttrConceal, Invisible}, {uv.AttrStrikethrough, Strike},
	} {
		if a&m.uv != 0 {
			out |= m.a
		}
	}
	if underline {
		out |= Underline
	}
	return out
}

func rgb(c uint32) color.Color {
	return color.RGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xff}
}

// registerColorQueries answers OSC 10/11/12 and OSC 4 queries in xterm's
// form (rgb:rrrr/gggg/bbbb), ending each reply with the terminator the query
// used. x/vt always answers 10-12 with BEL and ignores OSC 4. Anything but a
// pure query falls through to x/vt, which stores OSC 10-12 colors.
func registerColorQueries(e *xvt.Emulator, st *state) {
	reply := func(code string, c color.Color) {
		r, g, b, _ := c.RGBA()
		term := "\x07"
		if st.term == ansi.ESC {
			term = "\x1b\\"
		}
		_, _ = fmt.Fprintf(e.InputPipe(), "\x1b]%s;rgb:%04x/%04x/%04x%s", code, r, g, b, term)
	}
	notQuery := func(a string) bool { return a != "?" }
	for _, cmd := range []int{10, 11, 12} {
		e.RegisterOscHandler(cmd, func(data []byte) bool {
			// OSC 10;?;? asks for 10 and then 11, as in xterm.
			args := strings.Split(string(data), ";")[1:]
			if len(args) == 0 || slices.ContainsFunc(args, notQuery) {
				return false
			}
			for i := range args {
				switch cmd + i {
				case 10:
					reply("10", e.ForegroundColor())
				case 11:
					reply("11", e.BackgroundColor())
				case 12:
					reply("12", e.CursorColor())
				}
			}
			return true
		})
	}
	e.RegisterOscHandler(4, func(data []byte) bool {
		args := strings.Split(string(data), ";")[1:]
		if len(args) == 0 || len(args)%2 != 0 {
			return false
		}
		idx := make([]int, 0, len(args)/2)
		for i := 0; i < len(args); i += 2 {
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 || n > 255 || args[i+1] != "?" {
				return false
			}
			idx = append(idx, n)
		}
		for _, n := range idx {
			c := color.Color(ansi.IndexedColor(n))
			if n < len(DefaultPalette.ANSI) {
				c = rgb(DefaultPalette.ANSI[n])
			}
			reply("4;"+strconv.Itoa(n), c)
		}
		return true
	})
}

func kittyStack(e *xvt.Emulator, st *state) *[]uint8 {
	if e.IsAltScreen() {
		return &st.kitty[1]
	}
	return &st.kitty[0]
}

func top(s []uint8) uint8 {
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}

// registerKitty adds the kitty keyboard protocol flag stack, which x/vt does
// not track: CSI > f u pushes, CSI < n u pops, CSI = f ; m u sets, CSI ? u
// queries. See https://sw.kovidgoyal.net/kitty/keyboard-protocol/.
func registerKitty(e *xvt.Emulator, st *state) {
	e.RegisterCsiHandler(ansi.Command('>', 0, 'u'), func(p ansi.Params) bool {
		f, _, _ := p.Param(0, 0)
		s := kittyStack(e, st)
		if len(*s) >= kittyStackMax {
			*s = (*s)[1:]
		}
		*s = append(*s, uint8(f&0x1f))
		return true
	})
	e.RegisterCsiHandler(ansi.Command('<', 0, 'u'), func(p ansi.Params) bool {
		n, _, _ := p.Param(0, 1)
		s := kittyStack(e, st)
		*s = (*s)[:max(0, len(*s)-n)]
		return true
	})
	e.RegisterCsiHandler(ansi.Command('=', 0, 'u'), func(p ansi.Params) bool {
		f, _, _ := p.Param(0, 0)
		mode, _, _ := p.Param(1, 1)
		s := kittyStack(e, st)
		if len(*s) == 0 {
			*s = append(*s, 0)
		}
		cur := &(*s)[len(*s)-1]
		switch mode {
		case 1:
			*cur = uint8(f & 0x1f)
		case 2:
			*cur |= uint8(f & 0x1f)
		case 3:
			*cur &^= uint8(f & 0x1f)
		}
		return true
	})
	e.RegisterCsiHandler(ansi.Command('?', 0, 'u'), func(ansi.Params) bool {
		_, _ = fmt.Fprintf(e.InputPipe(), "\x1b[?%du", top(*kittyStack(e, st)))
		return true
	})
	e.RegisterEscHandler('c', func() bool {
		st.kitty = [2][]uint8{}
		return false // let x/vt's own RIS run too
	})
}

// pumpReplies moves x/vt's replies from its pipe to reply through a buffer,
// so a slow reply writer never stalls Write. Past replyCap pending bytes it
// drops what x/vt sends and counts it in dropped.
func pumpReplies(src io.Reader, dst io.Writer, dropped *atomic.Uint64) {
	var mu sync.Mutex
	var pending []byte
	wake := make(chan struct{}, 1)
	go func() {
		for range wake {
			mu.Lock()
			p := pending
			pending = nil
			mu.Unlock()
			if len(p) > 0 {
				_, _ = dst.Write(p)
			}
		}
	}()
	buf := make([]byte, 4096)
	for {
		n, err := src.Read(buf)
		mu.Lock()
		if len(pending)+n > replyCap {
			dropped.Add(uint64(n))
		} else {
			pending = append(pending, buf[:n]...)
		}
		mu.Unlock()
		select {
		case wake <- struct{}{}:
		default:
		}
		if err != nil {
			close(wake)
			return
		}
	}
}
