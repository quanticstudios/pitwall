package panel

import (
	"image/color"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/ui/syntax"
)

// span is a styled run of a line, runes [a, b).
type span struct {
	a, b int
	fg   int16       // a palette index 0-255, -1 for the default, -2 for rgb
	rgb  color.NRGBA // fg -2's color
	bold bool
	syn  syntax.Kind // a code token's kind, colored by synColors; Plain for an escape's run
}

// sgr is the style the escapes so far set.
type sgr struct {
	fg   int16
	rgb  color.NRGBA
	bold bool
}

func (s sgr) plain() bool { return s.fg == -1 && !s.bold }

// ansiLines splits a command's output into lines without escapes, and the
// colored runs of each. A carriage return keeps what was written after
// it, as a terminal shows a progress bar's last state.
func ansiLines(s string) []monoLine {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	st := sgr{fg: -1}
	var out []monoLine
	for l := range strings.SplitSeq(s, "\n") {
		if i := strings.LastIndexByte(strings.TrimRight(l, "\r"), '\r'); i >= 0 {
			l = l[i+1:]
		}
		var b strings.Builder
		var spans []span
		n := 0 // runes written
		start, cur := 0, st
		flush := func() {
			if n > start && !cur.plain() {
				spans = append(spans, span{a: start, b: n, fg: cur.fg, rgb: cur.rgb, bold: cur.bold})
			}
			start = n
		}
		for i := 0; i < len(l); {
			if l[i] != 0x1b {
				r, size := utf8.DecodeRuneInString(l[i:])
				i += size
				switch {
				case r == '\t': // as mdview.Detab, so the runs' offsets hold
					b.WriteString("    ")
					n += 4
				case r >= 0x20:
					b.WriteRune(r)
					n++
				}
				continue
			}
			end, params, final := escape(l, i)
			i = end
			if final != 'm' {
				continue
			}
			flush()
			st = st.apply(params)
			cur = st
		}
		flush()
		out = append(out, monoLine{text: b.String(), spans: spans})
	}
	return out
}

// escape reads the escape sequence at s[i]: where it ends, a CSI's
// parameters and final byte (0 for any other sequence).
func escape(s string, i int) (end int, params string, final byte) {
	if i+1 >= len(s) {
		return len(s), "", 0
	}
	switch s[i+1] {
	case '[':
		j := i + 2
		for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
			j++
		}
		if j >= len(s) {
			return len(s), "", 0
		}
		return j + 1, s[i+2 : j], s[j]
	case ']': // OSC, to BEL or ST
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1, "", 0
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2, "", 0
			}
		}
		return len(s), "", 0
	}
	return i + 2, "", 0
}

// apply is st after SGR parameters p.
func (st sgr) apply(p string) sgr {
	if p == "" {
		p = "0"
	}
	ps := strings.Split(p, ";")
	for k := 0; k < len(ps); k++ {
		n, err := strconv.Atoi(ps[k])
		if err != nil {
			continue
		}
		switch {
		case n == 0:
			st = sgr{fg: -1}
		case n == 1:
			st.bold = true
		case n == 22:
			st.bold = false
		case n >= 30 && n <= 37:
			st.fg = int16(n - 30)
		case n >= 90 && n <= 97:
			st.fg = int16(n - 90 + 8)
		case n == 39:
			st.fg = -1
		case n == 38 && k+2 < len(ps) && ps[k+1] == "5":
			st.fg = int16(channel(ps[k+2]))
			k += 2
		case n == 38 && k+4 < len(ps) && ps[k+1] == "2":
			st.fg, st.rgb = -2, color.NRGBA{R: channel(ps[k+2]), G: channel(ps[k+3]), B: channel(ps[k+4]), A: 0xff}
			k += 4
		case n == 48 && k+1 < len(ps): // a background color's parameters
			if ps[k+1] == "5" {
				k += 2
			} else if ps[k+1] == "2" {
				k += 4
			}
		}
	}
	return st
}

// channel is an SGR color parameter, 0-255: past 255 it is 255, and what
// is not a number is 0.
func channel(p string) uint8 {
	n, _ := strconv.ParseUint(p, 10, 8) // a range error gives 255
	return uint8(n)
}

// xterm256 is palette color n past the 16 the theme sets.
func xterm256(n int) color.NRGBA {
	if n >= 232 {
		v := uint8(8 + 10*(n-232))
		return color.NRGBA{R: v, G: v, B: v, A: 0xff}
	}
	n -= 16
	lv := func(c int) uint8 {
		if c == 0 {
			return 0
		}
		return uint8(55 + 40*c)
	}
	return color.NRGBA{R: lv(n / 36), G: lv(n / 6 % 6), B: lv(n % 6), A: 0xff}
}
