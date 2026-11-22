package panel

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/pointer"
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

// monoLine is a line of a mono box.
type monoLine struct {
	kind     byte // 0 plain; '+', '-', ' ' a diff's; '@' a hunk header; '$' a command; 'q' a quiet note
	old, new int  // line numbers, 0 for none
	text     string
	spans    []span // colored runs, from ANSI escapes
}

// monoBox is code, a diff or a command's output in mono on a panel: lines
// that do not wrap and scroll sideways, line numbers in a gutter, a
// diff's tints, the first or last lines with Show all, and Copy.
type monoBox struct {
	id         string
	lines      []monoLine
	nums       int  // gutters: 0, 1 for new numbers, 2 for old and new
	signs      bool // a column for a diff's + and -
	fixed      int  // lines always shown, a command's, before those head and tail cut
	head, tail int  // show only the first head or the last tail lines of the rest; 0 shows all
	title      string
	titleMono  bool
	copy       string // what Copy puts on the clipboard
	copyLabel  string // the header's Copy button; "" puts an icon on the box
}

// maxShown is the most lines Show all draws.
// shortcut: a box lays out every line it shows, as a list row does not
// cull inside itself; past this the rest is a Copy away. Cull to the
// viewport if outputs this long are common.
const maxShown = 5000

// toolState is the tool rows' own state in a Transcript.
type toolState struct {
	scrolls map[string]*gesture.Scroll
	offs    map[string]int // sideways scroll by box id
	shells  shells
	views   map[string]viewCache
	zoom    zoomed
	syn     map[string]synLines // code boxes' tokens by box id
}

// viewCache is a call's view, worked out once per update of its item.
type viewCache struct {
	updated string
	v       toolView
	out     string
}

func (d *cdraw) box(gtx layout.Context, b monoBox) layout.Dimensions {
	th, ts := d.th, &d.t.tool
	w := gtx.Constraints.Max.X
	all := d.t.open["all:"+b.id]
	b.fixed = min(b.fixed, len(b.lines))
	rest := b.lines[b.fixed:]
	cut := b.head > 0 && len(rest) > b.head || b.tail > 0 && len(rest) > b.tail
	switch {
	case !all && b.head > 0 && len(rest) > b.head:
		rest = rest[:b.head]
	case !all && b.tail > 0 && len(rest) > b.tail:
		rest = rest[len(rest)-b.tail:]
	case len(rest) > maxShown:
		rest = rest[:maxShown]
	}
	shown := append(b.lines[:b.fixed:b.fixed], rest...)

	// The text: one selectable string, a line per line.
	var sb, orig strings.Builder
	starts := make([]int, len(shown))
	lineRunes := make([]int, len(shown))
	runes, maxNo := 0, 0
	hasDecor := b.nums > 0 || b.signs
	for i, l := range shown {
		if i > 0 {
			sb.WriteByte('\n')
			orig.WriteByte('\n')
			runes++
		}
		starts[i] = runes
		orig.WriteString(l.text)
		t := mdview.Detab(l.text)
		sb.WriteString(t)
		rc := utf8.RuneCountInString(t)
		lineRunes[i] = rc
		runes += rc
		maxNo = max(maxNo, l.old, l.new)
		if l.kind == '+' || l.kind == '-' || l.kind == '@' {
			hasDecor = true
		}
	}
	st := d.mono()
	numW := 0
	if b.nums > 0 && maxNo > 0 {
		_, cd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.lbl(gtx, th.MonoFont, st.size, th.Muted, strings.Repeat("0", len(strconv.Itoa(maxNo))))
		})
		numW = cd.Size.X + gtx.Dp(14)
	}
	signW := 0
	if b.signs {
		signW = gtx.Dp(16)
	}
	textX := b.nums*numW + signW
	if textX == 0 {
		textX = gtx.Dp(theme.SpaceM)
	}
	padR := gtx.Dp(theme.SpaceM)
	hh := 0
	if b.title != "" {
		hh = gtx.Dp(rowH)
	} else if b.copy != "" {
		padR += gtx.Dp(hit)
	}
	sl := d.t.sel("box:" + b.id)
	d.copyAs(sl, tabbed(orig.String(), sb.String()))
	d.paintMono(sl, starts, lineRunes, shown)
	g := gtx
	g.Constraints = layout.Constraints{Max: image.Pt(1<<20, 1<<20)} // no wrapping
	call, td := measure(g, func(gtx layout.Context) layout.Dimensions { return d.selectable(gtx, sl, st, sb.String()) })
	py := gtx.Dp(6)
	top := hh + py
	h := top + td.Size.Y + py
	viewW := max(0, w-textX-padR)
	maxOff := max(0, td.Size.X-viewW)

	// Sideways scroll: a wheel's or touchpad's x, kept per box.
	if ts.scrolls == nil {
		ts.scrolls, ts.offs = map[string]*gesture.Scroll{}, map[string]int{}
	}
	sc := ts.scrolls[b.id]
	if sc == nil {
		sc = new(gesture.Scroll)
		ts.scrolls[b.id] = sc
	}
	off := min(ts.offs[b.id], maxOff)
	off += sc.Update(gtx.Metric, gtx.Source, gtx.Now, gesture.Horizontal, pointer.ScrollRange{Min: -off, Max: maxOff - off}, pointer.ScrollRange{})
	off = min(max(off, 0), maxOff)
	ts.offs[b.id] = off

	rad := gtx.Dp(theme.RadiusControl)
	rr := clip.UniformRRect(image.Rect(0, 0, w, h), rad)
	paint.FillShape(gtx.Ops, th.Surface, rr.Op(gtx.Ops))
	cl := rr.Push(gtx.Ops)
	if hh > 0 {
		paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Rect{Min: image.Pt(0, hh-1), Max: image.Pt(w, hh)}.Op())
	}

	// Tints and gutters behind each line, where the text put it.
	add, del, hunk := theme.Mix(th.Surface, th.Green, 0.10), theme.Mix(th.Surface, th.Red, 0.10), theme.Mix(th.Surface, th.Blue, 0.07)
	gutter := theme.Mix(th.Surface, th.Bg, 0.5)
	if numW > 0 {
		paint.FillShape(gtx.Ops, gutter, clip.Rect{Min: image.Pt(0, hh), Max: image.Pt(b.nums*numW, h)}.Op())
	}
	if hasDecor {
		var rg []widget.Region
		for i, l := range shown {
			rg = sl.Regions(starts[i], starts[i]+max(lineRunes[i], 1), rg[:0])
			if len(rg) == 0 {
				continue
			}
			r := rg[0]
			y0, y1 := top+r.Bounds.Min.Y, top+r.Bounds.Max.Y
			tint := color.NRGBA{}
			sign, scol := "", th.Muted
			switch l.kind {
			case '+':
				tint, sign, scol = add, "+", th.Readable(th.Green, add)
			case '-':
				tint, sign, scol = del, "−", th.Readable(th.Red, del)
			case '@':
				tint = hunk
			}
			if tint.A > 0 {
				paint.FillShape(gtx.Ops, tint, clip.Rect{Min: image.Pt(0, y0), Max: image.Pt(w, y1)}.Op())
			}
			num := func(x, n int) {
				if n == 0 {
					return
				}
				d.onBaseAt(gtx, th.MonoFont, st, th.TextQuiet, strconv.Itoa(n), x+numW-gtx.Dp(8), y1-r.Baseline, true)
			}
			if numW > 0 {
				if b.nums == 2 {
					num(0, l.old)
					num(numW, l.new)
				} else {
					num(0, l.new)
				}
			}
			if sign != "" {
				d.onBaseAt(gtx, th.MonoFont, st, scol, sign, b.nums*numW+gtx.Dp(4), y1-r.Baseline, false)
			}
		}
	}

	// The text, scrolled sideways inside its column, shaped once with sl.Colors/Styles.
	tc := clip.Rect{Min: image.Pt(textX, hh), Max: image.Pt(w-padR+gtx.Dp(theme.SpaceS), h)}.Push(gtx.Ops)
	o := op.Offset(image.Pt(textX-off, 0)).Push(gtx.Ops)
	at(gtx, image.Pt(0, top), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return td })
	o.Pop()
	tc.Pop()

	// The sideways scroll takes the wheel over the whole box; a thin bar
	// shows where it is.
	area := clip.Rect{Min: image.Pt(0, hh), Max: image.Pt(w, h)}.Push(gtx.Ops)
	sc.Add(gtx.Ops)
	area.Pop()
	if maxOff > 0 && viewW > 0 {
		tw := max(gtx.Dp(24), viewW*viewW/td.Size.X)
		x := textX + off*(viewW-tw)/maxOff
		bar := image.Rect(x, h-gtx.Dp(4), x+tw, h-gtx.Dp(1))
		paint.FillShape(gtx.Ops, th.BorderStrong, clip.UniformRRect(bar, bar.Dy()/2).Op(gtx.Ops))
	}

	// Copy: in the header bar, else at the box's top right.
	if b.copy != "" {
		ck := d.t.click("copy:box:" + b.id)
		if ck.Clicked(gtx) {
			d.t.copy(gtx, "box:"+b.id, b.copy)
		}
		bc, bd := measure(gtx, func(gtx layout.Context) layout.Dimensions {
			return d.ghost(gtx, ck, "clipboard", b.copyLabel, d.t.isCopied("box:"+b.id))
		})
		y := gtx.Dp(2)
		if hh > 0 {
			y = (hh - bd.Size.Y) / 2
		}
		at(gtx, image.Pt(w-gtx.Dp(2)-bd.Size.X, y), func(gtx layout.Context) layout.Dimensions { bc.Add(gtx.Ops); return bd })
		if hh > 0 && b.title != "" {
			f := th.UIFont
			if b.titleMono {
				f = th.MonoFont
			}
			g := gtx
			g.Constraints.Max.X = max(0, w-bd.Size.X-gtx.Dp(24))
			hc, hd := measure(g, func(gtx layout.Context) layout.Dimensions {
				return d.lbl(gtx, f, th.Sp(theme.Small), th.Muted, b.title)
			})
			at(gtx, image.Pt(gtx.Dp(theme.SpaceM), (hh-hd.Size.Y)/2), func(gtx layout.Context) layout.Dimensions { hc.Add(gtx.Ops); return hd })
		}
	}
	cl.Pop()
	if !cut {
		return layout.Dimensions{Size: image.Pt(w, h)}
	}
	label := "Show all " + strconv.Itoa(len(b.lines)-b.fixed) + " lines"
	switch {
	case all && b.head > 0:
		label = "Show the first " + strconv.Itoa(b.head) + " lines"
	case all:
		label = "Show the last " + strconv.Itoa(b.tail) + " lines"
	}
	sk := d.t.click("all:" + b.id)
	for sk.Clicked(gtx) {
		d.t.open["all:"+b.id] = !all
	}
	ic := "chevron-down"
	if all {
		ic = "chevron-right"
	}
	md := at(gtx, image.Pt(0, h+gtx.Dp(gapShowAll)), func(gtx layout.Context) layout.Dimensions {
		return d.ghost(gtx, sk, ic, label, false)
	})
	return layout.Dimensions{Size: image.Pt(w, h+gtx.Dp(gapShowAll)+md.Size.Y)}
}

// paintMono sets sl's Colors and Styles from shown's line kinds and spans
// in one pass, reusing sl's slices so a frame allocates nothing new.
func (d *cdraw) paintMono(sl *widget.Selectable, starts, lineRunes []int, shown []monoLine) {
	th := d.th
	boldFont := semibold(th.MonoFont)
	italFont := th.MonoFont
	italFont.Style = font.Italic
	boldItal := boldFont
	boldItal.Style = font.Italic
	sl.Colors, sl.Styles = sl.Colors[:0], sl.Styles[:0]
	for i, l := range shown {
		n := lineRunes[i]
		if n == 0 {
			continue
		}
		start := starts[i]
		switch {
		case l.kind == '$':
			p := min(2, n)
			sl.Colors = append(sl.Colors, widget.TextColor{Start: start, End: start + p, Color: th.TextQuiet})
			if p < n {
				sl.Colors = append(sl.Colors, widget.TextColor{Start: start + p, End: start + n, Color: th.Fg})
			}
		case l.kind == '@':
			sl.Colors = append(sl.Colors, widget.TextColor{Start: start, End: start + n, Color: th.Muted})
		case l.kind == 'q':
			sl.Colors = append(sl.Colors, widget.TextColor{Start: start, End: start + n, Color: th.TextQuiet})
		case len(l.spans) > 0:
			for _, s := range l.spans {
				a, b := min(s.a, n), min(s.b, n)
				if b <= a {
					continue
				}
				sl.Colors = append(sl.Colors, widget.TextColor{Start: start + a, End: start + b, Color: d.ansiColor(s)})
				it := s.syn == syntax.Comment
				if s.bold || it {
					f := boldFont
					if s.bold && it {
						f = boldItal
					} else if it {
						f = italFont
					}
					sl.Styles = append(sl.Styles, text.Style{Start: start + a, End: start + b, Font: f, Scale: 1})
				}
			}
		}
	}
}

// onBaseAt draws one line of txt with its baseline at y, from x, or
// ending at x when right.
func (d *cdraw) onBaseAt(gtx layout.Context, f font.Font, st tstyle, c color.NRGBA, txt string, x, y int, right bool) {
	call, dims := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints = layout.Constraints{Max: image.Pt(1<<20, 1<<20)}
		return widget.Label{MaxLines: 1}.Layout(gtx, d.th.Shaper, f, st.size, txt, material(gtx, c))
	})
	if right {
		x -= dims.Size.X
	}
	at(gtx, image.Pt(x, y-(dims.Size.Y-dims.Baseline)), func(gtx layout.Context) layout.Dimensions { call.Add(gtx.Ops); return dims })
}

// ansiColor is a run's color on the box: the theme's terminal palette
// for the first 16, made readable on the surface.
func (d *cdraw) ansiColor(s span) color.NRGBA {
	th := d.th
	var c color.NRGBA
	switch {
	case s.syn != syntax.Plain:
		return d.synColors()[s.syn]
	case s.fg == -2:
		c = s.rgb
	case s.fg >= 0 && s.fg < 16:
		c = th.ANSI[s.fg]
	case s.fg >= 16:
		c = xterm256(int(s.fg))
	default:
		return d.mono().c
	}
	return th.Readable(c, th.Surface)
}

// diffMono is an edit's lines as a box's lines.
func diffMono(lines []diffLine) []monoLine {
	out := make([]monoLine, len(lines))
	for i, l := range lines {
		out[i] = monoLine{kind: l.kind, old: l.old, new: l.new, text: l.text}
	}
	return out
}

// textMono is plain text as a box's lines.
func textMono(s string) []monoLine {
	var out []monoLine
	for l := range strings.SplitSeq(s, "\n") {
		out = append(out, monoLine{text: l})
	}
	return out
}

// commandMono is a command as a box's first lines, after a quiet "$ ".
func commandMono(cmd string) []monoLine {
	var out []monoLine
	for i, l := range strings.Split(strings.TrimRight(cmd, "\n"), "\n") {
		p := "  "
		if i == 0 {
			p = "$ "
		}
		out = append(out, monoLine{kind: '$', text: p + l})
	}
	return out
}
