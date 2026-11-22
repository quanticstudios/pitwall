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

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// slashState is the / menu: the chat's commands that match the draft's
// first word and the highlighted one, or a note when none shows. It
// opens and narrows on each keystroke, so it has no fade.
type slashState struct {
	rows   []session.Command
	sel    int    // the highlighted row
	picked string // its name, which keeps the highlight when the list is read again
	query  string
	note   string // shown in place of rows when there are none; it takes no keys but Esc
	shut   string // the text Esc closed the menu on; it stays shut until the text changes
	anchor image.Rectangle
	tag    int // the menu's own press target
	// hovered is the row under the pointer this frame, and was last
	// frame's: the pointer moving onto a row highlights it, a pointer
	// resting on one leaves the keys' highlight alone.
	hovered, was string
	full         string // the highlighted row's description when the row cuts it, shown whole above the menu
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
func (s *slashState) open() bool { return len(s.rows) > 0 }

// shown reports whether the menu shows at all: rows or a note.
func (s *slashState) shown() bool { return s.open() || s.note != "" }

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
// A new query highlights the first row; the same query keeps the row it
// highlighted when the chat's list is read again.
func (c *Composer) slashUpdate(env Env) {
	s := &c.slash
	txt := c.Text()
	word, ok := c.Slash()
	if !ok || env.Snap == nil || txt == s.shut || c.menu {
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
	s.rows, s.note = SlashMatches(env.Snap.Commands, q, slashUsed), ""
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
	case !env.Commands:
		s.note = oldNote
	case len(env.Snap.Commands) == 0:
		s.note = noListNote
	default:
		s.note = noMatchNote
	}
}

// slashKey runs key e on the open menu: Up and Down move the highlight,
// Enter or Tab fills the command in, Esc closes the menu.
func (c *Composer) slashKey(gtx layout.Context, e key.Event) {
	s := &c.slash
	switch e.Name {
	case key.NameUpArrow:
		s.sel = (s.sel + len(s.rows) - 1) % len(s.rows)
	case key.NameDownArrow:
		s.sel = (s.sel + 1) % len(s.rows)
	case key.NameEscape:
		s.shut, s.rows, s.note = c.Text(), nil, ""
		return
	default:
		c.pickSlash(gtx, s.rows[s.sel].Name)
		return
	}
	s.picked = s.rows[s.sel].Name
}

// pickSlash puts /name and a space in place of the draft's first word.
func (c *Composer) pickSlash(gtx layout.Context, name string) {
	r := []rune(c.Text())
	end := 0
	for end < len(r) && !unicode.IsSpace(r[end]) {
		end++
	}
	if end < len(r) && r[end] == ' ' {
		end++ // the space after the word, which the pick puts back
	}
	c.ed.SetCaret(0, end)
	c.ed.Insert("/" + name + " ")
	c.slash.rows, c.slash.note = nil, ""
	gtx.Execute(key.FocusCmd{Tag: &c.ed})
}

// slashClicks picks a row the pointer clicked.
func (c *Composer) slashClicks(gtx layout.Context) {
	for _, cmd := range c.slash.rows {
		if c.click("slash:" + cmd.Name).Clicked(gtx) {
			c.pickSlash(gtx, cmd.Name)
			return
		}
	}
}

// slashAnchor notes where the draft's / sits in the card, from the
// editor laid out at off in it.
func (c *Composer) slashAnchor(off image.Point) {
	if !c.slash.shown() {
		return
	}
	if rs := c.ed.Regions(0, 1, nil); len(rs) > 0 {
		c.slash.anchor = rs[0].Bounds.Add(off)
	}
}

// slashPop is the menu above the draft's first line, in the card's
// coordinates, placed as the @ popover is. Its width stays the same
// whatever it lists, so narrowing it moves no column.
func (c *Composer) slashPop(gtx layout.Context, th *theme.Theme) {
	s := &c.slash
	rec := op.Record(gtx.Ops)
	pw := min(gtx.Dp(560), c.cardSize.X)
	pg := gtx
	pg.Constraints = layout.Constraints{Min: image.Pt(pw, 0), Max: image.Pt(pw, gtx.Constraints.Max.Y)}
	body := op.Record(gtx.Ops)
	d := layout.UniformInset(theme.SpaceXS).Layout(pg, func(gtx layout.Context) layout.Dimensions {
		if !s.open() {
			return c.slashNote(gtx, th)
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
				return c.slashRow(gtx, th, cmd, first+i == s.sel)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
	call := body.Stop()
	bounds := image.Rectangle{Min: image.Pt(0, -1<<20), Max: c.cardSize}
	at := kit.Place(s.anchor, d.Size, bounds, kit.AboveStart, gtx.Dp(theme.SpaceXS), 0)
	o := op.Offset(image.Pt(at.X, -d.Size.Y-gtx.Dp(theme.SpaceXS))).Push(gtx.Ops)
	rect := image.Rectangle{Max: d.Size}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	cl := clip.UniformRRect(rect, gtx.Dp(theme.RadiusPopover)).Push(gtx.Ops)
	event.Op(gtx.Ops, &s.tag) // why: a press on the menu goes no further
	call.Add(gtx.Ops)
	cl.Pop()
	if s.full != "" {
		c.slashFull(gtx, th, d.Size.X)
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
func (s *slashState) hover() bool {
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

// slashFull is the highlighted command's whole description, wrapped,
// in a card of the menu's width w right above it, as T3 Code's tooltip
// on a cut description. The menu's rows do not move for it.
func (c *Composer) slashFull(gtx layout.Context, th *theme.Theme, w int) {
	g := gtx
	g.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Constraints.Max.Y)}
	m := op.Record(gtx.Ops)
	d := layout.UniformInset(theme.SpaceS).Layout(g, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return widget.Label{MaxLines: 4}.Layout(gtx, th.Shaper, th.UIFont, th.Sp(theme.Small), c.slash.full, colorOp(gtx, th.Fg))
	})
	call := m.Stop()
	o := op.Offset(image.Pt(0, -d.Size.Y-gtx.Dp(theme.SpaceXS))).Push(gtx.Ops)
	rect := image.Rectangle{Max: image.Pt(w, d.Size.Y)}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	call.Add(gtx.Ops)
	o.Pop()
}

// slashNote is the menu's one muted line when it lists no command.
func (c *Composer) slashNote(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	h, w := gtx.Dp(28), gtx.Constraints.Max.X
	pad := gtx.Dp(theme.SpaceS)
	g := gtx
	g.Constraints.Max.X = max(0, w-2*pad)
	m := op.Record(gtx.Ops)
	d := text(g, th, th.UIFont, th.Sp(theme.Small), th.Muted, c.slash.note)
	call := m.Stop()
	o := op.Offset(image.Pt(pad, (h-d.Size.Y)/2)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	o.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// slashRow is one command: /name in mono with its argument hint muted,
// taking at most nameShare of the row, then its description muted in
// what is left, and the terminal note at the right on one that opens
// there. Each part is one line, cut with an ellipsis.
func (c *Composer) slashRow(gtx layout.Context, th *theme.Theme, cmd session.Command, on bool) layout.Dimensions {
	b := c.click("slash:" + cmd.Name)
	return kit.Clickable(gtx, b, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp("/" + cmd.Name).Add(gtx.Ops)
		if b.Hovered() {
			c.slash.hovered = cmd.Name
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
						c.slash.full = cmd.Description
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
