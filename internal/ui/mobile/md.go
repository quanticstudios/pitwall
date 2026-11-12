package mobile

import (
	"regexp"
	"strings"

	"github.com/quanticstudios/pitwall/internal/ui/mdview"
)

// mdPath is a markdown file's path as a reply writes it in its text:
// relative, absolute or under ~/, with a :line suffix or not.
var mdPath = regexp.MustCompile(`(?i)(?:~/|\.{1,2}/|/)?(?:[\w.@+-]+/)*[\w@+-][\w.@+-]*\.(?:md|markdown)(?::\d+)*\b`)

// linkPaths makes the markdown file paths in blocks' text links, so a tap
// opens them: in paragraphs, headings and table cells, inline code too,
// but not in a link already, a URL or a code block.
func linkPaths(blocks []mdview.Block) {
	for i := range blocks {
		b := &blocks[i]
		b.Spans = linkSpans(b.Spans)
		for _, r := range b.Rows {
			for c := range r {
				r[c] = linkSpans(r[c])
			}
		}
	}
}

func linkSpans(spans []mdview.Span) []mdview.Span {
	var out []mdview.Span
	for k, s := range spans {
		ms := pathsIn(s.Text)
		if s.Link != "" || len(ms) == 0 {
			if out != nil {
				out = append(out, s)
			}
			continue
		}
		if out == nil {
			out = append(make([]mdview.Span, 0, len(spans)+2*len(ms)), spans[:k]...)
		}
		at := 0
		for _, m := range ms {
			if m[0] > at {
				t := s
				t.Text = s.Text[at:m[0]]
				out = append(out, t)
			}
			l := s
			l.Text, l.Link = s.Text[m[0]:m[1]], s.Text[m[0]:m[1]]
			out = append(out, l)
			at = m[1]
		}
		if at < len(s.Text) {
			t := s
			t.Text = s.Text[at:]
			out = append(out, t)
		}
	}
	if out == nil {
		return spans
	}
	return out
}

// pathsIn is where markdown paths stand in s on their own: not inside a
// longer word, a URL or a path that goes on.
func pathsIn(s string) [][]int {
	var out [][]int
	for _, m := range mdPath.FindAllStringIndex(s, -1) {
		if m[0] > 0 && strings.ContainsRune("/.:~-@+", rune(s[m[0]-1])) || isWord(s, m[0]-1) {
			continue
		}
		if m[1] < len(s) && (s[m[1]] == '/' || isWord(s, m[1])) {
			continue
		}
		out = append(out, m)
	}
	return out
}

func isWord(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
