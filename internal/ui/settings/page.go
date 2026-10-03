// Package settings is the settings page. It fills the pane area like a tab,
// as Warp's settings do: categories and a search field on the left, rows of
// label and control on the right. Every change goes straight to
// config.toml through config.SetKey, so the file stays the one source of
// truth and keeps the user's comments.
package settings

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Result is what the window should do after a frame of the page.
type Result int

const (
	None   Result = iota
	Closed        // back to the terminal
	Saved         // config.toml changed; reload it now
)

const (
	catAppearance = iota
	catKeys
	catTerminal
	catAgents
	catAbout
)

var categories = []struct{ name, desc string }{
	{"Appearance", "Theme, fonts and spacing. Changes apply as you make them."},
	{"Keyboard shortcuts", "Click a shortcut to record a new one."},
	{"Terminal", "What every pane keeps."},
	{"Agents", "The sidebar learns what Claude Code and Codex are doing from hooks in their configs."},
	{"About", "Version, config file and documentation."},
}

const repo = "https://github.com/quanticstudios/pitwall"

// Page is the settings page's state. The zero value is hidden.
type Page struct {
	shown  bool
	cat    int
	th     *theme.Theme
	s      config.Settings
	probs  []string
	err    string // the last save that failed
	result Result

	search      widget.Editor
	focusSearch bool
	list        widget.List
	cats        [5]widget.Clickable
	clicks      map[string]*widget.Clickable

	rec      slot // the chord being recorded, when rec.action is set
	recTag   int
	recFocus bool // move key focus to recTag this frame
	recHad   bool // recTag has had focus since recording started
	conflict *pending

	dd       string // the open font dropdown's config key
	ddFilter widget.Editor
	ddList   widget.List
	ddTag    int
	ddPanel  int
	ddFocus  bool

	themes   []config.NamedTheme
	hooks    []hookFile
	ver      string
	copied   string
	copiedAt time.Time
}

// pending is a recorded chord another action already has.
type pending struct {
	slot
	chord config.Chord
	other string
}

// Shown reports whether the page is open.
func (p *Page) Shown() bool { return p.shown }

// Show opens the page and reads what it shows from disk: themes, hooks.
func (p *Page) Show(configPath string) {
	p.shown, p.focusSearch, p.err = true, true, ""
	p.themes = config.AllThemes(filepath.Join(filepath.Dir(configPath), "themes"))
	home, _ := os.UserHomeDir()
	p.hooks = hookStatus(home)
	p.ver = version()
	families(nil) // start the font scan
}

// Hide closes the page, dropping a recording or open dropdown.
func (p *Page) Hide() {
	p.shown, p.rec, p.conflict, p.dd = false, slot{}, nil, ""
}

// take returns the frame's result. After a save it asks for another
// frame, which draws the reloaded config.
func (p *Page) take(gtx gl.Context) Result {
	r := p.result
	p.result = None
	if r != None {
		gtx.Execute(op.InvalidateCmd{})
	}
	return r
}

func (p *Page) btn(id string) *widget.Clickable {
	if p.clicks == nil {
		p.clicks = map[string]*widget.Clickable{}
	}
	c := p.clicks[id]
	if c == nil {
		c = new(widget.Clickable)
		p.clicks[id] = c
	}
	return c
}

func modifier(k key.Name) bool {
	switch k {
	case key.NameCtrl, key.NameShift, key.NameAlt, key.NameSuper, key.NameCommand:
		return true
	}
	return false
}

// Keys takes the keys the page owns. The window calls it before polling
// its own shortcuts, so a chord being recorded never runs its action, and
// Escape closes the page (or what is open on it).
func (p *Page) Keys(gtx gl.Context) Result {
	if !p.shown {
		return None
	}
	if p.rec.action != "" {
		for {
			ev, ok := gtx.Event(p.recFilters()...)
			if !ok {
				break
			}
			switch e := ev.(type) {
			case key.FocusEvent:
				if e.Focus {
					p.recHad = true
				} else if p.recHad && !p.recFocus {
					p.rec = slot{} // focus went elsewhere, like the search field
				}
			case key.Event:
				if e.State == key.Press && !modifier(e.Name) && p.rec.action != "" {
					p.recorded(config.Chord{Mods: e.Modifiers, Name: e.Name})
				}
			}
		}
		return p.take(gtx)
	}
	for {
		ev, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			switch {
			case p.dd != "":
				p.dd = ""
			case p.conflict != nil:
				p.conflict = nil
			case p.search.Text() != "":
				p.search.SetText("")
			default:
				p.result = Closed
			}
		}
	}
	return p.take(gtx)
}

// recFilters are every key, for the recorder. Gio routes a key only to
// filters polled in the frame before it, so they are polled from the
// frame recording starts in.
func (p *Page) recFilters() []event.Filter {
	all := key.ModAlt | key.ModShift | key.ModCtrl | key.ModSuper | key.ModCommand
	return []event.Filter{
		key.FocusFilter{Target: &p.recTag},
		key.Filter{Focus: &p.recTag, Optional: all},
		key.Filter{Focus: &p.recTag, Name: key.NameTab, Optional: all},
		key.Filter{Focus: &p.recTag, Name: key.NameEscape, Optional: all},
	}
}

// recorded handles one key pressed while recording.
func (p *Page) recorded(c config.Chord) {
	s := p.rec
	p.rec = slot{}
	switch {
	case c.Mods == 0 && c.Name == key.NameEscape:
	case c.Mods == 0 && c.Name == key.NameDeleteBackward:
		p.write(edit{s.action, s.tab, replaceChord(chordsOf(p.s.Keys, s.action, s.tab), s.index, nil)})
	case owner(p.s.Keys, s, c) != "":
		p.conflict = &pending{s, c, owner(p.s.Keys, s, c)}
	default:
		p.write(record(p.s.Keys, s, c, false)...)
	}
}

func (p *Page) startRecord(s slot) {
	p.rec, p.recFocus, p.recHad, p.conflict, p.dd = s, true, false, nil, ""
}

func (p *Page) write(es ...edit) {
	for _, e := range es {
		p.save(table(e.tab), e.action, e.value(p.s.Keys.Preset))
	}
}

// save sets table.key to the TOML literal v, or removes it for nil.
func (p *Page) save(table, k string, v *string) {
	path := p.s.Path
	if path == "" {
		path = config.Path()
	}
	var err error
	if v == nil {
		err = config.RemoveKey(path, table, k)
	} else {
		err = config.SetKey(path, table, k, *v)
	}
	if err != nil {
		p.err = err.Error()
		return
	}
	p.err, p.result = "", Saved
}

func (p *Page) saveValue(table, k, v string) { p.save(table, k, &v) }

func (p *Page) copy(gtx gl.Context, id, s string) {
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s))})
	p.copied, p.copiedAt = id, gtx.Now
}

func open(target string) {
	cmd := exec.Command("xdg-open", target)
	if cmd.Start() == nil {
		go cmd.Wait()
	}
}

// Layout draws the page into the constraints (the pane area) and returns
// what the window should do.
func (p *Page) Layout(gtx gl.Context, th *theme.Theme, s config.Settings, probs []string) Result {
	p.th, p.s, p.probs = th, s, probs
	if p.s.Keys == nil {
		p.s.Keys = config.Preset(config.DefaultPreset)
	}
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &p.recTag)
	paint.FillShape(gtx.Ops, th.Surface, clip.Rect{Max: size}.Op())

	for {
		ev, ok := p.search.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.ChangeEvent); ok {
			p.list.Position = gl.Position{}
		}
	}
	p.search.SingleLine = true
	q := strings.TrimSpace(p.search.Text())

	navW := min(gtx.Dp(224), size.X/3)
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: image.Pt(navW, size.Y)}.Op())
	paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(navW, 0), Max: image.Pt(navW+1, size.Y)}.Op())
	ng := gtx
	ng.Constraints = gl.Exact(image.Pt(navW, size.Y))
	p.nav(ng, q)

	cg := gtx
	cg.Constraints = gl.Exact(image.Pt(size.X-navW-1, size.Y))
	o := op.Offset(image.Pt(navW+1, 0)).Push(gtx.Ops)
	p.content(cg, q)
	o.Pop()

	if p.focusSearch {
		p.focusSearch = false
		gtx.Execute(key.FocusCmd{Tag: &p.search})
	}
	if p.recFocus {
		// A click starts recording after Keys polled this frame.
		for {
			if _, ok := gtx.Event(p.recFilters()...); !ok {
				break
			}
		}
		p.recFocus = false
		gtx.Execute(key.FocusCmd{Tag: &p.recTag})
	}
	if p.copied != "" {
		if left := 1500*time.Millisecond - gtx.Now.Sub(p.copiedAt); left > 0 {
			gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(left)})
		} else {
			p.copied = ""
		}
	}
	return p.take(gtx)
}

// nav is the left column: title, search field, categories.
func (p *Page) nav(gtx gl.Context, q string) gl.Dimensions {
	th := p.th
	counts := make([]int, len(categories))
	if q != "" {
		for i := range categories {
			for _, sec := range p.sections(i) {
				counts[i] += len(filterRows(sec.rows, q))
			}
		}
	}
	kids := []gl.FlexChild{
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			return gl.Inset{Left: 8, Top: 4, Bottom: 4}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
				return p.text(gtx, weight(th.UIFont, font.SemiBold), p.sp(14), th.Fg, "Settings")
			})
		}),
		gl.Rigid(gl.Spacer{Height: 12}.Layout),
		gl.Rigid(p.searchField),
		gl.Rigid(gl.Spacer{Height: 16}.Layout),
	}
	for i, c := range categories {
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			b := &p.cats[i]
			for b.Clicked(gtx) {
				p.cat, p.list.Position, p.dd, p.conflict = i, gl.Position{}, "", nil
				p.search.SetText("")
			}
			return b.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
				h, w := gtx.Dp(30), gtx.Constraints.Max.X
				sel := q == "" && p.cat == i
				fg := th.Muted
				f := th.UIFont
				switch {
				case sel:
					rrect(gtx, theme.Mix(th.SurfaceElevated, th.Fg, 0.04), image.Rect(0, 0, w, h), gtx.Dp(6))
					fg, f = th.Fg, weight(f, font.Medium)
				case b.Hovered():
					rrect(gtx, th.SurfaceElevated, image.Rect(0, 0, w, h), gtx.Dp(6))
					fg = th.Fg
				case q != "" && counts[i] == 0:
					fg = theme.Mix(th.Bg, th.Muted, 0.5)
				}
				m := op.Record(gtx.Ops)
				d := p.text(gtx, f, p.sp(13), fg, c.name)
				call := m.Stop()
				oo := op.Offset(image.Pt(gtx.Dp(10), (h-d.Size.Y)/2)).Push(gtx.Ops)
				call.Add(gtx.Ops)
				oo.Pop()
				if q != "" && counts[i] > 0 {
					m := op.Record(gtx.Ops)
					d := p.text(gtx, th.UIFont, p.sp(12), th.Muted, fmt.Sprint(counts[i]))
					call := m.Stop()
					oo := op.Offset(image.Pt(w-gtx.Dp(10)-d.Size.X, (h-d.Size.Y)/2)).Push(gtx.Ops)
					call.Add(gtx.Ops)
					oo.Pop()
				}
				defer clip.Rect{Max: image.Pt(w, h)}.Push(gtx.Ops).Pop()
				pointer.CursorPointer.Add(gtx.Ops)
				return gl.Dimensions{Size: image.Pt(w, h)}
			})
		}))
		kids = append(kids, gl.Rigid(gl.Spacer{Height: 2}.Layout))
	}
	return gl.UniformInset(16).Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
	})
}

// field draws a single-line editor in aide's h-8 input with a placeholder.
func (p *Page) field(gtx gl.Context, e *widget.Editor, placeholder string, glyph bool) gl.Dimensions {
	th := p.th
	w, h := gtx.Constraints.Max.X, gtx.Dp(32)
	border := th.Border
	if gtx.Focused(e) {
		border = theme.Mix(th.SurfaceElevated, th.Primary, 0.6)
	}
	rrect(gtx, border, image.Rect(0, 0, w, h), gtx.Dp(6))
	rrect(gtx, th.SurfaceElevated, image.Rect(1, 1, w-1, h-1), gtx.Dp(6)-1)
	x := gtx.Dp(10)
	if glyph {
		is := gtx.Dp(14)
		o := op.Offset(image.Pt(gtx.Dp(9), (h-is)/2)).Push(gtx.Ops)
		icon(gtx, th.Muted, is, searchGlyph)
		o.Pop()
		x = gtx.Dp(30)
	}
	eg := gtx
	eg.Constraints = gl.Exact(image.Pt(max(0, w-x-gtx.Dp(8)), h))
	o := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
	if e.Len() == 0 {
		gl.W.Layout(eg, func(gtx gl.Context) gl.Dimensions {
			return p.text(gtx, th.UIFont, p.sp(13), th.Muted, placeholder)
		})
	}
	gl.W.Layout(eg, func(gtx gl.Context) gl.Dimensions {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, 0)
		return e.Layout(gtx, th.Shaper, th.UIFont, p.sp(13), colorOp(gtx, th.Fg), colorOp(gtx, theme.Mix(th.SurfaceElevated, th.Primary, 0.35)))
	})
	o.Pop()
	return gl.Dimensions{Size: image.Pt(w, h)}
}

func (p *Page) searchField(gtx gl.Context) gl.Dimensions {
	return p.field(gtx, &p.search, "Search settings", true)
}

// row is one setting: a label, a line about it, and its control on the
// right (or under it, when wide).
type row struct {
	label, desc string
	extra       string // more text the search matches
	wide        bool
	control     gl.Widget
	below       gl.Widget // a prompt under the row, like a shortcut conflict
}

type section struct {
	title, desc string
	rows        []row
}

func filterRows(rs []row, q string) []row {
	var out []row
	for _, r := range rs {
		if matches(q, r.label, r.desc, r.extra) {
			out = append(out, r)
		}
	}
	return out
}

// content is the scrolling right side: the category (or every match of
// the search) as cards of rows, at most 800dp wide.
func (p *Page) content(gtx gl.Context, q string) gl.Dimensions {
	th := p.th
	var items []gl.Widget
	space := func(h unit.Dp) { items = append(items, gl.Spacer{Height: h}.Layout) }
	title, desc := categories[p.cat].name, categories[p.cat].desc
	if q != "" {
		title, desc = "Search", "Settings matching “"+q+"”."
	}
	items = append(items, func(gtx gl.Context) gl.Dimensions { return p.header(gtx, title, desc) })
	if len(p.probs) > 0 || p.err != "" {
		space(16)
		items = append(items, p.alert)
	}
	addSections := func(secs []section) {
		for _, sec := range secs {
			space(24)
			if sec.title != "" {
				items = append(items, func(gtx gl.Context) gl.Dimensions {
					return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
						gl.Rigid(func(gtx gl.Context) gl.Dimensions {
							return p.text(gtx, weight(th.UIFont, font.Medium), p.sp(13), th.Fg, sec.title)
						}),
						gl.Rigid(func(gtx gl.Context) gl.Dimensions {
							if sec.desc == "" {
								return gl.Dimensions{}
							}
							return gl.Inset{Top: 2}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
								return p.para(gtx, th.UIFont, p.sp(12), th.Muted, sec.desc)
							})
						}),
					)
				})
				space(10)
			}
			for i, r := range sec.rows {
				first, last := i == 0, i == len(sec.rows)-1
				items = append(items, func(gtx gl.Context) gl.Dimensions { return p.card(gtx, r, first, last) })
			}
		}
	}
	if q == "" {
		addSections(p.sections(p.cat))
	} else {
		found := false
		for i, c := range categories {
			var secs []section
			for _, sec := range p.sections(i) {
				if rs := filterRows(sec.rows, q); len(rs) > 0 {
					secs = append(secs, section{title: sec.title, rows: rs})
				}
			}
			if len(secs) == 0 {
				continue
			}
			found = true
			if secs[0].title == "" {
				secs[0].title = c.name
			} else {
				secs[0].title = c.name + " · " + secs[0].title
			}
			addSections(secs)
		}
		if !found {
			space(24)
			items = append(items, func(gtx gl.Context) gl.Dimensions {
				return p.para(gtx, th.UIFont, p.sp(13), th.Muted, "No settings match. Search looks at names, descriptions and shortcuts.")
			})
		}
	}
	space(40)

	p.list.Axis = gl.Vertical
	return p.list.Layout(gtx, len(items), func(gtx gl.Context, i int) gl.Dimensions {
		w := gtx.Constraints.Max.X
		inner := min(gtx.Dp(800), w-2*gtx.Dp(32))
		x := (w - inner) / 2
		top := 0
		if i == 0 {
			top = gtx.Dp(40)
		}
		g := gtx
		g.Constraints = gl.Constraints{Min: image.Pt(inner, 0), Max: image.Pt(inner, gtx.Constraints.Max.Y)}
		o := op.Offset(image.Pt(x, top)).Push(gtx.Ops)
		d := items[i](g)
		o.Pop()
		return gl.Dimensions{Size: image.Pt(w, d.Size.Y+top)}
	})
}

func (p *Page) header(gtx gl.Context, title, desc string) gl.Dimensions {
	th := p.th
	return gl.Flex{Alignment: gl.Start}.Layout(gtx,
		gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
			return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.text(gtx, weight(th.UIFont, font.Medium), p.sp(20), th.Fg, title)
				}),
				gl.Rigid(gl.Spacer{Height: 4}.Layout),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return p.para(gtx, th.UIFont, p.sp(13), th.Muted, desc)
				}),
			)
		}),
		gl.Rigid(gl.Spacer{Width: 16}.Layout),
		gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			c := p.btn("close")
			for c.Clicked(gtx) {
				p.result = Closed
			}
			return hstack(gtx, 8,
				func(gtx gl.Context) gl.Dimensions { return p.keycap(gtx, "Esc", false, false) },
				func(gtx gl.Context) gl.Dimensions { return p.button(gtx, c, secondary, "Close") },
			)
		}),
	)
}

// alert lists config problems and a failed save, like aide's danger box.
func (p *Page) alert(gtx gl.Context) gl.Dimensions {
	th := p.th
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return boxed(gtx, theme.Mix(th.Surface, th.Red, 0.08), theme.Mix(th.Surface, th.Red, 0.35), gtx.Dp(6), image.Pt(gtx.Dp(12), gtx.Dp(10)), func(gtx gl.Context) gl.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		var kids []gl.FlexChild
		line := func(f font.Font, size unit.Sp, c, s string) {
			kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				col := th.Red
				if c == "fg" {
					col = th.Fg
				}
				return p.para(gtx, f, size, col, s)
			}))
		}
		if p.err != "" {
			line(weight(th.UIFont, font.Medium), p.sp(13), "red", "Could not save: "+p.err)
		}
		if len(p.probs) > 0 {
			if p.err != "" {
				kids = append(kids, gl.Rigid(gl.Spacer{Height: 8}.Layout))
			}
			line(weight(th.UIFont, font.Medium), p.sp(13), "red", "config.toml has problems. These entries use their defaults until fixed; changes here still save.")
			kids = append(kids, gl.Rigid(gl.Spacer{Height: 4}.Layout))
			for i, pr := range p.probs {
				if i == 8 {
					line(th.UIFont, p.sp(12), "fg", fmt.Sprintf("and %d more (pitwall config check)", len(p.probs)-8))
					break
				}
				line(th.MonoFont, p.sp(12), "fg", pr)
			}
		}
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
	})
}

// card draws one row as part of a bordered card: rounded on top for the
// first, at the bottom for the last, a hairline between rows.
func (p *Page) card(gtx gl.Context, r row, first, last bool) gl.Dimensions {
	th := p.th
	pad := image.Pt(gtx.Dp(16), gtx.Dp(12))
	w := gtx.Constraints.Max.X
	m := op.Record(gtx.Ops)
	g := gtx
	g.Constraints = gl.Constraints{Max: image.Pt(w-2*pad.X, gtx.Constraints.Max.Y)}
	g.Constraints.Min.X = g.Constraints.Max.X
	d := p.rowContent(g, r)
	call := m.Stop()
	sz := image.Pt(w, d.Size.Y+2*pad.Y)
	rad := gtx.Dp(8)
	top, bot := 0, 0
	if first {
		top = rad
	}
	if last {
		bot = rad
	}
	paint.FillShape(gtx.Ops, th.Border, clip.RRect{Rect: image.Rectangle{Max: sz}, NW: top, NE: top, SE: bot, SW: bot}.Op(gtx.Ops))
	in := image.Rect(1, 1, sz.X-1, sz.Y)
	if last {
		in.Max.Y--
	}
	paint.FillShape(gtx.Ops, th.SurfaceSecondary, clip.RRect{Rect: in, NW: max(0, top-1), NE: max(0, top-1), SE: max(0, bot-1), SW: max(0, bot-1)}.Op(gtx.Ops))
	o := op.Offset(pad).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return gl.Dimensions{Size: sz}
}

func (p *Page) rowContent(gtx gl.Context, r row) gl.Dimensions {
	th := p.th
	labels := func(gtx gl.Context) gl.Dimensions {
		return gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				return p.para(gtx, weight(th.UIFont, font.Medium), p.sp(13), th.Fg, r.label)
			}),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions {
				if r.desc == "" {
					return gl.Dimensions{}
				}
				return gl.Inset{Top: 2}.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
					return p.para(gtx, th.UIFont, p.sp(12), th.Muted, r.desc)
				})
			}),
		)
	}
	var kids []gl.FlexChild
	if r.wide {
		kids = append(kids, gl.Rigid(labels), gl.Rigid(gl.Spacer{Height: 12}.Layout), gl.Rigid(r.control))
	} else {
		kids = append(kids, gl.Rigid(func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
				gl.Flexed(1, labels),
				gl.Rigid(gl.Spacer{Width: 16}.Layout),
				gl.Rigid(r.control),
			)
		}))
	}
	if r.below != nil {
		kids = append(kids, gl.Rigid(gl.Spacer{Height: 10}.Layout), gl.Rigid(r.below))
	}
	return gl.Flex{Axis: gl.Vertical}.Layout(gtx, kids...)
}

// sections are a category's rows.
func (p *Page) sections(cat int) []section {
	switch cat {
	case catAppearance:
		return p.appearance()
	case catKeys:
		return p.shortcuts()
	case catTerminal:
		return p.terminal()
	case catAgents:
		return p.agents()
	case catAbout:
		return p.about()
	}
	return nil
}

func (p *Page) appearance() []section {
	f := p.s.Font
	var names []string
	for _, t := range p.themes {
		names = append(names, t.Name)
	}
	return []section{
		{rows: []row{
			{label: "Theme", desc: "Window and terminal colors. Custom themes are themes/<name>.toml next to config.toml.",
				extra: "color colour dark light " + strings.Join(names, " "), wide: true, control: p.themeCards},
		}},
		{title: "Text", rows: []row{
			{label: "Interface font", desc: "Sidebar, dialogs and this page. Geist ships with pitwall.", extra: "ui family typeface",
				control: p.fontPicker("ui_family", f.UIFamily, config.DefaultUIFamily)},
			{label: "Interface text size", extra: "ui font", control: p.stepper("font", "ui_size", f.UISize, 6, 48, 1, config.DefaultUISize)},
			{label: "Terminal font", desc: "Any installed family. Characters it lacks come from mono_fallback, then any monospace font.", extra: "mono family typeface",
				control: p.fontPicker("mono_family", f.MonoFamily, config.DefaultMonoFamily)},
			{label: "Terminal text size", extra: "mono font", control: p.stepper("font", "mono_size", f.MonoSize, 6, 48, 1, config.DefaultMonoSize)},
			{label: "Line height", desc: "Terminal rows as a multiple of the font's height.", extra: "spacing",
				control: p.stepper("font", "line_height", f.LineHeight, 0.8, 3, 0.05, 1)},
		}},
		{title: "Panes", rows: []row{
			{label: "Pane gap", desc: "Space between split panes, in dp.", extra: "spacing split",
				control: p.stepper("layout", "pane_gap", p.s.PaneGap, 0, 64, 1, config.DefaultPaneGap)},
			{label: "Pane margin", desc: "Space between the panes and the window edges and sidebar, in dp.", extra: "spacing padding",
				control: p.stepper("layout", "pane_margin", p.s.PaneMargin, 0, 64, 1, config.DefaultPaneMargin)},
		}},
	}
}

// stepper is aide-styled − value + with a reset arrow once the value is
// not the default. The default is written by removing the key.
func (p *Page) stepper(table, k string, v, lo, hi, step, def float64) gl.Widget {
	return func(gtx gl.Context) gl.Dimensions {
		th := p.th
		id := table + "." + k
		set := func(nv float64) {
			nv = math.Round(min(hi, max(lo, nv))*100) / 100
			if nv == def {
				p.save(table, k, nil)
			} else if nv != v {
				p.saveValue(table, k, config.Number(nv))
			}
		}
		for p.btn(id + "-").Clicked(gtx) {
			set(v - step)
		}
		for p.btn(id + "+").Clicked(gtx) {
			set(v + step)
		}
		for p.btn(id + "reset").Clicked(gtx) {
			set(def)
		}
		return hstack(gtx, 4,
			p.resetSlot(id+"reset", v != def),
			func(gtx gl.Context) gl.Dimensions {
				return boxed(gtx, th.SurfaceElevated, th.Border, gtx.Dp(6), image.Point{}, func(gtx gl.Context) gl.Dimensions {
					return hstack(gtx, 0,
						func(gtx gl.Context) gl.Dimensions { return p.iconButton(gtx, p.btn(id+"-"), minusGlyph) },
						func(gtx gl.Context) gl.Dimensions {
							w := gtx.Dp(52)
							m := op.Record(gtx.Ops)
							d := p.text(gtx, th.UIFont, p.sp(13), th.Fg, config.Number(v))
							call := m.Stop()
							o := op.Offset(image.Pt((w-d.Size.X)/2, 0)).Push(gtx.Ops)
							call.Add(gtx.Ops)
							o.Pop()
							return gl.Dimensions{Size: image.Pt(w, d.Size.Y)}
						},
						func(gtx gl.Context) gl.Dimensions { return p.iconButton(gtx, p.btn(id+"+"), plusGlyph) },
					)
				})
			},
		)
	}
}

// resetSlot is the reset arrow when shown, else the space it takes, so
// controls line up.
func (p *Page) resetSlot(id string, shown bool) gl.Widget {
	return func(gtx gl.Context) gl.Dimensions {
		if !shown {
			return gl.Dimensions{Size: image.Pt(gtx.Dp(28), gtx.Dp(28))}
		}
		return p.iconButton(gtx, p.btn(id), resetGlyph)
	}
}

// segmented is a row of options with the current one raised.
func (p *Page) segmented(id string, opts []string, cur string, pick func(string)) gl.Widget {
	return func(gtx gl.Context) gl.Dimensions {
		th := p.th
		for _, o := range opts {
			for p.btn(id + ":" + o).Clicked(gtx) {
				if o != cur {
					pick(o)
				}
			}
		}
		return boxed(gtx, th.SurfaceElevated, th.Border, gtx.Dp(6), image.Pt(gtx.Dp(2), gtx.Dp(2)), func(gtx gl.Context) gl.Dimensions {
			var ws []gl.Widget
			for _, o := range opts {
				c := p.btn(id + ":" + o)
				ws = append(ws, func(gtx gl.Context) gl.Dimensions {
					return c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
						label := o
						if label == "" {
							label = "None"
						}
						fg, f := th.Muted, th.UIFont
						m := op.Record(gtx.Ops)
						if o == cur {
							fg, f = th.Fg, weight(f, font.Medium)
						} else if c.Hovered() {
							fg = th.Fg
						}
						d := p.text(gtx, f, p.sp(13), fg, label)
						call := m.Stop()
						sz := image.Pt(d.Size.X+2*gtx.Dp(10), gtx.Dp(24))
						if o == cur {
							rrect(gtx, theme.Mix(th.SurfaceElevated, th.Fg, 0.1), image.Rectangle{Max: sz}, gtx.Dp(4))
						}
						oo := op.Offset(sz.Sub(d.Size).Div(2)).Push(gtx.Ops)
						call.Add(gtx.Ops)
						oo.Pop()
						defer clip.Rect{Max: sz}.Push(gtx.Ops).Pop()
						pointer.CursorPointer.Add(gtx.Ops)
						return gl.Dimensions{Size: sz}
					})
				})
			}
			return hstack(gtx, 2, ws...)
		})
	}
}

func (p *Page) shortcuts() []section {
	b := p.s.Keys
	preset := config.Preset(b.Preset)
	holdName := func(m key.Modifiers) string {
		if m == 0 {
			return ""
		}
		return strings.TrimSuffix(config.Chord{Mods: m}.String(), "+")
	}
	general := section{rows: []row{
		{label: "Preset", desc: "conventional follows Linux terminals (Ghostty, kitty, GNOME Terminal); aide is aide's Alt-key layout. Shortcuts you changed stay changed.",
			extra: "keys layout conventional aide", control: p.segmented("preset", config.Presets, b.Preset, func(o string) {
				p.saveValue("keys", "preset", config.Quote(o))
			})},
	}}
	if !config.SwitcherHidden {
		general.rows = append(general.rows, row{label: "Switcher hold key", desc: "Holding it shows every tab; the tab keys then walk them all.", extra: "modifier alt super ctrl switcher",
			control: p.segmented("hold", []string{"", "Alt", "Super", "Ctrl"}, holdName(b.Hold), func(o string) {
				if o == holdName(preset.Hold) {
					p.save("keys", "switcher_modifier", nil)
				} else {
					p.saveValue("keys", "switcher_modifier", config.Quote(o))
				}
			})})
	}
	var global, tab []row
	for _, a := range config.Actions() {
		cs := chordsOf(b, a.Name, a.Tab)
		var names []string
		for _, c := range cs {
			names = append(names, c.String())
		}
		r := row{label: firstSentence(a.Doc), desc: a.Name, extra: a.Doc + " " + strings.Join(names, " "),
			control: p.chords(a), below: p.below(a)}
		if a.Tab {
			r.label = "Tab mode: " + strings.ToLower(r.label[:1]) + r.label[1:]
			tab = append(tab, r)
		} else {
			global = append(global, r)
		}
	}
	tabDesc := "Off: give tab_prefix a shortcut above to use these."
	if cs := b.Global["tab_prefix"]; len(cs) > 0 {
		tabDesc = "After " + cs[0].String() + ", one key runs one of these."
	}
	return []section{general, {title: "Shortcuts", rows: global}, {title: "Tab mode", desc: tabDesc, rows: tab}}
}

// chords is an action's control: a keycap per chord (click to record), +
// to add one, and a reset arrow when it differs from the preset.
func (p *Page) chords(a config.Action) gl.Widget {
	return func(gtx gl.Context) gl.Dimensions {
		th := p.th
		b := p.s.Keys
		cs := chordsOf(b, a.Name, a.Tab)
		id := fmt.Sprint("k:", a.Tab, ":", a.Name)
		recording := p.rec.action == a.Name && p.rec.tab == a.Tab
		var ws []gl.Widget
		cap := func(i int, label string) {
			c := p.btn(fmt.Sprint(id, ":", i))
			for c.Clicked(gtx) {
				p.startRecord(slot{a.Name, a.Tab, i})
			}
			hot := recording && p.rec.index == i
			if hot {
				label = "Press keys…"
			}
			ws = append(ws, func(gtx gl.Context) gl.Dimensions {
				return c.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
					d := p.keycap(gtx, label, hot, c.Hovered())
					defer clip.Rect{Max: d.Size}.Push(gtx.Ops).Pop()
					pointer.CursorPointer.Add(gtx.Ops)
					return d
				})
			})
		}
		for i, c := range cs {
			cap(i, c.String())
		}
		if recording && p.rec.index < 0 {
			cap(-1, "")
		}
		if len(ws) == 0 {
			ws = append(ws, func(gtx gl.Context) gl.Dimensions {
				return p.text(gtx, th.UIFont, p.sp(12), th.Muted, "Unbound")
			})
		}
		add := p.btn(id + ":add")
		for add.Clicked(gtx) {
			p.startRecord(slot{a.Name, a.Tab, -1})
		}
		ws = append(ws, func(gtx gl.Context) gl.Dimensions { return p.iconButton(gtx, add, plusGlyph) })
		def := presetChords(b.Preset, a.Name, a.Tab)
		for p.btn(id + ":reset").Clicked(gtx) {
			p.write(edit{a.Name, a.Tab, def})
		}
		ws = append(ws, p.resetSlot(id+":reset", !slices.Equal(cs, def)))
		return hstack(gtx, 6, ws...)
	}
}

func (p *Page) actionLabel(name string, tab bool) string {
	for _, a := range config.Actions() {
		if a.Name == name && a.Tab == tab {
			return firstSentence(a.Doc)
		}
	}
	return name
}

// below is the recording hint or the conflict prompt under an action.
func (p *Page) below(a config.Action) gl.Widget {
	th := p.th
	if p.rec.action == a.Name && p.rec.tab == a.Tab {
		return func(gtx gl.Context) gl.Dimensions {
			return p.para(gtx, th.UIFont, p.sp(12), th.Primary, "Press the new shortcut. Esc cancels, Backspace removes this one.")
		}
	}
	c := p.conflict
	if c == nil || c.action != a.Name || c.tab != a.Tab {
		return nil
	}
	return func(gtx gl.Context) gl.Dimensions {
		for p.btn("swap").Clicked(gtx) {
			p.write(record(p.s.Keys, c.slot, c.chord, true)...)
			p.conflict = nil
		}
		for p.btn("nope").Clicked(gtx) {
			p.conflict = nil
		}
		other := p.actionLabel(c.other, c.tab)
		msg := c.chord.String() + " already runs “" + other + "”. "
		if cur := chordsOf(p.s.Keys, c.action, c.tab); c.index >= 0 && c.index < len(cur) {
			msg += "Swap gives it " + cur[c.index].String() + "."
		} else {
			msg += "Swap takes it from there."
		}
		return boxed(gtx, theme.Mix(th.SurfaceSecondary, th.Yellow, 0.08), theme.Mix(th.SurfaceSecondary, th.Yellow, 0.35), gtx.Dp(6), image.Pt(gtx.Dp(10), gtx.Dp(6)), func(gtx gl.Context) gl.Dimensions {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			return gl.Flex{Alignment: gl.Middle}.Layout(gtx,
				gl.Flexed(1, func(gtx gl.Context) gl.Dimensions {
					return p.para(gtx, th.UIFont, p.sp(12), th.Fg, msg)
				}),
				gl.Rigid(gl.Spacer{Width: 12}.Layout),
				gl.Rigid(func(gtx gl.Context) gl.Dimensions {
					return hstack(gtx, 6,
						func(gtx gl.Context) gl.Dimensions { return p.button(gtx, p.btn("nope"), ghost, "Cancel") },
						func(gtx gl.Context) gl.Dimensions { return p.button(gtx, p.btn("swap"), primary, "Swap") },
					)
				}),
			)
		})
	}
}

func (p *Page) terminal() []section {
	return []section{{rows: []row{
		{label: "Scrollback", desc: "Lines of history each pane keeps. Fixed in this version.", extra: "history lines buffer",
			control: func(gtx gl.Context) gl.Dimensions {
				return p.text(gtx, p.th.UIFont, p.sp(13), p.th.Fg, "10,000 lines")
			}},
		{label: "Colors", desc: "From the theme. Set them under [theme.terminal] in config.toml, or in a custom theme.", extra: "palette ansi colour",
			control: p.palette},
	}}}
}

// palette shows the terminal's background and the normal ANSI colors over
// its foreground and the bright ones.
func (p *Page) palette(gtx gl.Context) gl.Dimensions {
	th := p.th
	s := gtx.Dp(14)
	g := gtx.Dp(3)
	cell := func(i int) image.Rectangle {
		x, y := i%9, i/9
		return image.Rect(x*(s+g), y*(s+g), x*(s+g)+s, y*(s+g)+s)
	}
	cols := append([]color.NRGBA{th.TermBg}, th.ANSI[:8]...)
	cols = append(append(cols, th.TermFg), th.ANSI[8:]...)
	for i, c := range cols {
		r := cell(i)
		rrect(gtx, th.Border, r, gtx.Dp(3))
		rrect(gtx, c, r.Inset(1), gtx.Dp(3)-1)
	}
	return gl.Dimensions{Size: image.Pt(9*(s+g)-g, 2*(s+g)-g)}
}

func (p *Page) agents() []section {
	th := p.th
	var rows []row
	for _, h := range p.hooks {
		status, col := "Not installed", th.Muted
		switch {
		case h.Err != "":
			status, col = "Unreadable", th.Red
		case h.Have > 0 && h.Have == h.Want:
			status, col = "Installed", th.Green
		case h.Have > 0:
			status, col = fmt.Sprintf("Partly (%d of %d events)", h.Have, h.Want), th.Yellow
		}
		desc := shortPath(h.Path)
		if h.Err != "" {
			desc += ": " + h.Err
		}
		if h.Agent == "Codex" {
			desc += ". Codex asks you to trust them once with /hooks."
		}
		rows = append(rows, row{label: h.Agent + " hooks", desc: desc, extra: "hooks status install agent",
			control: func(gtx gl.Context) gl.Dimensions {
				return hstack(gtx, 8,
					func(gtx gl.Context) gl.Dimensions {
						d := gtx.Dp(8)
						rrect(gtx, col, image.Rect(0, 0, d, d), d/2)
						return gl.Dimensions{Size: image.Pt(d, d)}
					},
					func(gtx gl.Context) gl.Dimensions { return p.text(gtx, th.UIFont, p.sp(13), th.Fg, status) },
				)
			}})
	}
	const cmd = "pitwall hooks install"
	rows = append(rows, row{label: "Install or update", desc: "Run this in a terminal; it adds pitwall's hooks to both files and keeps a backup. This page only reads them.",
		extra: "hooks command", control: func(gtx gl.Context) gl.Dimensions {
			c := p.btn("copy-hooks")
			for c.Clicked(gtx) {
				p.copy(gtx, "copy-hooks", cmd)
			}
			return hstack(gtx, 8,
				func(gtx gl.Context) gl.Dimensions {
					return boxed(gtx, th.Bg, th.Border, gtx.Dp(6), image.Pt(gtx.Dp(10), gtx.Dp(5)), func(gtx gl.Context) gl.Dimensions {
						return p.text(gtx, th.MonoFont, p.sp(12), th.Fg, cmd)
					})
				},
				func(gtx gl.Context) gl.Dimensions {
					return p.button(gtx, c, secondary, p.copyLabel("copy-hooks", "Copy"))
				},
			)
		}})
	return []section{{rows: rows}}
}

func (p *Page) copyLabel(id, label string) string {
	if p.copied == id {
		return "Copied"
	}
	return label
}

func (p *Page) about() []section {
	th := p.th
	path := p.s.Path
	if path == "" {
		path = config.Path()
	}
	link := func(id, label, url string) gl.Widget {
		return func(gtx gl.Context) gl.Dimensions {
			c := p.btn(id)
			for c.Clicked(gtx) {
				open(url)
			}
			return p.button(gtx, c, secondary, label)
		}
	}
	return []section{{rows: []row{
		{label: "Version", extra: "release build", control: func(gtx gl.Context) gl.Dimensions {
			return p.text(gtx, th.MonoFont, p.sp(13), th.Fg, p.ver)
		}},
		{label: "Config file", desc: shortPath(path), extra: "config.toml path editor", control: func(gtx gl.Context) gl.Dimensions {
			c := p.btn("copy-path")
			for c.Clicked(gtx) {
				p.copy(gtx, "copy-path", path)
			}
			return hstack(gtx, 8,
				link("edit", "Open in editor", path),
				func(gtx gl.Context) gl.Dimensions {
					return p.button(gtx, c, secondary, p.copyLabel("copy-path", "Copy path"))
				},
			)
		}},
		{label: "Documentation", desc: "The README covers every option; the changelog lists what each release changed.", extra: "readme changelog help docs github",
			control: func(gtx gl.Context) gl.Dimensions {
				return hstack(gtx, 8, link("readme", "README", repo+"#readme"), link("changelog", "Changelog", repo+"/blob/main/CHANGELOG.md"))
			}},
	}}}
}

func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}
