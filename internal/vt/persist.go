package vt

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
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

var historySGR = [...]struct {
	attr Attr
	seq  string
}{
	{Bold, "\x1b[1m"}, {Faint, "\x1b[2m"}, {Italic, "\x1b[3m"}, {Underline, "\x1b[4m"},
	{Blink, "\x1b[5m"}, {Reverse, "\x1b[7m"}, {Invisible, "\x1b[8m"}, {Strike, "\x1b[9m"},
}

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
	count := min(total, limit)
	raw := make([]byte, 0, min(count*64, maxHistoryText))
	offsets := make([]int, 1, count+1)
	// Read newest first so the byte bound drops the oldest rows too.
	for i := total - 1; i >= max(0, total-limit); i-- {
		var l line
		if i < h.len() {
			l = *h.at(i)
		} else {
			y := i - h.len()
			l = h.line(rows[y], flags[y], 0)
		}
		prev := len(raw)
		raw = appendHistoryANSI(raw, l)
		if len(raw) > maxHistoryText {
			raw = raw[:prev]
			break
		}
		offsets = append(offsets, len(raw))
	}
	var out strings.Builder
	out.Grow(len(raw))
	for i := len(offsets) - 1; i > 0; i-- {
		out.Write(raw[offsets[i-1]:offsets[i]])
	}
	return len(rows[0]), out.String()
}

func appendHistoryANSI(out []byte, l line) []byte {
	col, off := 0, 0
	for i, r := range l.runs {
		end := l.width()
		if i+1 < len(l.runs) {
			end = int(l.runs[i+1].col)
		}
		start := off
		if l.cells == nil {
			off += end - col
			col = end
		} else {
			for col < end {
				off += int(l.cells[col] >> 2)
				col++
			}
		}
		if start == off {
			continue
		}
		if r.attrs != 0 || r.fg != 0 || r.bg != 0 || i > 0 {
			out = append(out, "\x1b[0m"...)
			if r.attrs != 0 {
				for _, a := range historySGR {
					if r.attrs&a.attr != 0 {
						out = append(out, a.seq...)
					}
				}
			}
			if r.fg != 0 {
				out = appendHistoryColor(out, 38, r.fg)
			}
			if r.bg != 0 {
				out = appendHistoryColor(out, 48, r.bg)
			}
		}
		out = appendSanitizedText(out, l.text[start:off])
	}
	return append(out, "\x1b[0m\r\n"...)
}

func appendHistoryColor(out []byte, base byte, color Color) []byte {
	switch color &^ 0xffffff {
	case PaletteFlag:
		out = append(out, '\x1b', '[')
		out = strconv.AppendUint(out, uint64(base), 10)
		out = append(out, ';', '5', ';')
		out = strconv.AppendUint(out, uint64(color&255), 10)
		out = append(out, 'm')
	case RGBFlag:
		out = append(out, '\x1b', '[')
		out = strconv.AppendUint(out, uint64(base), 10)
		out = append(out, ';', '2', ';')
		out = strconv.AppendUint(out, uint64(color>>16&255), 10)
		out = append(out, ';')
		out = strconv.AppendUint(out, uint64(color>>8&255), 10)
		out = append(out, ';')
		out = strconv.AppendUint(out, uint64(color&255), 10)
		out = append(out, 'm')
	}
	return out
}

func appendSanitizedText(out []byte, s string) []byte {
	// Fast path: printable ASCII has no control runes and needs no decoding.
	clean := true
	for i := range len(s) {
		b := s[i]
		if b < 0x20 || b >= 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return append(out, s...)
	}
	for i := 0; i < len(s); {
		if b := s[i]; b < utf8.RuneSelf {
			if b >= 0x20 && b != 0x7f {
				out = append(out, b)
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsControl(r) {
			out = append(out, s[i:i+size]...)
		}
		i += size
	}
	return out
}
