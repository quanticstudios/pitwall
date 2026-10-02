package vt

import (
	"fmt"
	"image/color"
	"io"
	"runtime"
	"sync"
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
	last      *Grid // the previous Snapshot, shown while syncing
}

// New returns an Emulator backed by github.com/charmbracelet/x/vt.
func New(cols, rows int, reply io.Writer) Emulator {
	if reply == nil {
		reply = io.Discard
	}
	e := xvt.NewEmulator(max(cols, 1), max(rows, 1))
	// Nothing reads scrollback yet, and x/vt's default keeps 10k lines (up to
	// 134MB per pane at 120 columns). Raise this when a scrollback view lands.
	e.SetScrollbackSize(1)
	st := &state{modes: map[ansi.Mode]bool{}}
	e.SetCallbacks(xvt.Callbacks{
		Title:            func(s string) { st.title = s },
		CursorVisibility: func(v bool) { st.hidden = !v },
		CursorStyle:      func(s xvt.CursorStyle, _ bool) { st.shape = CursorShape(s) },
		EnableMode: func(m ansi.Mode) {
			if m == modeSync && !st.syncing {
				st.syncStart = time.Now()
			}
			st.modes[m] = true
			st.syncing = st.modes[modeSync]
		},
		DisableMode: func(m ansi.Mode) {
			st.modes[m] = false
			st.syncing = st.modes[modeSync]
		},
	})
	registerKitty(e, st)
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

	go pumpReplies(e, reply)
	t := &emulator{e: e, st: st}
	runtime.AddCleanup(t, func(w io.Writer) { _ = w.(*io.PipeWriter).Close() }, e.InputPipe())
	return t
}

func (t *emulator) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.e.Write(p)
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

// pumpReplies moves x/vt's replies from its pipe to reply through an
// unbounded buffer, so a slow reply writer never stalls Write.
func pumpReplies(src io.Reader, dst io.Writer) {
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
		pending = append(pending, buf[:n]...)
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
