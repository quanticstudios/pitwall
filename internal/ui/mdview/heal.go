package mdview

import (
	"slices"
	"strings"
)

// Heal closes the inline code, bold, italic and strikethrough a reply
// still streaming has opened in its last paragraph, so they draw styled
// as they grow instead of as raw markers that vanish when the closer
// arrives. A marker the text ends on is dropped until its text comes. An
// open code fence is left alone: the parser already draws it as code to
// the end.
func Heal(src string) string {
	start := 0
	fence := ""
	for off := 0; off < len(src); {
		end := strings.IndexByte(src[off:], '\n')
		if end < 0 {
			end = len(src)
		} else {
			end += off
		}
		line := strings.TrimLeft(src[off:end], " ")
		switch {
		case fence == "" && (strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~")):
			fence = line[:3]
		case fence != "" && strings.HasPrefix(line, fence):
			fence = ""
			start = end
		case fence == "" && strings.TrimSpace(line) == "":
			start = end
		}
		off = end + 1
	}
	if fence != "" {
		return src
	}
	body := strings.TrimRight(src, " \t")
	tail := body[min(start, len(body)):]
	// A marker run at the very end, after a space or a line start, opens
	// nothing yet.
	trimmed := strings.TrimRight(tail, "*~`")
	if trimmed != tail && (trimmed == "" || strings.ContainsAny(trimmed[len(trimmed)-1:], " \n")) {
		body = strings.TrimRight(body[:len(body)-(len(tail)-len(trimmed))], " \t")
		tail = trimmed
	}
	var open []string
	toggle := func(tok string) {
		if i := slices.Index(open, tok); i >= 0 {
			open = slices.Delete(open, i, i+1)
			return
		}
		open = append(open, tok)
	}
	lineStart := true
	for i := 0; i < len(tail); {
		c := tail[i]
		switch {
		case c == '`':
			j := strings.IndexByte(tail[i+1:], '`')
			if j < 0 {
				open = append(open, "`")
				i = len(tail)
				continue
			}
			i += j + 2
		case c == '~' && strings.HasPrefix(tail[i:], "~~"):
			toggle("~~")
			i += 2
		case c == '*':
			n := len(tail[i:]) - len(strings.TrimLeft(tail[i:], "*"))
			if lineStart && n == 1 && i+1 < len(tail) && tail[i+1] == ' ' {
				i += 2 // a bullet
				break
			}
			if n >= 2 {
				toggle("**")
			}
			if n%2 == 1 {
				toggle("*")
			}
			i += n
		case c == '\\':
			i += 2
		default:
			i++
		}
		lineStart = c == '\n' || lineStart && c == ' '
	}
	if len(open) == 0 && body == strings.TrimRight(src, " \t") {
		return src
	}
	var b strings.Builder
	b.WriteString(body)
	for i := len(open) - 1; i >= 0; i-- {
		b.WriteString(open[i])
	}
	return b.String()
}
