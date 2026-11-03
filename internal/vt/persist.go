package vt

import (
	"fmt"
	"strings"
	"unicode"
)

// HistoryText captures at most limit main-screen rows, including scrollback.
// Alternate-screen contents, links and terminal modes are left out.
func HistoryText(e Emulator, limit int) (int, string) {
	if h, ok := e.(interface{ HistoryText(int) (int, string) }); ok {
		return h.HistoryText(limit)
	}
	return 0, ""
}

// maxHistoryText also bounds panes full of long graphemes or style changes.
const maxHistoryText = 4 << 20

func (t *emulator) HistoryText(limit int) (int, string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if limit <= 0 {
		return 0, ""
	}
	rows, flags, cur, _ := t.e.MainLines()
	last := min(cur.Y, len(rows)-1)
	for y := len(rows) - 1; y > last; y-- {
		if !blank(rows[y]) {
			last = y
			break
		}
	}
	h := &t.st.hist
	total := h.len() + last + 1
	var lines []string
	size := 0
	// Read newest first so the byte bound drops the oldest rows too.
	for i := total - 1; i >= max(0, total-limit); i-- {
		var l line
		if i < h.len() {
			l = *h.at(i)
		} else {
			y := i - h.len()
			l = h.line(rows[y], flags[y], 0)
		}
		s := historyANSI(l)
		if size+len(s) > maxHistoryText {
			break
		}
		lines = append(lines, s)
		size += len(s)
	}
	var out strings.Builder
	out.Grow(size)
	for i := len(lines) - 1; i >= 0; i-- {
		out.WriteString(lines[i])
	}
	return len(rows[0]), out.String()
}

func historyANSI(l line) string {
	var out strings.Builder
	col, off := 0, 0
	for i, r := range l.runs {
		end := l.width()
		if i+1 < len(l.runs) {
			end = int(l.runs[i+1].col)
		}
		start := off
		for col < end {
			if l.cells == nil {
				off++
			} else {
				off += int(l.cells[col] >> 2)
			}
			col++
		}
		if start == off {
			continue
		}
		if r.attrs != 0 || r.fg != 0 || r.bg != 0 || i > 0 {
			out.WriteString("\x1b[0m")
			for _, a := range []struct {
				attr Attr
				sgr  int
			}{
				{Bold, 1}, {Faint, 2}, {Italic, 3}, {Underline, 4},
				{Blink, 5}, {Reverse, 7}, {Invisible, 8}, {Strike, 9},
			} {
				if r.attrs&a.attr != 0 {
					fmt.Fprintf(&out, "\x1b[%dm", a.sgr)
				}
			}
			for j, color := range []Color{r.fg, r.bg} {
				switch color &^ 0xffffff {
				case PaletteFlag:
					fmt.Fprintf(&out, "\x1b[%d;5;%dm", 38+10*j, color&255)
				case RGBFlag:
					fmt.Fprintf(&out, "\x1b[%d;2;%d;%d;%dm", 38+10*j, color>>16&255, color>>8&255, color&255)
				}
			}
		}
		out.WriteString(strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, l.text[start:off]))
	}
	out.WriteString("\x1b[0m\r\n")
	return out.String()
}
