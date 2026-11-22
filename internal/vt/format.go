package vt

import (
	"fmt"
	"html"
	"strings"
	"unicode"
)

// Format names accepted by FormatGrid and CaptureScrollbackFormat.
const (
	FormatText = "text"
	FormatANSI = "ansi"
	FormatHTML = "html"
)

// ValidFormat reports whether format is "", "text", "ansi" or "html".
func ValidFormat(format string) bool {
	switch format {
	case "", FormatText, FormatANSI, FormatHTML:
		return true
	}
	return false
}

// FormatGrid renders g in format ("text", "ansi" or "html") using pal for
// HTML theme colors. An empty format means "text".
func FormatGrid(g Grid, format string, pal Palette) (string, error) {
	if !ValidFormat(format) {
		return "", fmt.Errorf("unknown format %q: want text, ansi or html", format)
	}
	rows := make([][]Cell, g.Rows)
	for y := range g.Rows {
		rows[y] = g.Cells[y*g.Cols : (y+1)*g.Cols]
	}
	return formatRows(rows, format, pal, false), nil
}

// CaptureScrollbackFormat captures history and screen rows under the caller's
// lock and returns a function that formats them in format ("text", "ansi" or
// "html") without holding any emulator lock. When lines > 0, only the last
// lines logical lines are kept.
func CaptureScrollbackFormat(e Emulator, format string, lines int, pal Palette) (func() string, error) {
	if !ValidFormat(format) {
		return nil, fmt.Errorf("unknown format %q: want text, ansi or html", format)
	}
	if lines < 0 {
		return nil, fmt.Errorf("lines must be non-negative")
	}
	if format == "" || format == FormatText {
		build := CaptureScrollback(e)
		return func() string {
			return tailTextLines(build(), lines)
		}, nil
	}
	if c, ok := e.(interface {
		ScrollbackCellsSnapshot() func() [][]Cell
	}); ok {
		snap := c.ScrollbackCellsSnapshot()
		return func() string {
			rows := snap()
			if lines > 0 && len(rows) > lines {
				rows = rows[len(rows)-lines:]
			}
			return formatRows(rows, format, pal, true)
		}, nil
	}
	g := e.Snapshot()
	return func() string {
		rows := joinWrappedGrid(g)
		if lines > 0 && len(rows) > lines {
			rows = rows[len(rows)-lines:]
		}
		return formatRows(rows, format, pal, true)
	}, nil
}

// ScrollbackCellsSnapshot captures history and screen under t.mu and returns a
// builder that joins soft-wrapped rows into logical cell lines with trailing
// empty rows trimmed.
func (t *emulator) ScrollbackCellsSnapshot() func() [][]Cell {
	t.mu.Lock()
	g := t.snapshot()
	var history []line
	if !g.AltScreen {
		history = make([]line, t.st.hist.len())
		for i := range history {
			history[i] = *t.st.hist.at(i)
		}
	}
	t.mu.Unlock()
	return func() [][]Cell {
		if g.Rows == 0 || g.Cols == 0 {
			return nil
		}
		var out [][]Cell
		var cur []Cell
		appendRow := func(cells []Cell, wrapped bool) {
			cur = append(cur, cells...)
			if !wrapped {
				out = append(out, cur)
				cur = nil
			}
		}
		for i := range history {
			l := &history[i]
			row := make([]Cell, l.width())
			l.fill(row)
			appendRow(row, l.wrapped())
		}
		for y := range g.Rows {
			wrapped := y < len(g.Wrapped) && g.Wrapped[y]
			row := g.Cells[y*g.Cols : (y+1)*g.Cols]
			appendRow(row, wrapped)
		}
		if cur != nil {
			out = append(out, cur)
		}
		return trimTrailingEmptyRows(out)
	}
}

func joinWrappedGrid(g Grid) [][]Cell {
	if g.Rows == 0 || g.Cols == 0 {
		return nil
	}
	var out [][]Cell
	var cur []Cell
	for y := range g.Rows {
		wrapped := y < len(g.Wrapped) && g.Wrapped[y]
		cur = append(cur, g.Cells[y*g.Cols:(y+1)*g.Cols]...)
		if !wrapped {
			out = append(out, cur)
			cur = nil
		}
	}
	if cur != nil {
		out = append(out, cur)
	}
	return trimTrailingEmptyRows(out)
}

func tailTextLines(text string, lines int) string {
	if lines <= 0 || text == "" {
		return text
	}
	end := len(strings.TrimSuffix(text, "\n"))
	for range lines {
		i := strings.LastIndexByte(text[:end], '\n')
		if i < 0 {
			end = -1
			break
		}
		end = i
	}
	return text[end+1:]
}

func trimTrailingEmptyRows(rows [][]Cell) [][]Cell {
	end := len(rows)
	for end > 0 && rowEmpty(rows[end-1]) {
		end--
	}
	return rows[:end]
}

func rowEmpty(cells []Cell) bool {
	return trimmedLen(cells, true) == 0
}

// trimmedLen returns the length of cells excluding trailing unstyled blanks.
// When keepStyled is false (plain text), trailing spaces go regardless of style.
func trimmedLen(cells []Cell, keepStyled bool) int {
	n := len(cells)
	for n > 0 {
		c := cells[n-1]
		if c.Width == 0 && c.Content == "" {
			// Trailing half of a wide char: keep it if its leading half is kept.
			if n >= 2 && cells[n-2].Width > 1 {
				break
			}
			n--
			continue
		}
		blank := c.Content == "" || c.Content == " "
		styled := keepStyled && (c.BG != 0 || c.Attrs&(Reverse|Underline|Strike) != 0)
		if !blank || styled {
			break
		}
		n--
	}
	return n
}

func formatRows(rows [][]Cell, format string, pal Palette, trailingNL bool) string {
	switch format {
	case "", FormatText:
		return formatPlain(rows, trailingNL)
	case FormatANSI:
		return formatANSI(rows, trailingNL)
	case FormatHTML:
		return formatHTML(rows, pal, trailingNL)
	default:
		return ""
	}
}

func formatPlain(rows [][]Cell, trailingNL bool) string {
	rows = trimTrailingEmptyRows(rows)
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		n := trimmedLen(row, false)
		for _, c := range row[:n] {
			switch {
			case c.Width == 0:
			case c.Content == "":
				b.WriteByte(' ')
			default:
				b.WriteString(c.Content)
			}
		}
	}
	if trailingNL && b.Len() > 0 {
		b.WriteByte('\n')
	}
	return b.String()
}

type cellStyle struct {
	fg, bg Color
	attrs  Attr
	link   string
}

func cellText(c Cell) string {
	if c.Content == "" {
		return " "
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, c.Content)
}

func formatANSI(rows [][]Cell, trailingNL bool) string {
	rows = trimTrailingEmptyRows(rows)
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		n := trimmedLen(row, true)
		var cur cellStyle
		styled := false
		for _, c := range row[:n] {
			if c.Width == 0 {
				continue
			}
			st := cellStyle{fg: c.FG, bg: c.BG, attrs: c.Attrs}
			if st != cur {
				writeSGR(&b, st)
				cur = st
				styled = st != (cellStyle{})
			}
			b.WriteString(cellText(c))
		}
		if styled {
			b.WriteString("\x1b[0m")
		}
	}
	if trailingNL && b.Len() > 0 {
		b.WriteByte('\n')
	}
	return b.String()
}

func writeSGR(b *strings.Builder, st cellStyle) {
	if st == (cellStyle{}) {
		b.WriteString("\x1b[0m")
		return
	}
	var params []string
	params = append(params, "0")
	for _, a := range []struct {
		attr Attr
		sgr  string
	}{
		{Bold, "1"}, {Faint, "2"}, {Italic, "3"}, {Underline, "4"},
		{Blink, "5"}, {Reverse, "7"}, {Invisible, "8"}, {Strike, "9"},
	} {
		if st.attrs&a.attr != 0 {
			params = append(params, a.sgr)
		}
	}
	if s := colorSGR(st.fg, false); s != "" {
		params = append(params, s)
	}
	if s := colorSGR(st.bg, true); s != "" {
		params = append(params, s)
	}
	b.WriteString("\x1b[")
	b.WriteString(strings.Join(params, ";"))
	b.WriteByte('m')
}

func colorSGR(c Color, bg bool) string {
	switch c &^ 0xffffff {
	case PaletteFlag:
		idx := c & 255
		if idx < 8 {
			base := 30
			if bg {
				base = 40
			}
			return fmt.Sprintf("%d", base+int(idx))
		}
		if idx < 16 {
			base := 90
			if bg {
				base = 100
			}
			return fmt.Sprintf("%d", base+int(idx-8))
		}
		code := 38
		if bg {
			code = 48
		}
		return fmt.Sprintf("%d;5;%d", code, idx)
	case RGBFlag:
		code := 38
		if bg {
			code = 48
		}
		return fmt.Sprintf("%d;2;%d;%d;%d", code, c>>16&255, c>>8&255, c&255)
	}
	return ""
}

func formatHTML(rows [][]Cell, pal Palette, trailingNL bool) string {
	if pal == (Palette{}) {
		pal = DefaultPalette
	}
	rows = trimTrailingEmptyRows(rows)
	var b strings.Builder
	fmt.Fprintf(&b, `<pre class="pitwall-screen" style="background:#%06x;color:#%06x;font-family:ui-monospace,SFMono-Regular,Menlo,Monaco,Consolas,monospace;font-size:13px;line-height:1.4;padding:12px 16px;margin:0;border-radius:6px;overflow-x:auto">`, pal.Bg&0xffffff, pal.Fg&0xffffff)
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		n := trimmedLen(row, true)
		var cur cellStyle
		var run strings.Builder
		flush := func() {
			if run.Len() == 0 {
				return
			}
			escaped := html.EscapeString(run.String())
			run.Reset()
			css := htmlStyle(cur, pal)
			link := safeLink(cur.link)
			if link != "" {
				fmt.Fprintf(&b, `<a href="%s" style="color:inherit">`, html.EscapeString(link))
			}
			if css != "" {
				fmt.Fprintf(&b, `<span style="%s">%s</span>`, css, escaped)
			} else {
				b.WriteString(escaped)
			}
			if link != "" {
				b.WriteString("</a>")
			}
		}
		for _, c := range row[:n] {
			if c.Width == 0 {
				continue
			}
			st := cellStyle{fg: c.FG, bg: c.BG, attrs: c.Attrs, link: c.Link}
			if st != cur {
				flush()
				cur = st
			}
			run.WriteString(cellText(c))
		}
		flush()
	}
	b.WriteString("</pre>")
	if trailingNL {
		b.WriteByte('\n')
	}
	return b.String()
}

func htmlStyle(st cellStyle, pal Palette) string {
	fg, hasFG := resolveColor(st.fg, pal)
	bg, hasBG := resolveColor(st.bg, pal)
	if st.attrs&Reverse != 0 {
		if !hasFG {
			fg = pal.Fg & 0xffffff
		}
		if !hasBG {
			bg = pal.Bg & 0xffffff
		}
		fg, bg = bg, fg
		hasFG, hasBG = true, true
	}
	var parts []string
	if st.attrs&Invisible != 0 {
		parts = append(parts, "color:transparent")
	} else if hasFG {
		parts = append(parts, fmt.Sprintf("color:#%06x", fg))
	}
	if hasBG {
		parts = append(parts, fmt.Sprintf("background:#%06x", bg))
	}
	if st.attrs&Bold != 0 {
		parts = append(parts, "font-weight:700")
	}
	if st.attrs&Faint != 0 {
		parts = append(parts, "opacity:0.6")
	}
	if st.attrs&Italic != 0 {
		parts = append(parts, "font-style:italic")
	}
	switch {
	case st.attrs&Underline != 0 && st.attrs&Strike != 0:
		parts = append(parts, "text-decoration:underline line-through")
	case st.attrs&Underline != 0:
		parts = append(parts, "text-decoration:underline")
	case st.attrs&Strike != 0:
		parts = append(parts, "text-decoration:line-through")
	}
	return strings.Join(parts, ";")
}

func resolveColor(c Color, pal Palette) (uint32, bool) {
	switch c &^ 0xffffff {
	case PaletteFlag:
		idx := int(c & 255)
		if idx < 16 {
			return pal.ANSI[idx] & 0xffffff, true
		}
		if idx < 232 {
			n := idx - 16
			levels := [6]uint32{0, 95, 135, 175, 215, 255}
			r := levels[n/36]
			g := levels[(n/6)%6]
			b := levels[n%6]
			return r<<16 | g<<8 | b, true
		}
		v := uint32(8 + 10*(idx-232))
		return v<<16 | v<<8 | v, true
	case RGBFlag:
		return uint32(c) & 0xffffff, true
	}
	return 0, false
}

func safeLink(u string) string {
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return ""
}
