package chatin

import (
	"image"
	"slices"
	"strings"
	"unicode"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// SlashMenu is the / menu of a prompt's editor: the agent's commands
// that match the /word at the caret and the highlighted one, or a note
// when none shows. It opens and narrows on each keystroke, so it has no
// fade. The chat composer has one; another editor that writes a prompt
// for an agent calls Events, Update and Layout.
type SlashMenu struct {
	rows   []session.Command
	sel    int    // the highlighted row
	picked string // its name, which keeps the highlight when the list is read again
	query  string
	start  int    // the word's rune index in the draft
	note   string // shown in place of rows when there are none; it takes no keys but Esc
	shut   string // the text Esc closed the menu on; it stays shut until the text changes
	anchor image.Rectangle
	tag    int // the menu's own press target
	// hovered is the row under the pointer this frame, and was last
	// frame's: the pointer moving onto a row highlights it, a pointer
	// resting on one leaves the keys' highlight alone.
	hovered, was string
	full         string                       // the highlighted row's description when the row cuts it, shown whole above the menu
	items        map[string]*widget.Clickable // the rows' buttons, by command name
}

// The menu's notes: a command that runs in the agent's terminal, and
// what shows in place of rows.
const (
	terminalNote = "opens in the terminal"
	noMatchNote  = "No matching command"
	noListNote   = "No commands yet"
	oldNote      = "This daemon sends no commands; restart it to update"
)

// open reports whether the menu shows rows, which take the keys.
func (s *SlashMenu) open() bool { return len(s.rows) > 0 }

// shown reports whether the menu shows at all: rows or a note.
func (s *SlashMenu) shown() bool { return s.open() || s.note != "" }

// Shown reports whether the menu shows, rows or a note.
func (s *SlashMenu) Shown() bool { return s.shown() }

// Rows is the commands the menu lists.
func (s *SlashMenu) Rows() []session.Command { return s.rows }

// SlashMatches is cmds that match query, ignoring case, best first: a
// name or alias that is the query, then one that starts with it, then
// one with a word (after - _ : or a space) that does, then one that has
// it inside, then a name with its letters in order, then a description
// that has it, these two only for a query of three letters or more.
// Within each, the commands used more come first (used, by name), then
// cmds' own order.
//
// Adapted from herdr-chat-plus (MIT): mods/chat/commands.js (matchCommands)
// Adapted from T3 Code (MIT): apps/web/src/components/chat/composerSlashCommandSearch.ts
func SlashMatches(cmds []session.Command, query string, used map[string]int) []session.Command {
	q := strings.ToLower(query)
	type match struct {
		c    session.Command
		tier int
	}
	var ms []match
	for _, c := range cmds {
		if t := slashTier(c, q); t >= 0 {
			ms = append(ms, match{c, t})
		}
	}
	slices.SortStableFunc(ms, func(a, b match) int {
		if a.tier != b.tier {
			return a.tier - b.tier
		}
		return used[b.c.Name] - used[a.c.Name]
	})
	out := make([]session.Command, len(ms))
	for i, m := range ms {
		out[i] = m.c
	}
	return out
}

// slashTier is how well c matches q (see SlashMatches), -1 for not.
func slashTier(c session.Command, q string) int {
	names := append([]string{c.Name}, c.Aliases...)
	best := -1
	for i, n := range names {
		n = strings.ToLower(n)
		t := -1
		switch {
		case n == q:
			t = 0
		case strings.HasPrefix(n, q):
			t = 1
		case slices.ContainsFunc(strings.FieldsFunc(n, func(r rune) bool { return strings.ContainsRune("-_: ", r) }), func(w string) bool { return strings.HasPrefix(w, q) }):
			t = 2
		case strings.Contains(n, q):
			t = 3
		case i == 0 && len(q) >= 3 && subsequence(n, q):
			t = 4
		}
		if t >= 0 && (best < 0 || t < best) {
			best = t
		}
	}
	if best < 0 && len(q) >= 3 && strings.Contains(strings.ToLower(c.Description), q) {
		best = 5
	}
	return best
}

// subsequence reports whether q's letters appear in s in order.
func subsequence(s, q string) bool {
	for _, r := range q {
		i := strings.IndexRune(s, r)
		if i < 0 {
			return false
		}
		s = s[i+len(string(r)):]
	}
	return true
}

// slashUsed counts the commands this window sent or picked, by name,
// which SlashMatches ranks by. It lasts as long as the window.
var slashUsed = map[string]int{}

// slashUpdate opens, narrows or closes the menu for the draft's /word.
func (c *Composer) slashUpdate(env Env) {
	_, caret := c.ed.Selection()
	var cmds []session.Command
	var p model.Provider
	if env.Snap != nil {
		cmds, p = env.Snap.Commands, env.Snap.Provider
	}
	c.slash.update(c.Text(), caret, cmds, p, env.Commands, env.Snap == nil || c.menu)
}

// update opens, narrows or closes the menu for the /word at rune caret
// of txt, listing cmds of an agent of provider p; known is whether the
// daemon sends the commands, off keeps the menu closed. A new query
// highlights the first row; the same query keeps the row it highlighted
// when the list is read again.
func (s *SlashMenu) update(txt string, caret int, cmds []session.Command, p model.Provider, known, off bool) {
	start, word, ok := Slash(txt, caret)
	if !ok || off || txt == s.shut {
		s.rows, s.note = nil, ""
		if txt != s.shut {
			s.shut = ""
		}
		return
	}
	q := strings.TrimPrefix(word, "/")
	if strings.ContainsAny(q, "/\\") { // why: a second slash makes the word a path, as /tmp/x (T3 Code's isPathWord)
		s.rows, s.note = nil, ""
		return
	}
	if start > 0 {
		if !midPrompt(p) { // why: Codex names a skill with $, so a /word past the first is text to it
			s.rows, s.note = nil, ""
			return
		}
		cmds = MidCommands(cmds)
	}
	s.start = start
	s.rows, s.note = SlashMatches(cmds, q, slashUsed), ""
	switch {
	case q != s.query:
		s.sel = 0
	case s.picked != "":
		if i := slices.IndexFunc(s.rows, func(r session.Command) bool { return r.Name == s.picked }); i >= 0 {
			s.sel = i
		}
	}
	s.query = q
	s.sel = min(s.sel, max(0, len(s.rows)-1))
	s.picked = ""
	if s.open() {
		s.picked = s.rows[s.sel].Name
	}
	switch {
	case s.open():
	case !known:
		s.note = oldNote
	case len(cmds) == 0:
		s.note = noListNote
	default:
		s.note = noMatchNote
	}
}

// midPrompt reports whether provider p reads a /skill that is not the
// prompt's first word.
func midPrompt(p model.Provider) bool {
	return p == "" || p == model.ProviderClaude || p == model.ProviderAntigravity
}

// MidCommands is the commands a provider reads in the middle of a
// prompt: skills and custom commands, never a built-in.
func MidCommands(cmds []session.Command) []session.Command {
	return slices.DeleteFunc(slices.Clone(cmds), func(c session.Command) bool {
		return c.Terminal || (c.Source != "user" && c.Source != "project" && c.Source != "plugin")
	})
}

// key runs key e on the open menu of ed, whose text is txt: Up and
// Down move the highlight, Enter or Tab fills the command in, Esc closes
// the menu.
func (s *SlashMenu) key(gtx layout.Context, ed *widget.Editor, txt string, e key.Event) {
	switch e.Name {
	case key.NameUpArrow:
		s.sel = (s.sel + len(s.rows) - 1) % len(s.rows)
	case key.NameDownArrow:
		s.sel = (s.sel + 1) % len(s.rows)
	case key.NameEscape:
		s.shut, s.rows, s.note = txt, nil, ""
		return
	default:
		s.pick(gtx, ed, txt, s.rows[s.sel].Name)
		return
	}
	s.picked = s.rows[s.sel].Name
}

// pick puts /name and a space in place of the /word at ed's caret; txt
// is ed's text.
func (s *SlashMenu) pick(gtx layout.Context, ed *widget.Editor, txt, name string) {
	r := []rune(txt)
	start := min(s.start, len(r))
	end := start
	for end < len(r) && !unicode.IsSpace(r[end]) {
		end++
	}
	if end < len(r) && r[end] == ' ' {
		end++ // the space after the word, which the pick puts back
	}
	ed.SetCaret(start, end)
	ed.Insert("/" + name + " ")
	s.rows, s.note = nil, ""
	gtx.Execute(key.FocusCmd{Tag: ed})
}

// click is the button of the row of command name.
func (s *SlashMenu) click(name string) *widget.Clickable {
	if s.items == nil {
		s.items = map[string]*widget.Clickable{}
	}
	b := s.items[name]
	if b == nil {
		b = new(widget.Clickable)
		s.items[name] = b
	}
	return b
}

// clicks picks a row the pointer clicked, into ed, whose text is txt.
func (s *SlashMenu) clicks(gtx layout.Context, ed *widget.Editor, txt string) {
	for _, cmd := range s.rows {
		if s.click(cmd.Name).Clicked(gtx) {
			s.pick(gtx, ed, txt, cmd.Name)
			return
		}
	}
}

// anchorAt notes where the /word's / sits, from ed laid out at off.
func (s *SlashMenu) anchorAt(ed *widget.Editor, off image.Point) {
	if !s.shown() {
		return
	}
	if rs := ed.Regions(s.start, s.start+1, nil); len(rs) > 0 {
		s.anchor = rs[0].Bounds.Add(off)
	}
}

// Events picks a row the pointer clicked and runs the keys the shown
// menu takes from ed: Up, Down, Tab, Enter and Esc. Call it before
// anything else reads ed's keys, ed.Update included, so Enter picks
// rather than sends or adds a line and Esc closes only the menu.
func (s *SlashMenu) Events(gtx layout.Context, ed *widget.Editor) {
	s.clicks(gtx, ed, ed.Text())
	for s.shown() {
		fs := []event.Filter{key.Filter{Focus: ed, Name: key.NameEscape}}
		if s.open() {
			for _, n := range []key.Name{key.NameUpArrow, key.NameDownArrow, key.NameTab, key.NameReturn, key.NameEnter} {
				fs = append(fs, key.Filter{Focus: ed, Name: n})
			}
		}
		ev, ok := gtx.Event(fs...)
		if !ok {
			return
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			if s.open() {
				s.key(gtx, ed, ed.Text(), e)
			} else {
				s.shut, s.note = ed.Text(), ""
			}
		}
	}
}

// Update opens, narrows or closes the menu for the /word at ed's caret,
// listing cmds of an agent of provider p. No commands, or ed without key
// focus, closes it. Call it once ed took this frame's keys.
func (s *SlashMenu) Update(gtx layout.Context, ed *widget.Editor, cmds []session.Command, p model.Provider) {
	_, caret := ed.Selection()
	s.update(ed.Text(), caret, cmds, p, true, len(cmds) == 0 || !gtx.Focused(ed))
}

// Layout draws the shown menu over ed, which is laid out at the origin
// with size sz, as wide as ed: above it, or below it when bounds, the
// room it may take in the same coordinates, has none above.
func (s *SlashMenu) Layout(gtx layout.Context, th *theme.Theme, ed *widget.Editor, sz image.Point, bounds image.Rectangle) {
	if !s.shown() {
		return
	}
	s.anchorAt(ed, image.Point{})
	bounds.Min.X, bounds.Max.X = 0, sz.X
	s.pop(gtx, th, image.Rectangle{Max: sz}, bounds)
}

// pop is the menu above area, or below it when bounds has no room
// above, placed as the @ popover is. Its width stays the same whatever
// it lists, so narrowing it moves no column.
func (s *SlashMenu) pop(gtx layout.Context, th *theme.Theme, area, bounds image.Rectangle) {
	rec := op.Record(gtx.Ops)
	pw := min(gtx.Dp(560), area.Dx())
	pg := gtx
	pg.Constraints = layout.Constraints{Min: image.Pt(pw, 0), Max: image.Pt(pw, gtx.Constraints.Max.Y)}
	body := op.Record(gtx.Ops)
	d := layout.UniformInset(theme.SpaceXS).Layout(pg, func(gtx layout.Context) layout.Dimensions {
		if !s.open() {
			return s.noteLine(gtx, th)
		}
		first := max(0, min(s.sel-mentionRows+1, len(s.rows)-mentionRows))
		if s.sel < first {
			first = s.sel
		}
		shown := s.rows[first:min(len(s.rows), first+mentionRows)]
		s.hovered, s.full = "", ""
		var kids []layout.FlexChild
		for i, cmd := range shown {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return s.row(gtx, th, cmd, first+i == s.sel)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
	call := body.Stop()
	gap := gtx.Dp(theme.SpaceXS)
	at := kit.Place(s.anchor, d.Size, bounds, kit.AboveStart, gap, 0)
	y, below := area.Min.Y-d.Size.Y-gap, false
	if y < bounds.Min.Y {
		y, below = area.Max.Y+gap, true
	}
	o := op.Offset(image.Pt(at.X, y)).Push(gtx.Ops)
	rect := image.Rectangle{Max: d.Size}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	cl := clip.UniformRRect(rect, gtx.Dp(theme.RadiusPopover)).Push(gtx.Ops)
	event.Op(gtx.Ops, &s.tag) // why: a press on the menu goes no further
	call.Add(gtx.Ops)
	cl.Pop()
	if s.full != "" {
		s.fullCard(gtx, th, d.Size.X, d.Size.Y, below)
	}
	o.Pop()
	op.Defer(gtx.Ops, rec.Stop())
	if s.hover() {
		gtx.Execute(op.InvalidateCmd{}) // the rows drew with the old highlight
	}
}

// hover moves the highlight to the row the pointer moved onto this
// frame, and reports whether it did; a pointer that rests where it was
// leaves the keys' highlight alone.
func (s *SlashMenu) hover() bool {
	if s.hovered == s.was {
		return false
	}
	s.was = s.hovered
	i := slices.IndexFunc(s.rows, func(r session.Command) bool { return r.Name == s.hovered })
	if i < 0 || i == s.sel {
		return false
	}
	s.sel, s.picked = i, s.hovered
	return true
}

// fullCard is the highlighted command's whole description, wrapped,
// in a card of the menu's width w right above it, or under it, h high,
// when it opened below its editor, as T3 Code's tooltip on a cut
// description. The menu's rows do not move for it.
func (s *SlashMenu) fullCard(gtx layout.Context, th *theme.Theme, w, h int, below bool) {
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Constraints.Max.Y)}
	m := op.Record(gtx.Ops)
	d := layout.UniformInset(theme.SpaceS).Layout(g, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return widget.Label{MaxLines: 4}.Layout(gtx, th.Shaper, th.UIFont, th.Sp(theme.Small), s.full, colorOp(gtx, th.Fg))
	})
	call := m.Stop()
	y := -d.Size.Y - gtx.Dp(theme.SpaceXS)
	if below {
		y = h + gtx.Dp(theme.SpaceXS)
	}
	o := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
	rect := image.Rectangle{Max: image.Pt(w, d.Size.Y)}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	call.Add(gtx.Ops)
	o.Pop()
}

// noteLine is the menu's one muted line when it lists no command.
func (s *SlashMenu) noteLine(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	h, w := gtx.Dp(28), gtx.Constraints.Max.X
	pad := gtx.Dp(theme.SpaceS)
	g := gtx
	g.Constraints.Max.X = max(0, w-2*pad)
	m := op.Record(gtx.Ops)
	d := text(g, th, th.UIFont, th.Sp(theme.Small), th.Muted, s.note)
	call := m.Stop()
	o := op.Offset(image.Pt(pad, (h-d.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// row is one command: /name in mono with its argument hint muted,
// taking at most nameShare of the row, then its description muted in
// what is left, and the terminal note at the right on one that opens
// there. Each part is one line, cut with an ellipsis.
func (s *SlashMenu) row(gtx layout.Context, th *theme.Theme, cmd session.Command, on bool) layout.Dimensions {
	b := s.click(cmd.Name)
	return kit.Clickable(gtx, b, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp("/" + cmd.Name).Add(gtx.Ops)
		if b.Hovered() {
			s.hovered = cmd.Name
		}
		h, w := gtx.Dp(28), gtx.Constraints.Max.X
		rect := image.Rectangle{Max: image.Pt(w, h)}
		fill := theme.Transparent
		switch {
		case b.Pressed():
			fill = th.Pressed
		case on:
			fill = th.SelectedBg
		case b.Hovered():
			fill = th.HoverElevated
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		pad, gap := gtx.Dp(theme.SpaceS), gtx.Dp(theme.SpaceM)
		inner := w - 2*pad
		noteW := 0
		if note := rowNote(cmd); note != "" {
			m := op.Record(gtx.Ops)
			nd := text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, note)
			note := m.Stop()
			noteW = nd.Size.X + gap
			o := op.Offset(image.Pt(w-pad-nd.Size.X, (h-nd.Size.Y)/2)).Push(gtx.Ops)
			note.Add(gtx.Ops)
			o.Pop()
		}
		g := gtx
		g.Constraints.Max.X = max(0, inner-noteW)
		o := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
		row(g, gap, h,
			func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X, int(nameShare*float32(inner)))
				name := text(gtx, th, th.MonoFont, th.Sp(theme.Body), th.Fg, "/"+cmd.Name)
				if cmd.Hint == "" || name.Size.X+gap >= gtx.Constraints.Max.X {
					return name
				}
				hg := gtx
				hg.Constraints.Max.X -= name.Size.X + gtx.Dp(theme.SpaceXS)
				m := op.Record(gtx.Ops)
				hd := text(hg, th, th.MonoFont, th.Sp(theme.Small), th.Muted, cmd.Hint)
				hint := m.Stop()
				ho := op.Offset(image.Pt(name.Size.X+gtx.Dp(theme.SpaceXS), name.Baseline-hd.Baseline)).Push(gtx.Ops)
				hint.Add(gtx.Ops)
				ho.Pop()
				return layout.Dimensions{Size: image.Pt(name.Size.X+gtx.Dp(theme.SpaceXS)+hd.Size.X, name.Size.Y), Baseline: name.Baseline}
			},
			func(gtx layout.Context) layout.Dimensions {
				if on && cmd.Description != "" {
					g := gtx
					g.Constraints.Max.X = 1 << 20
					m := op.Record(gtx.Ops)
					if text(g, th, th.UIFont, th.Sp(theme.Small), th.Muted, cmd.Description).Size.X > gtx.Constraints.Max.X {
						s.full = cmd.Description
					}
					m.Stop()
				}
				return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, cmd.Description)
			})
		o.Pop()
		return layout.Dimensions{Size: rect.Max}
	})
}

// rowNote is the quiet note at a row's right: that the command opens
// the agent's terminal, else where it comes from, as T3 Code's source
// badge says; "" for a built-in.
func rowNote(c session.Command) string {
	switch {
	case c.Terminal:
		return terminalNote
	case c.Source == "user":
		return "Personal"
	case c.Source == "project":
		return "Project"
	case c.Source == "plugin":
		return "Plugin"
	}
	return ""
}

// nameShare is the most of a row the name and its hint take, so a long
// name leaves its description room.
const nameShare = 0.45
