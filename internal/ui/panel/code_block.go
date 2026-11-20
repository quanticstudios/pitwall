package panel

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/syntax"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// foldAt is how many lines a code block shows before it folds behind
// "Show all N lines".
const foldAt = 30

// hlBlock is a fenced block made ready once per text: the code as drawn,
// its tokens, where it folds and the command Run types. Drawing it maps
// the tokens to colors and never lexes again.
type hlBlock struct {
	src, lang string
	code      string
	toks      []syntax.Token
	lines     int
	cut       int // bytes of code in its first foldAt lines, all of it when it does not fold
	run       string
}

func newHL(lang, src string) *hlBlock {
	code := mdview.Detab(strings.TrimRight(src, "\n"))
	h := &hlBlock{src: src, lang: lang, code: code, toks: syntax.Lex(lang, code), lines: strings.Count(code, "\n") + 1, cut: len(code)}
	if h.lines > foldAt {
		h.cut = nthLine(code, foldAt)
	}
	h.run = runnable(lang, code)
	return h
}

// nthLine is the byte index of the newline that ends s's n-th line.
func nthLine(s string, n int) int {
	at := 0
	for range n {
		i := strings.IndexByte(s[at:], '\n')
		if i < 0 {
			return len(s)
		}
		at += i + 1
	}
	return at - 1
}

// hl is block key's hlBlock for src in lang, made when the text changed.
func (r *reply) hl(key, lang, src string) *hlBlock {
	h := r.code[key]
	if h == nil || h.src != src || h.lang != lang {
		h = newHL(lang, src)
		r.code[key] = h
	}
	return h
}

// synColors is each token kind's color on a code card, the theme's
// terminal palette as herdr-chat maps it: keywords magenta, strings
// green, numbers yellow, functions blue, types cyan, tags red, comments
// and punctuation muted.
func (d *cdraw) synColors() *[syntax.Header + 1]color.NRGBA {
	th := d.th
	if d.syn != nil && d.synFor == th {
		return d.syn
	}
	on := func(c color.NRGBA) color.NRGBA { return th.Readable(c, th.Surface) }
	fg := d.mono().c
	d.syn, d.synFor = &[syntax.Header + 1]color.NRGBA{
		syntax.Plain:   fg,
		syntax.Keyword: on(th.ANSI[5]),
		syntax.String:  on(th.ANSI[2]),
		syntax.Number:  on(th.ANSI[3]),
		syntax.Func:    on(th.ANSI[4]),
		syntax.Type:    on(th.ANSI[6]),
		syntax.Comment: th.Muted,
		syntax.Tag:     on(th.ANSI[1]),
		syntax.Punct:   theme.Mix(fg, th.Muted, 0.6),
		syntax.Added:   on(th.Green),
		syntax.Removed: on(th.Red),
		syntax.Hunk:    on(th.Blue),
		syntax.Header:  th.Muted,
	}, th
	return d.syn
}

// paint sets sl's colors and italics from toks, the runes before end of
// them, reusing sl's slices so a frame allocates nothing new.
func (d *cdraw) paintTokens(sl *widget.Selectable, toks []syntax.Token, end int) {
	pal := d.synColors()
	it := d.mono().f
	it.Style = font.Italic
	sl.Colors, sl.Styles = sl.Colors[:0], sl.Styles[:0]
	for _, t := range toks {
		if t.Start >= end {
			break
		}
		e := min(t.End, end)
		sl.Colors = append(sl.Colors, widget.TextColor{Start: t.Start, End: e, Color: pal[t.Kind]})
		if t.Kind == syntax.Comment {
			sl.Styles = append(sl.Styles, text.Style{Start: t.Start, End: e, Font: it, Scale: 1})
		}
	}
}

// codeBlock is a fenced block as a card: a 30 dp strip with its language,
// Run for a shell block and Copy, then the code in mono, highlighted,
// selectable and never wrapped; a line wider than the card scrolls it
// sideways. Over foldAt lines it shows the first ones and Show all.
func (d *cdraw) codeBlock(gtx layout.Context, r *reply, key, lang, src string) layout.Dimensions {
	th := d.th
	h := r.hl(key, lang, src)
	w := gtx.Constraints.Max.X
	hh := gtx.Dp(rowH)
	px, py := gtx.Dp(theme.SpaceM+theme.Space2XS), gtx.Dp(theme.SpaceM)
	rad := gtx.Dp(theme.RadiusPopover)
	folds := h.lines > foldAt
	open := folds && d.t.toggled(gtx, "fold:"+key, false)
	shown := h.code
	if folds && !open {
		shown = h.code[:h.cut]
	}
	sl := d.t.sel(key)
	d.paintTokens(sl, h.toks, utf8.RuneCountInString(shown))
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(1<<20, gtx.Constraints.Max.Y)}
	call, td := measure(g, func(gtx layout.Context) layout.Dimensions { return d.selectable(gtx, sl, d.mono(), shown) })
	sw := r.wide[key]
	if sw == nil {
		sw = new(mdview.Sideways)
		r.wide[key] = sw
	}
	body, bar := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		inner := op.Record(gtx.Ops)
		at(gtx, image.Pt(px, py), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
		content := inner.Stop()
		bh := td.Size.Y + 2*py
		extra := sw.Layout(gtx, th, w-2, bh, td.Size.X+2*px, rad-1, content)
		return layout.Dimensions{Size: image.Pt(w, bh+extra)}
	})
	ht := hh + bar.Size.Y
	if folds {
		ht += hh
	}
	rrect(gtx, image.Rect(0, 0, w, ht), rad, th.Surface, th.BorderSubtle)
	line := func(y int) {
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(1, y-1), Max: image.Pt(w-1, y)}.Op())
	}
	line(hh)
	if lang != "" {
		lc, ld := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Muted, lang)
		})
		at(gtx, image.Pt(px, (hh-ld.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { lc.Add(gtx.Ops); return ld })
	}
	ck := d.t.click("copy:" + key)
	if ck.Clicked(gtx) {
		d.t.copy(gtx, key, h.code)
	}
	btns := []layout.Widget{func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, ck, "clipboard", "Copy", d.t.isCopied(key))
	}}
	if h.run != "" && d.t.Run != nil {
		rk := d.t.click("run:" + key)
		if rk.Clicked(gtx) {
			d.t.Run(h.run)
		}
		btns = append([]layout.Widget{func(gtx layout.Context) layout.Dimensions {
			return d.ghost(gtx, rk, "terminal", "Run", false)
		}}, btns...)
	}
	bc, bd := measure(gtx, func(gtx layout.Context) layout.Dimensions { return hrow(gtx, 0, btns...) })
	at(gtx, image.Pt(w-gtx.Dp(theme.Space2XS)-bd.Size.X, (hh-bd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { bc.Add(gtx.Ops); return bd })
	at(gtx, image.Pt(1, hh), func(gtx layout.Context) layout.Dimensions { body.Add(gtx.Ops); return bar })
	if folds {
		y := hh + bar.Size.Y
		line(y + 1)
		fk := d.t.click("fold:" + key)
		ic, txt := "chevron-down", "Show all "+strconv.Itoa(h.lines)+" lines"
		if open {
			ic, txt = "chevron-up", "Show less"
		}
		fc, fd := measure(gtx, func(gtx layout.Context) layout.Dimensions { return d.ghost(gtx, fk, ic, txt, false) })
		at(gtx, image.Pt(px-gtx.Dp(theme.SpaceS), y+(hh-fd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { fc.Add(gtx.Ops); return fd })
	}
	return layout.Dimensions{Size: image.Pt(w, ht)}
}
