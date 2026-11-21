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

// SlashMatches is cmds whose name has query in it, ignoring case: the
// names that start with it first, then the rest, each in cmds' order.
func SlashMatches(cmds []session.Command, query string) []session.Command {
	q := strings.ToLower(query)
	var first, rest []session.Command
	for _, c := range cmds {
		n := strings.ToLower(c.Name)
		switch {
		case strings.HasPrefix(n, q):
			first = append(first, c)
		case strings.Contains(n, q):
			rest = append(rest, c)
		}
	}
	return append(first, rest...)
}

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
	s.rows, s.note = SlashMatches(env.Snap.Commands, q), ""
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
	o.Pop()
	op.Defer(gtx.Ops, rec.Stop())
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
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp("/" + cmd.Name).Add(gtx.Ops)
		h, w := gtx.Dp(28), gtx.Constraints.Max.X
		rect := image.Rectangle{Max: image.Pt(w, h)}
		fill := theme.Transparent
		switch {
		case b.Pressed():
			fill = th.Pressed
		case on:
			fill = th.SelectedBg
		case b.Hovered():
			fill = th.Hover
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		pad, gap := gtx.Dp(theme.SpaceS), gtx.Dp(theme.SpaceM)
		inner := w - 2*pad
		noteW := 0
		if cmd.Terminal {
			m := op.Record(gtx.Ops)
			nd := text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, terminalNote)
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
				return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, cmd.Description)
			})
		o.Pop()
		return layout.Dimensions{Size: rect.Max}
	})
}

// nameShare is the most of a row the name and its hint take, so a long
// name leaves its description room.
const nameShare = 0.45
