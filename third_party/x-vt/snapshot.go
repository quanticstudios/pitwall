package vt

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"image/color"
	"slices"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

const maxPending = 4 << 20
const maxSnapshotCells = 1 << 20

// These are the concrete colors produced by the parser or used for defaults.
// Gob preserves indexed colors as indices, rather than resolving them to RGB.
func init() {
	for _, c := range []any{ansi.BasicColor(0), ansi.IndexedColor(0), ansi.TrueColor(0), ansi.RGBColor{},
		color.RGBA{}, color.RGBA64{}, color.NRGBA{}, color.NRGBA64{}, color.Gray{}, color.Gray16{},
		color.Alpha{}, color.Alpha16{}, color.CMYK{}} {
		gob.Register(c)
	}
}

type screenState struct {
	Lines         []uv.Line
	Flags         []LineFlags
	Cursor, Saved Cursor
	Scroll        uv.Rectangle
}

type modeState struct {
	Number  int
	ANSI    bool
	Setting ansi.ModeSetting
}

type emulatorState struct {
	Version             uint32
	Width, Height       int
	Screens             [2]screenState
	Alt                 bool
	Colors              [256]color.Color
	Defaults, Overrides [3]color.Color
	Charsets            [4]CharSet
	GL, GR, Single      int
	Modes               []modeState
	Tabs                []int
	LastChar            rune
	Phantom             bool
	Icon, Title, Cwd    string
	Pending             []byte
}

// MarshalBinary captures both screens and the parser's continuation state.
// Call between Writes. Drained scrollback belongs to the embedding emulator.
// An unfinished sequence over 4 MiB or a screen over 1,048,576 cells
// refuses handoff instead of losing bytes or exceeding allocation limits.
func (e *Emulator) MarshalBinary() ([]byte, error) {
	if e.Width() < 1 || e.Height() < 1 || e.Width() > maxSnapshotCells/e.Height() {
		return nil, fmt.Errorf("vt: screen exceeds snapshot cell limit")
	}
	if e.pendingOverflow {
		return nil, fmt.Errorf("vt: unfinished parser sequence exceeds %d bytes", maxPending)
	}
	s := emulatorState{Version: 1, Width: e.Width(), Height: e.Height(), Alt: e.IsAltScreen(),
		Colors: e.colors, Defaults: [3]color.Color{e.defaultFg, e.defaultBg, e.defaultCur},
		Overrides: [3]color.Color{e.fgColor, e.bgColor, e.curColor}, Charsets: e.charsets,
		GL: e.gl, GR: e.gr, Single: e.gsingle, LastChar: e.lastChar, Phantom: e.atPhantom,
		Icon: e.iconName, Title: e.title, Cwd: e.cwd, Pending: e.pending}
	for i := range e.scrs {
		scr := &e.scrs[i]
		if scr.scrollback.Len() != 0 {
			return nil, fmt.Errorf("vt: drain scrollback before snapshot")
		}
		s.Screens[i] = screenState{Flags: scr.flags, Cursor: scr.cur, Saved: scr.saved, Scroll: scr.scroll}
		for y := range s.Height {
			s.Screens[i].Lines = append(s.Screens[i].Lines, scr.Line(y))
		}
	}
	for m, setting := range e.modes {
		_, isANSI := m.(ansi.ANSIMode)
		s.Modes = append(s.Modes, modeState{m.Mode(), isANSI, setting})
	}
	for x := range e.tabstops.Width() {
		if e.tabstops.IsStop(x) {
			s.Tabs = append(s.Tabs, x)
		}
	}
	var b bytes.Buffer
	err := gob.NewEncoder(&b).Encode(s)
	return b.Bytes(), err
}

// UnmarshalBinary restores into a fresh emulator, retaining its handlers,
// callbacks and reply pipe. It does not emit replies or invoke callbacks.
func (e *Emulator) UnmarshalBinary(data []byte) error {
	var s emulatorState
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&s); err != nil {
		return fmt.Errorf("vt state: %w", err)
	}
	if s.Version != 1 {
		return fmt.Errorf("vt: unsupported backend snapshot version %d", s.Version)
	}
	if s.Width < 1 || s.Height < 1 || s.Width > maxSnapshotCells/s.Height || len(s.Pending) > maxPending ||
		s.GL < 0 || s.GL > 3 || s.GR < 0 || s.GR > 3 || s.Single < 0 || s.Single > 3 {
		return fmt.Errorf("vt: invalid snapshot dimensions or charset")
	}
	for _, scr := range s.Screens {
		if len(scr.Lines) != s.Height || len(scr.Flags) != s.Height ||
			scr.Scroll.Empty() || !scr.Scroll.In(uv.Rect(0, 0, s.Width, s.Height)) {
			return fmt.Errorf("vt: invalid screen")
		}
		for _, line := range scr.Lines {
			if len(line) != s.Width {
				return fmt.Errorf("vt: invalid screen row")
			}
		}
	}
	for _, x := range s.Tabs {
		if x < 0 || x >= s.Width {
			return fmt.Errorf("vt: invalid tab stop")
		}
	}
	for i, scr := range s.Screens {
		e.scrs[i].Resize(s.Width, s.Height)
		// Assign whole rows, including zero cells and wide-character halves.
		for y, line := range scr.Lines {
			copy(e.scrs[i].Line(y), line)
		}
		e.scrs[i].flags = scr.Flags
		e.scrs[i].cur, e.scrs[i].saved, e.scrs[i].scroll = scr.Cursor, scr.Saved, scr.Scroll
	}
	e.scr = &e.scrs[0]
	if s.Alt {
		e.scr = &e.scrs[1]
	}
	e.colors, e.charsets = s.Colors, s.Charsets
	e.defaultFg, e.defaultBg, e.defaultCur = s.Defaults[0], s.Defaults[1], s.Defaults[2]
	e.fgColor, e.bgColor, e.curColor = s.Overrides[0], s.Overrides[1], s.Overrides[2]
	e.gl, e.gr, e.gsingle = s.GL, s.GR, s.Single
	e.lastChar, e.atPhantom = s.LastChar, s.Phantom
	e.iconName, e.title, e.cwd = s.Icon, s.Title, s.Cwd
	e.modes = make(ansi.Modes)
	for _, m := range s.Modes {
		var mode ansi.Mode = ansi.DECMode(m.Number)
		if m.ANSI {
			mode = ansi.ANSIMode(m.Number)
		}
		e.modes[mode] = m.Setting
	}
	e.tabstops = uv.DefaultTabStops(s.Width)
	e.tabstops.Clear()
	for _, x := range s.Tabs {
		e.tabstops.Set(x)
	}
	// Rebuild only the unfinished parser sequence, with handlers muted so
	// embedded control bytes cannot execute twice. Completed output is never replayed.
	e.parser.SetHandler(ansi.Handler{})
	e.parser.Reset()
	e.parser.Parse(s.Pending)
	e.setParserHandlers()
	e.pending, e.pendingOverflow = slices.Clone(s.Pending), false
	return nil
}
