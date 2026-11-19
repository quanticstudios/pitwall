package mdview

import (
	"slices"
	"strings"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/text"
	"gioui.org/x/richtext"
	"golang.org/x/image/math/fixed"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Detab turns tabs into four spaces: the mono font has no tab glyph, and
// Go code from a transcript indents with tabs.
func Detab(s string) string { return strings.ReplaceAll(s, "\t", "    ") }

// MonoScale is the size of inline code over its text's size that gives
// the mono font the UI font's x-height, so code reads the same size as the
// words around it whichever mono font the platform has: JetBrains Mono on
// a desktop, Droid Sans Mono on Android. The patched styledtext sets both
// on one baseline.
func MonoScale(th *theme.Theme) float32 {
	ui, mono := XHeight(th, th.UIFont), XHeight(th, th.MonoFont)
	if ui == 0 || mono == 0 {
		return 1
	}
	return float32(ui) / float32(mono)
}

// XHeight is the height of f's "x" at 1000 px per em.
func XHeight(th *theme.Theme, f font.Font) int {
	th.Shaper.LayoutString(text.Parameters{Font: f, PxPerEm: fixed.I(1000), MaxWidth: 1 << 20}, "x")
	h := 0
	for {
		g, ok := th.Shaper.NextGlyph()
		if !ok {
			return h
		}
		h = -g.Bounds.Min.Y.Round()
	}
}

// SpanFont is the font span s draws in, in text set in base, and its size
// over the text's: semibold for bold (the type spec has no 700), slanted
// for italic, the mono font at mono for code. ok is false for a span in
// base as it is.
func SpanFont(th *theme.Theme, base font.Font, s Span, mono float32) (f font.Font, scale float32, ok bool) {
	f, scale = base, 1
	if s.Mono {
		f, scale = th.MonoFont, mono
	}
	if s.Bold && !s.Mono {
		f.Weight = max(f.Weight, font.SemiBold)
	}
	if s.Italic {
		f.Style = font.Italic
	}
	return f, scale, f != base || scale != 1
}

// Inline is s's first block as one line of text, with the styles that
// set its bold, italic and code: a notice's body.
func Inline(th *theme.Theme, base font.Font, s string) (string, []text.Style) {
	blocks := Parse([]byte(Heal(s)))
	if len(blocks) == 0 {
		return s, nil
	}
	var b strings.Builder
	var styles []text.Style
	n := 0
	mono := MonoScale(th)
	for _, sp := range blocks[0].Spans {
		t := strings.ReplaceAll(Detab(sp.Text), "\n", " ")
		k := utf8.RuneCountInString(t)
		if f, scale, ok := SpanFont(th, base, sp, mono); ok {
			styles = append(styles, text.Style{Start: n, End: n + k, Font: f, Scale: scale})
		}
		b.WriteString(t)
		n += k
	}
	return b.String(), styles
}

// GluePunct moves the punctuation that starts a span onto the end of the
// span before it, when nothing separates them. styledtext wraps span by
// span, so "**Build from source**." could put the period on a line of its
// own; the period takes the span's style, which at that size reads the
// same.
func GluePunct(spans []richtext.SpanStyle) []richtext.SpanStyle {
	for i := 1; i < len(spans); i++ {
		prev, cur := &spans[i-1], &spans[i]
		n := len(cur.Content) - len(strings.TrimLeft(cur.Content, ".,;:!?)]'\"…"))
		if n == 0 || prev.Content == "" || strings.ContainsAny(prev.Content[len(prev.Content)-1:], " \n\t") {
			continue
		}
		prev.Content += cur.Content[:n]
		cur.Content = cur.Content[n:]
	}
	return slices.DeleteFunc(spans, func(s richtext.SpanStyle) bool { return s.Content == "" })
}
