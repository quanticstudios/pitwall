package mobile

import (
	"strings"

	"gioui.org/x/richtext"
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
