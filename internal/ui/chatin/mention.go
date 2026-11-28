package chatin

import (
	"image"
	"image/color"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// mentionRows is how many matches the @ popover shows at once.
const mentionRows = 8

// mentionState is the @ popover: the word it completes, its matches and
// the highlighted one, and the paths picked into the draft, which draw as
// chips.
type mentionState struct {
	start  int    // the @'s rune index in the text
	query  string // what follows the @ up to the caret
	rows   []string
	sel    int
	shut   string // the text Esc closed the popover on; it stays shut until the text changes
	picked map[string]bool
	anchor image.Rectangle // the @ in the card, as the last frame drew it
	t      anim.Value
	tag    int // the popover's own press target
}

// Mention is the @ word the caret (in runes) is in: the rune index of
// its @ and what follows it up to the caret. The @ starts the text or
// follows a space, so an email address is none; ok false otherwise.
func Mention(text string, caret int) (start int, query string, ok bool) {
	r := []rune(text)
	if caret < 1 || caret > len(r) {
		return 0, "", false
	}
	i := caret
	for i > 0 && !unicode.IsSpace(r[i-1]) {
		i--
	}
	if i == caret || r[i] != '@' {
		return 0, "", false
	}
	return i, string(r[i+1 : caret]), true
}

// MentionText is what picking path p puts in the draft: @p, quoted when
// it has a space, as Claude Code reads it; a space after a file, none
// after a folder, so the popover goes on into it.
func MentionText(p string) string {
	t := "@" + p
	if strings.ContainsAny(p, " \t") {
		t = `@"` + p + `"`
	}
	if !strings.HasSuffix(p, "/") {
		t += " "
	}
	return t
}

// open reports whether the popover shows.
func (m *mentionState) open() bool { return len(m.rows) > 0 }

// mentionUpdate opens, narrows or closes the popover for the caret's @
// word, with matches from env.Mentions.
func (c *Composer) mentionUpdate(gtx layout.Context, env Env) {
	m := &c.mention
	txt := c.Text()
	_, caret := c.ed.Selection()
	start, query, ok := Mention(txt, caret)
	if _, _, slash := Slash(txt, caret); !ok || slash || env.Mentions == nil || txt == m.shut || c.menu {
		m.rows = nil
		if txt != m.shut {
			m.shut = ""
		}
		return
	}
	was := m.open()
	if query != m.query || start != m.start || !was {
		m.sel = 0
	}
	m.start, m.query = start, query
	m.rows = env.Mentions(query)
	m.sel = min(m.sel, max(0, len(m.rows)-1))
	if !was && m.open() {
		m.t.Set(gtx.Now, 0, 0)
		m.t.Set(gtx.Now, 1, anim.Dialog)
	}
}

// mentionKey runs key e on the open popover: Up and Down move the
// highlight, Enter or Tab picks it, Esc closes the popover.
func (c *Composer) mentionKey(gtx layout.Context, e key.Event) {
	m := &c.mention
	switch e.Name {
	case key.NameUpArrow:
		m.sel = (m.sel + len(m.rows) - 1) % len(m.rows)
	case key.NameDownArrow:
		m.sel = (m.sel + 1) % len(m.rows)
	case key.NameEscape:
		m.shut, m.rows = c.Text(), nil
	default:
		c.pickMention(gtx, m.rows[m.sel])
	}
}

// pickMention puts @p in place of the @ word at the caret.
func (c *Composer) pickMention(gtx layout.Context, p string) {
	m := &c.mention
	r := []rune(c.Text())
	_, caret := c.ed.Selection()
	end := caret
	for end < len(r) && !unicode.IsSpace(r[end]) {
		end++
	}
	c.ed.SetCaret(m.start, end)
	c.ed.Insert(MentionText(p))
	if m.picked == nil {
		m.picked = map[string]bool{}
	}
	m.picked[strings.TrimSpace(MentionText(p))] = true
	m.rows = nil
	gtx.Execute(key.FocusCmd{Tag: &c.ed})
}

// mentionBefore is the rune index of the picked mention that ends right
// before caret in text, so Backspace takes it whole; -1 when none does.
func (c *Composer) mentionBefore(text string, caret int) int {
	r := []rune(text)
	if caret > len(r) {
		return -1
	}
	for tok := range c.mention.picked {
		n := len([]rune(tok))
		if s := caret - n; s >= 0 && string(r[s:caret]) == tok && (s == 0 || unicode.IsSpace(r[s-1])) {
			return s
		}
	}
	return -1
}

// mentionSpans calls fn with the rune span [s, e) of every whitespace-bounded
// occurrence of tok in txt without heap allocations.
func mentionSpans(txt, tok string, fn func(s, e int)) {
	if tok == "" || len(tok) > len(txt) {
		return
	}
	tokRunes := utf8.RuneCountInString(tok)
	bytePos, runePos := 0, 0
	for bytePos < len(txt) {
		i := strings.Index(txt[bytePos:], tok)
		if i < 0 {
			return
		}
		matchByte := bytePos + i
		runePos += utf8.RuneCountInString(txt[bytePos:matchByte])
		endByte := matchByte + len(tok)
		leftOK := matchByte == 0
		if !leftOK {
			prev, _ := utf8.DecodeLastRuneInString(txt[:matchByte])
			leftOK = unicode.IsSpace(prev)
		}
		rightOK := endByte == len(txt)
		if !rightOK {
			next, _ := utf8.DecodeRuneInString(txt[endByte:])
			rightOK = unicode.IsSpace(next)
		}
		if leftOK && rightOK {
			fn(runePos, runePos+tokRunes)
		}
		_, w := utf8.DecodeRuneInString(txt[matchByte:])
		bytePos = matchByte + w
		runePos++
	}
}

// mentionChips covers each picked @path in the editor with a chip, its
// label the same text in the chip's color on the line's baseline. One
// that wraps stays plain text.
func (c *Composer) mentionChips(gtx layout.Context, th *theme.Theme) {
	if len(c.mention.picked) == 0 {
		return
	}
	txt := c.Text()
	if !strings.Contains(txt, "@") {
		return
	}
	for tok := range c.mention.picked {
		mentionSpans(txt, tok, func(s, e int) { c.textChip(gtx, th, th.Blue, s, e, tok) })
	}
}

// slashChip covers the draft's leading /command with a chip when the
// agent has that command or skill, so it reads as one and not as prose.
func (c *Composer) slashChip(gtx layout.Context, th *theme.Theme, env Env) {
	if env.Snap == nil {
		return
	}
	txt := c.Text()
	tok, _, _ := strings.Cut(txt, " ")
	tok, _, _ = strings.Cut(tok, "\n")
	if KnownSlash(tok, env.Snap.Commands) {
		c.textChip(gtx, th, th.Purple, 0, utf8.RuneCountInString(tok), tok)
	}
}

// KnownSlash reports whether word is "/" and the name or an alias of one
// of cmds, ignoring case.
func KnownSlash(word string, cmds []session.Command) bool {
	name, ok := strings.CutPrefix(word, "/")
	if !ok || name == "" {
		return false
	}
	for _, c := range cmds {
		if strings.EqualFold(c.Name, name) || slices.ContainsFunc(c.Aliases, func(a string) bool { return strings.EqualFold(a, name) }) {
			return true
		}
	}
	return false
}

// textChip covers the editor's runes [s, e) with a chip in col, its label
// tok in the chip's color on the line's baseline. A span that wraps stays
// plain text.
func (c *Composer) textChip(gtx layout.Context, th *theme.Theme, col color.NRGBA, s, e int, tok string) {
	regions := c.ed.Regions(s, e, nil)
	if len(regions) != 1 {
		return
	}
	bg, fg := th.Chip(col, th.Surface)
	rg := regions[0]
	lm := op.Record(gtx.Ops)
	ld := text(gtx, th, th.UIFont, th.Sp(theme.Large), fg, tok)
	label := lm.Stop()
	base := rg.Bounds.Max.Y - rg.Baseline
	ly := base - (ld.Size.Y - ld.Baseline)
	// why: 2 dp, not the image chip's 6: the label is the text itself, so wider padding would cover the spaces around it.
	padX, padY := gtx.Dp(theme.Space2XS), gtx.Dp(theme.Space2XS)
	box := image.Rect(rg.Bounds.Min.X-padX, ly-padY, rg.Bounds.Min.X+ld.Size.X+padX, ly+ld.Size.Y+padY)
	paint.FillShape(gtx.Ops, bg, clip.UniformRRect(box, gtx.Dp(5)).Op(gtx.Ops))
	o := op.Offset(image.Pt(rg.Bounds.Min.X, ly)).Push(gtx.Ops)
	label.Add(gtx.Ops)
	o.Pop()
}

// mentionAnchor notes where the @ sits in the card, from the editor laid
// out at off in it.
func (c *Composer) mentionAnchor(off image.Point) {
	if !c.mention.open() {
		return
	}
	rs := c.ed.Regions(c.mention.start, c.mention.start+1, nil)
	if len(rs) > 0 {
		c.mention.anchor = rs[0].Bounds.Add(off)
	}
}

// mentionPop is the popover of matches above the @, in the card's
// coordinates: a file or folder icon, the name, and its folder muted.
func (c *Composer) mentionPop(gtx layout.Context, th *theme.Theme) {
	m := &c.mention
	rec := op.Record(gtx.Ops)
	first := max(0, min(m.sel-mentionRows+1, len(m.rows)-mentionRows))
	if m.sel < first {
		first = m.sel
	}
	shown := m.rows[first:min(len(m.rows), first+mentionRows)]
	pw := min(gtx.Dp(420), c.cardSize.X)
	pg := gtx
	pg.Constraints = layout.Constraints{Min: image.Pt(pw, 0), Max: image.Pt(pw, gtx.Constraints.Max.Y)}
	body := op.Record(gtx.Ops)
	d := layout.UniformInset(theme.SpaceXS).Layout(pg, func(gtx layout.Context) layout.Dimensions {
		var kids []layout.FlexChild
		for i, p := range shown {
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return c.mentionRow(gtx, th, p, first+i == m.sel)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
	call := body.Stop()
	bounds := image.Rectangle{Min: image.Pt(0, -1<<20), Max: c.cardSize}
	at := kit.Place(m.anchor, d.Size, bounds, kit.AboveStart, gtx.Dp(theme.SpaceXS), 0)
	o := op.Offset(image.Pt(at.X, -d.Size.Y-gtx.Dp(theme.SpaceXS))).Push(gtx.Ops)
	t := m.t.Get(gtx)
	fade := paint.PushOpacity(gtx.Ops, t)
	sc := 0.97 + 0.03*t
	grow := op.Affine(f32.AffineId().Scale(f32.Pt(0, float32(d.Size.Y)), f32.Pt(sc, sc))).Push(gtx.Ops)
	rect := image.Rectangle{Max: d.Size}
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	cl := clip.UniformRRect(rect, gtx.Dp(theme.RadiusPopover)).Push(gtx.Ops)
	event.Op(gtx.Ops, &m.tag) // why: a press on the popover goes no further
	call.Add(gtx.Ops)
	cl.Pop()
	grow.Pop()
	fade.Pop()
	o.Pop()
	op.Defer(gtx.Ops, rec.Stop())
}

// mentionRow is one match: highlighted when picked by the keys, Hover
// under the pointer.
func (c *Composer) mentionRow(gtx layout.Context, th *theme.Theme, p string, on bool) layout.Dimensions {
	b := c.click("mention:" + p)
	return kit.Clickable(gtx, b, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp(p).Add(gtx.Ops)
		h, w := gtx.Dp(28), gtx.Constraints.Max.X
		rect := image.Rectangle{Max: image.Pt(w, h)}
		r := gtx.Dp(theme.RadiusControl)
		if gtx.Focused(b) {
			kit.FocusRing(gtx, th, rect, r)
		}
		fill := theme.Transparent
		switch {
		case b.Pressed():
			fill = th.Pressed
		case on:
			fill = th.SelectedBg
		case b.Hovered():
			fill = th.HoverElevated
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect, r).Op(gtx.Ops))
		icon, name := "file", path.Base(p)
		dir := path.Dir(strings.TrimSuffix(p, "/"))
		if strings.HasSuffix(p, "/") {
			icon, name = "folder", name+"/"
		}
		if dir == "." {
			dir = ""
		}
		g := gtx
		g.Constraints.Max.X = w - 2*gtx.Dp(theme.SpaceS)
		o := op.Offset(image.Pt(gtx.Dp(theme.SpaceS), 0)).Push(gtx.Ops)
		row(g, gtx.Dp(theme.SpaceS), h,
			func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, icon, gtx.Dp(14), th.Muted) },
			func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, th.UIFont, th.Sp(theme.Body), th.Fg, name)
			},
			func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Muted, dir)
			})
		o.Pop()
		defer clip.Rect(rect).Push(gtx.Ops).Pop()
		pointer.CursorPointer.Add(gtx.Ops)
		return layout.Dimensions{Size: rect.Max}
	})
}

// mentionClicks picks a row the pointer clicked.
func (c *Composer) mentionClicks(gtx layout.Context) {
	for _, p := range c.mention.rows {
		if c.click("mention:" + p).Clicked(gtx) {
			c.pickMention(gtx, p)
			return
		}
	}
}
