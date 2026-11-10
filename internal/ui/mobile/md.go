package mobile

import (
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

// monoScale is the size of inline code over its text's size that gives the
// mono font the UI font's ascent. x/styledtext tops every span of a line
// at the same y and ignores their baselines, so a span with a smaller
// ascent rides high; with equal ascents the baselines meet. For Geist and
// JetBrains Mono it is 1005/1020, which also brings the x-heights within
// 2% of each other.
//
// ponytail: a workaround for styledtext.Layout dropping lineAscent; patch
// x if replies ever mix more than two fonts.
func monoScale(th *theme.Theme) float32 {
	asc := func(f font.Font) float32 {
		th.Shaper.LayoutString(text.Parameters{Font: f, PxPerEm: fixed.I(1000), MaxWidth: 1 << 20}, "x")
		var a fixed.Int26_6
		for {
			g, ok := th.Shaper.NextGlyph()
			if !ok {
				break
			}
			a = g.Ascent
		}
		return float32(a.Round())
	}
	ui, mono := asc(th.UIFont), asc(th.MonoFont)
	if ui == 0 || mono == 0 {
		return 1
	}
	return ui / mono
}
