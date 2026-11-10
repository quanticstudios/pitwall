package mobile

import (
	"slices"
	"strings"

	"gioui.org/font"
	"gioui.org/text"
	"gioui.org/x/richtext"
	"golang.org/x/image/math/fixed"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// mdPart is a run of a reply: markdown, or a fenced code block, which the
// chat draws in its own box since richtext cannot fill behind a span.
type mdPart struct {
	code  bool
	text  string
	spans []richtext.SpanStyle // the markdown's, once rendered
}

// splitFences cuts src at ``` fences into markdown and code parts, empty
// markdown left out. An unclosed fence runs to the end, as a reply still
// streaming has it.
func splitFences(src string) []mdPart {
	var parts []mdPart
	var cur []string
	code := false
	flush := func() {
		s := strings.Join(cur, "\n")
		if code || strings.TrimSpace(s) != "" {
			if !code {
				s = strings.Trim(s, "\n")
			}
			parts = append(parts, mdPart{code: code, text: s})
		}
		cur = nil
	}
	for line := range strings.SplitSeq(src, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			flush()
			code = !code
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		flush()
	}
	return parts
}

// detab turns tabs into four spaces: the mono font has no tab glyph, and
// Go code from a transcript indents with tabs.
func detab(s string) string { return strings.ReplaceAll(s, "\t", "    ") }

// monoScale is the size of inline code over its text's size that gives
// the mono font the UI font's x-height, so code reads the same size as the
// words around it whichever mono font the platform has: JetBrains Mono on
// a desktop, Droid Sans Mono on Android. The patched styledtext sets both
// on one baseline.
func monoScale(th *theme.Theme) float32 {
	ui, mono := xHeight(th, th.UIFont), xHeight(th, th.MonoFont)
	if ui == 0 || mono == 0 {
		return 1
	}
	return float32(ui) / float32(mono)
}

// xHeight is the height of f's "x" at 1000 px per em.
func xHeight(th *theme.Theme, f font.Font) int {
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

// gluePunct moves the punctuation that starts a span onto the end of the
// span before it, when nothing separates them. styledtext wraps span by
// span, so "**Build from source**." could put the period on a line of its
// own; the period takes the span's style, which at that size reads the
// same.
func gluePunct(spans []richtext.SpanStyle) []richtext.SpanStyle {
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
