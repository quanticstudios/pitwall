package term

import (
	"image"
	"io"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/transfer"
	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/vt"
)

const otherMods = key.ModCtrl | key.ModShift | key.ModSuper | key.ModCommand

// linkMods open links on click: Ctrl, or Cmd on macOS.
const linkMods = key.ModCtrl | key.ModCommand

// ctrlDown is whether a link modifier is held, as of the last key event in
// the focused pane or pointer event in any pane. It is shared so the pane under the
// pointer, which may not have key focus, sees Ctrl go down and up without the
// pointer moving. Views only run on the UI goroutine.
var ctrlDown bool

func (v *View) keys() *config.Bindings {
	if v.Keys == nil {
		return config.Preset(config.DefaultPreset)
	}
	return v.Keys
}

// keyFilters matches every key except Alt chords the window binds with Alt
// alone, so a window filter gets those whether it runs before or after
// View.Layout. Gio filters can't subtract, so Alt combinations are listed
// one name at a time.
func keyFilters(v *View) []event.Filter {
	b := v.keys()
	fs := []event.Filter{
		key.Filter{Focus: v, Optional: otherMods},
		// Tab is a focus-move system key in Gio and only reaches a filter
		// that names it.
		key.Filter{Focus: v, Name: key.NameTab, Optional: otherMods},
	}
	alt := func(n key.Name) {
		if a := b.Action(key.Event{Name: n, Modifiers: key.ModAlt}); a == "" || config.PaneAction(a) {
			fs = append(fs, key.Filter{Focus: v, Name: n, Required: key.ModAlt, Optional: otherMods})
		}
	}
	for c := byte('!'); c <= '~'; c++ {
		if c < 'a' || c > 'z' { // Gio names letter keys in upper case
			alt(key.Name(c))
		}
	}
	for _, n := range []key.Name{
		key.NameReturn, key.NameEnter, key.NameEscape, key.NameHome, key.NameEnd,
		key.NameDeleteBackward, key.NameDeleteForward, key.NamePageUp, key.NamePageDown,
		key.NameTab, key.NameSpace, key.NameF1, key.NameF2, key.NameF3, key.NameF4,
		key.NameF5, key.NameF6, key.NameF7, key.NameF8, key.NameF9, key.NameF10,
		key.NameF11, key.NameF12, key.NameLeftArrow, key.NameRightArrow, key.NameUpArrow, key.NameDownArrow,
	} {
		alt(n)
	}
	return fs
}

// textKey reports whether Gio also delivers e as a key.EditEvent, in which
// case the text event is the one sent so keyboard layouts and compose work.
func textKey(e key.Event) bool {
	if e.Modifiers&^key.ModShift != 0 {
		return false
	}
	return e.Name == key.NameSpace || len(e.Name) == 1 && e.Name[0] > ' ' && e.Name[0] <= '~'
}

func (v *View) events(gtx layout.Context, g *vt.Grid, m vt.Modes, focused bool, rows int) []byte {
	if v.filters == nil || v.filtersFor != v.keys() {
		v.filtersFor = v.keys()
		v.filters = append(keyFilters(v),
			key.FocusFilter{Target: v},
			transfer.TargetFilter{Target: v, Type: "application/text"},
			pointer.Filter{
				Target:  v,
				Kinds:   pointer.Press | pointer.Release | pointer.Drag | pointer.Move | pointer.Scroll | pointer.Cancel | pointer.Leave,
				ScrollX: pointer.ScrollRange{Min: -1e6, Max: 1e6},
				ScrollY: pointer.ScrollRange{Min: -1e6, Max: 1e6},
			},
		)
	}
	if focused && !gtx.Focused(v) {
		gtx.Execute(key.FocusCmd{Tag: v})
	}
	kittyAll := m.KittyKeyboard&8 != 0 // report all keys as escape codes
	var out []byte
	for {
		ev, ok := gtx.Event(v.filters...)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.Event:
			if e.Name == key.NameCtrl || e.Name == key.NameCommand {
				ctrlDown = e.State == key.Press
			}
			if e.State == key.Press {
				v.eatText = ""
			}
			// Both press and release of a bound key stay out of the program.
			switch a := v.keys().Action(e); {
			case config.PaneAction(a):
				if e.State == key.Press {
					v.action(gtx, g, a, rows)
				}
				continue
			case a != "":
				continue // the window's; it reached the pane before the window polled
			case v.cm.on:
				if e.State == key.Press {
					v.copyModeKey(e, g, rows)
					if !v.cm.on && textKey(e) {
						v.eatText = string(e.Name) // y or q left; its text event follows
					}
				}
				continue // copy mode sends the program nothing
			}
			if textKey(e) && !kittyAll {
				continue
			}
			v.keyText = ""
			if kittyAll && e.State == key.Press && textKey(e) {
				v.keyText = string(e.Name)
				if e.Name == key.NameSpace {
					v.keyText = " "
				}
			}
			out = append(out, input.Key(e, m)...)
		case key.EditEvent:
			eat := v.eatText != "" && strings.EqualFold(e.Text, v.eatText)
			v.eatText = ""
			if v.cm.on || eat {
				continue
			}
			// Report-all already encoded a plain key press; its text event
			// follows it and is dropped. IME and compose commits have no
			// such press and go through.
			dup := kittyAll && v.keyText != "" && strings.EqualFold(e.Text, v.keyText)
			v.keyText = ""
			if !dup {
				out = append(out, input.Text(e.Text)...)
			}
		case transfer.DataEvent:
			r := e.Open()
			b, _ := io.ReadAll(r)
			r.Close()
			if !v.cm.on {
				out = append(out, input.Paste(string(b), m)...)
			}
		case pointer.Event:
			out = append(out, v.pointer(e, g, m, focused)...)
		case key.FocusEvent:
			// Gio sends these for key focus moves and the window's focus.
			v.keyFocus = e.Focus
			ctrlDown = ctrlDown && e.Focus
		}
	}
	if a := v.queued; a != "" {
		v.queued = ""
		v.action(gtx, g, a, rows)
	}
	if v.selDone && v.CopyOnSelect {
		v.copy(g)
	}
	v.selDone = false
	v.focusMode = m.FocusEvents
	if in := focused && v.keyFocus; in != v.focusIn {
		v.focusIn = in
		out = append(out, input.Focus(in, m.FocusEvents)...)
	}
	return out
}

// Run runs a pane action (config.PaneAction) in the next Layout, as its
// key would. The command palette runs them this way.
func (v *View) Run(action string) { v.queued = action }

// action runs a pane action: copy, copy mode, paste, scroll a page of
// rows, or jump to a shell prompt.
func (v *View) action(gtx layout.Context, g *vt.Grid, a string, rows int) {
	switch a {
	case "copy":
		if v.selOn {
			v.copy(g)
		}
		if v.cm.on {
			v.exitCopyMode()
		}
	case "copy_mode":
		v.toggleCopyMode(g, rows)
	case "paste":
		gtx.Execute(clipboard.ReadCmd{Tag: v})
	case "scroll_page_up":
		v.scrollLines += rows
	case "scroll_page_down":
		v.scrollLines -= rows
	case "prev_prompt":
		v.prompts++
	case "next_prompt":
		v.prompts--
	}
}

func (v *View) pointer(e pointer.Event, g *vt.Grid, m vt.Modes, focused bool) []byte {
	held := v.buttons
	v.buttons = e.Buttons
	if e.Kind == pointer.Cancel {
		v.buttons = 0
	}
	if e.Kind == pointer.Leave || e.Kind == pointer.Cancel {
		v.inside = false
	}
	if e.Kind == pointer.Leave || g.Cols == 0 || g.Rows == 0 {
		return nil
	}
	cell := image.Pt(
		min(max((int(e.Position.X)-v.pad)/v.cell.X, 0), g.Cols-1),
		min(max((int(e.Position.Y)-v.pad)/v.cell.Y, 0), g.Rows-1),
	)
	if e.Kind != pointer.Cancel {
		ctrlDown, v.inside, v.ptr = e.Modifiers&linkMods != 0, true, cell
	}
	// Ctrl+click (Cmd+click on macOS) on a link opens it, even when the program owns the mouse,
	// and neither the program nor the selection sees that click.
	if v.Links && e.Kind == pointer.Press && e.Buttons == pointer.ButtonPrimary && ctrlDown {
		if l, ok := v.linkAt(g, cell); ok {
			v.open, v.linkPress = l.url, true
			return nil
		}
	}
	if v.linkPress {
		v.linkPress = e.Kind != pointer.Release && e.Kind != pointer.Cancel
		return nil
	}
	// Shift forces local selection even when the program owns the mouse.
	if m.Mouse != vt.MouseOff && e.Modifiers&key.ModShift == 0 {
		if focused {
			// Gio reports the buttons held after the event; the program
			// wants the one pressed or released.
			if c := e.Buttons ^ held; (e.Kind == pointer.Press || e.Kind == pointer.Release) && c != 0 {
				e.Buttons = c
			}
			return input.Mouse(e, cell.X, cell.Y, m)
		}
		return nil
	}
	switch e.Kind {
	case pointer.Scroll:
		v.scroll(e)
	case pointer.Press:
		if e.Buttons != pointer.ButtonPrimary {
			return nil
		}
		v.clicks++
		if e.Time-v.lastPress.Time >= 400*time.Millisecond || cell != v.lastCell || v.clicks > 3 {
			v.clicks = 1
		}
		v.lastPress, v.lastCell = e, cell
		at := v.pos(cell)
		switch v.clicks {
		case 2:
			x0, x1 := wordAt(g, cell.X, cell.Y)
			v.selectDone(vt.Selection{A: vt.Pos{Line: at.Line, Col: x0}, B: vt.Pos{Line: at.Line, Col: x1}}, g)
		case 3:
			v.selectDone(vt.Selection{A: at, B: at, Mode: vt.SelectLines}, g)
		default:
			mode := vt.SelectChars
			if e.Modifiers&key.ModAlt != 0 {
				mode = vt.SelectBlock
			}
			v.sel, v.selOn, v.selCols = vt.Selection{A: at, B: at, Mode: mode}, false, g.Cols
			v.dragging, v.dragAt = true, e.Position
		}
	case pointer.Drag:
		if v.dragging {
			v.dragAt = e.Position
			v.drag(g)
		}
	case pointer.Release, pointer.Cancel:
		v.selDone = v.selDone || e.Kind == pointer.Release && v.dragging && v.selOn
		v.dragging, v.auto = false, 0
	}
	return nil
}

// scroll gathers wheel distance into whole lines of scrollback, keeping the
// remainder so touchpad pixel deltas add up. Gio turns Shift+wheel into a
// horizontal scroll, which counts as vertical here.
func (v *View) scroll(e pointer.Event) {
	d := e.Scroll.Y
	if d == 0 && e.Modifiers&key.ModShift != 0 {
		d = e.Scroll.X
	}
	v.scrollPx -= d // wheel up is back in history
	n := int(v.scrollPx / float32(v.cell.Y))
	v.scrollPx -= float32(n * v.cell.Y)
	v.scrollLines += n
}

// wordAt is the inclusive column range of the word under (x,y). Separators
// follow Ghostty's defaults minus ':', so URLs and paths select whole.
func wordAt(g *vt.Grid, x, y int) (int, int) {
	word := func(x int) bool { return isWord(g.At(x, y).Content) }
	if !word(x) {
		return x, x
	}
	x0, x1 := x, x
	for x0 > 0 && (word(x0-1) || g.At(x0-1, y).Width == 0) {
		x0--
	}
	for x1 < g.Cols-1 && (word(x1+1) || g.At(x1+1, y).Width == 0) {
		x1++
	}
	return x0, x1
}

// isWord reports whether a cell's content is part of a word.
func isWord(c string) bool {
	return c != "" && !strings.ContainsAny(c, " \t'\"`|;,()[]{}<>$│")
}

// Blur reports focus loss for a pane that is no longer drawn, such as one on
// the workspace just left, and returns the bytes for its PTY.
func (v *View) Blur() []byte {
	if !v.focusIn {
		return nil
	}
	v.focusIn = false
	return input.Focus(false, v.focusMode)
}
