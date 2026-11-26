package agent

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// How each TUI draws a reply's markdown, as Claude Code 2.1.332, Codex
// 0.172 and agy 1.5.18 do at 100 columns (testdata/*-md-*.txt):
//
//   - bold, italic and struck text in the cell attributes; Claude Code
//     leaves ~~ as typed. Codex colors bold and italic text too.
//   - inline code in a color of its own, no attributes.
//   - headings: Codex keeps the #s. Claude Code draws a level 1 bold,
//     italic and underlined, the rest bold. agy draws a level 1 bold
//     after a bold space, 2 bold and underlined, 4 bold and italic, 3
//     bold. A bold line can be any of them: it stays bold.
//   - lists: Claude Code keeps - and 1., and letters a line in under a
//     number; Codex and agy draw • for -.
//   - quotes behind ▎ (Claude Code, its text italic), │ (agy) or > (Codex,
//     the row in one color).
//   - code blocks without fences, in syntax colors: Claude Code the 16
//     terminal colors, which its prose never uses; Codex and agy every
//     cell colored, agy two columns further in.
//   - tables in box-drawing characters: Claude Code boxed, a rule under
//     each row; agy │ between columns, ┼ in the rule under the header;
//     Codex columns in spaces, ━ under the header, ─ between rows.
//   - links as OSC 8: Claude Code draws "text (url)", only the url linked;
//     Codex "text (url)" and agy "text url", both linked.
//   - a paragraph wrapped at the pane's width.

// LiveMarkdown is the markdown agent p drew as rows, a Live's Cells,
// whose text wraps at width columns: each block's marks read back from
// the cells' attributes, colors and links, and the rows the TUI wrapped
// joined back into lines. A style it cannot tell apart from another
// stays plain text.
func LiveMarkdown(p model.Provider, rows [][]vt.Cell, width int) string {
	var segs [][][]vt.Cell
	for i := 0; i < len(rows); {
		if blankRow(rows[i]) {
			i++
			continue
		}
		j := i
		for j < len(rows) && !blankRow(rows[j]) {
			j++
		}
		segs, i = append(segs, rows[i:j]), j
	}
	var out []string
	inList := false
	for i := 0; i < len(segs); i++ {
		seg := segs[i]
		switch {
		case len(seg) == 1 && ruleRow(cellText(seg[0])):
			out = append(out, "---")
		case isTable(seg):
			out = append(out, table(seg))
		case isCode(p, seg):
			// why: a blank row in a code block splits it into two runs of rows on screen.
			for i+1 < len(segs) && isCode(p, segs[i+1]) {
				seg = slices.Concat(seg, [][]vt.Cell{nil}, segs[i+1])
				i++
			}
			out = append(out, fence(p, seg))
		default:
			var s string
			s, inList = proseRows(p, seg, width, inList)
			out = append(out, s)
		}
	}
	return strings.Join(out, "\n\n")
}

// cellText is cells' text.
func cellText(cells []vt.Cell) string {
	var b strings.Builder
	for _, c := range cells {
		b.WriteString(c.Content)
	}
	return b.String()
}

func blankRow(cells []vt.Cell) bool { return strings.TrimSpace(cellText(cells)) == "" }

// ruleRow reports whether row draws only a box's lines.
func ruleRow(row string) bool {
	row = strings.TrimSpace(row)
	return row != "" && strings.ContainsAny(row, "─━") && strings.Trim(row, "─━┼┬┴├┤┌┐└┘│ ") == ""
}

// rowColor is the one foreground color all of cells' text is in, 0 for
// none or several.
func rowColor(cells []vt.Cell) vt.Color {
	var fg vt.Color
	for _, c := range cells {
		switch {
		case c.Content == " ":
		case c.FG == 0 || fg != 0 && c.FG != fg:
			return 0
		default:
			fg = c.FG
		}
	}
	return fg
}

// common is the attributes all of cells' text has.
func common(cells []vt.Cell) vt.Attr {
	a, seen := ^vt.Attr(0), false
	for _, c := range cells {
		if c.Content != " " {
			a &= c.Attrs
			seen = true
		}
	}
	if !seen {
		return 0
	}
	return a
}

// isTable reports whether seg is a table: a rule row with a row of text.
func isTable(seg [][]vt.Cell) bool {
	rule, text := false, false
	for _, r := range seg {
		if ruleRow(cellText(r)) {
			rule = true
		} else {
			text = true
		}
	}
	return rule && text
}

// table is seg, a table drawn in box-drawing characters, as markdown.
func table(seg [][]vt.Cell) string {
	// Columns are split at │, or for a table without one, as Codex draws,
	// at the runs of ━ in the rule under its header.
	var at []int
	pipes := slices.ContainsFunc(seg, func(r []vt.Cell) bool {
		return !ruleRow(cellText(r)) && strings.ContainsRune(cellText(r), '│')
	})
	if !pipes {
		for _, r := range seg {
			if t := cellText(r); ruleRow(t) && strings.ContainsRune(t, '━') {
				for x, c := range r {
					if c.Content == "━" && (x == 0 || r[x-1].Content != "━") {
						at = append(at, x)
					}
				}
				break
			}
		}
	}
	split := func(r []vt.Cell) [][]vt.Cell {
		var cols [][]vt.Cell
		if pipes {
			from := 0
			for x, c := range r {
				if c.Content == "│" {
					cols, from = append(cols, r[from:x]), x+1
				}
			}
			cols = append(cols, r[from:])
			if len(cols) > 1 && blankRow(cols[0]) && strings.HasPrefix(strings.TrimSpace(cellText(r)), "│") {
				cols = cols[1:]
			}
			if len(cols) > 1 && blankRow(cols[len(cols)-1]) && strings.HasSuffix(cellText(r), "│") {
				cols = cols[:len(cols)-1]
			}
			return cols
		}
		for i, x := range at {
			end := len(r)
			if i+1 < len(at) {
				end = min(end, at[i+1])
			}
			lo := min(x, len(r))
			cols = append(cols, r[lo:max(lo, end)])
		}
		return cols
	}
	// A table row is the rows between two rules where the TUI draws a
	// rule between its rows; else, as agy draws, each row after the
	// header is one. A header can take rows of its own either way.
	grouped := false
	phase, body := 0, false // phase: 0 above the header, 1 the header, 2 the rows
	for _, r := range seg {
		switch {
		case !ruleRow(cellText(r)) && phase == 0:
			phase = 1
		case !ruleRow(cellText(r)) && phase == 2:
			body = true
		case phase == 1:
			phase = 2
		case body:
			grouped = true
		}
	}
	var rows [][][]vt.Cell // each table row's columns
	open := false
	phase = 0
	for _, r := range seg {
		if ruleRow(cellText(r)) {
			if phase == 1 {
				phase = 2
			}
			open = false
			continue
		}
		cols := split(r)
		if phase == 0 {
			phase = 1
		}
		if open && (phase == 1 || grouped) {
			last := rows[len(rows)-1]
			for c := range min(len(cols), len(last)) {
				last[c] = joinCells(trimCells(last[c]), trimCells(cols[c]))
			}
			continue
		}
		rows, open = append(rows, cols), true
	}
	if len(rows) == 0 {
		return ""
	}
	n := 0
	for _, r := range rows {
		n = max(n, len(r))
	}
	// The TUI's header style: the reader bolds a header itself.
	drop := vt.Bold
	for _, c := range rows[0] {
		if c := trimCells(c); len(c) > 0 && common(c)&vt.Bold == 0 {
			drop = 0
		}
	}
	var b strings.Builder
	for i, r := range rows {
		cells := make([]string, n)
		for c := range min(n, len(r)) {
			d := vt.Attr(0)
			if i == 0 {
				d = drop
			}
			cells[c] = strings.ReplaceAll(inline(trimCells(r[c]), 0, d), "|", `\|`)
		}
		b.WriteString("| " + strings.Join(cells, " | ") + " |\n")
		if i == 0 {
			b.WriteString("|" + strings.Repeat(" --- |", n) + "\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// trimCells is cells without the blank ones at either end.
func trimCells(cells []vt.Cell) []vt.Cell {
	for len(cells) > 0 && cells[0].Content == " " {
		cells = cells[1:]
	}
	for len(cells) > 0 && cells[len(cells)-1].Content == " " {
		cells = cells[:len(cells)-1]
	}
	return cells
}

// isCode reports whether seg is a run of a code block's rows, as p
// colors them. One color all through could be anything: it is not.
func isCode(p model.Provider, seg [][]vt.Cell) bool {
	colors := map[vt.Color]bool{}
	for _, r := range seg {
		for _, c := range r {
			switch {
			case c.Content == " ":
			case p == model.ProviderClaude:
				if c.FG&^0xffffff == vt.PaletteFlag {
					return true
				}
			case c.FG == 0 || c.Link != "" || c.Attrs&(vt.Italic|vt.Underline) != 0:
				return false
			default:
				colors[c.FG] = true
			}
		}
	}
	return len(colors) > 1
}

// fence is seg, a code block's rows, fenced.
func fence(p model.Provider, seg [][]vt.Cell) string {
	lines := make([]string, len(seg))
	in := 0
	if p == model.ProviderAntigravity {
		in = 2
		for _, r := range seg {
			if t := cellText(r); strings.TrimSpace(t) != "" {
				in = min(in, len(t)-len(strings.TrimLeft(t, " ")))
			}
		}
	}
	mark := "```"
	for i, r := range seg {
		lines[i] = strings.TrimRight(cellText(r[min(in, len(r)):]), " ")
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			mark = "~~~"
		}
	}
	return mark + "\n" + strings.Join(lines, "\n") + "\n" + mark
}

// marker is a list item's marker at the start of a row's text: a bullet,
// a number, or a letter as Claude Code numbers a list in a numbered one.
var marker = regexp.MustCompile(`^(?:[•◦▪‣\-*+]|\d{1,9}[.)]|[a-z][.)])(?: |$)`)

// line is a row of prose, or rows the TUI wrapped it into.
type line struct {
	indent  int
	mark    string // the list marker, as markdown
	quote   bool
	heading int // its level; -1 for one Codex draws with its #s
	cells   []vt.Cell
	last    string // the text of the last row it was read from
}

// proseRows is seg, a run of prose rows, as markdown, and whether it ends
// in a list; inList says the one before did.
func proseRows(p model.Provider, seg [][]vt.Cell, width int, inList bool) (string, bool) {
	var lines []line
	for _, r := range seg {
		t := cellText(r)
		in := len(t) - len(strings.TrimLeft(t, " "))
		body := r[in:]
		bt := cellText(body)
		l := line{indent: in, cells: body, last: t}
		switch q, _ := utf8.DecodeRuneInString(bt); {
		case strings.ContainsRune("▎│>", q) && (len(bt) == utf8.RuneLen(q) || bt[utf8.RuneLen(q)] == ' '):
			l.quote, l.cells = true, trimCells(body[1:])
		case marker.MatchString(bt) && (bt[0] < 'a' || bt[0] > 'z' || in > 0 && p == model.ProviderClaude):
			mk := marker.FindString(bt)
			m := strings.TrimSpace(mk)
			switch {
			case strings.ContainsAny(m, "•◦▪‣*+"):
				m = "-"
			case m[0] >= 'a' && m[0] <= 'z':
				m = strconv.Itoa(int(m[0]-'a'+1)) + m[1:]
			}
			l.mark, l.cells = m, trimCells(body[utf8.RuneCountInString(mk):])
		default:
			l.heading = heading(p, r, in)
		}
		if n := len(lines); n > 0 && l.mark == "" && l.heading == 0 && lines[n-1].heading == 0 && l.quote == lines[n-1].quote &&
			wraps(lines[n-1].last, cellText(l.cells), width) {
			prev := &lines[n-1]
			prev.cells = joinCells(prev.cells, l.cells)
			prev.last = t
			continue
		}
		lines = append(lines, l)
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if l.mark != "" {
			inList = true
		} else if l.indent == 0 {
			inList = false
		}
		var s string
		switch {
		case l.heading < 0:
			s = cellText(l.cells)
		case l.heading > 0:
			s = strings.Repeat("#", l.heading) + " " + strings.TrimSpace(cellText(l.cells))
		case l.quote:
			drop := vt.Attr(0)
			if p == model.ProviderClaude && common(l.cells)&vt.Italic != 0 {
				drop = vt.Italic // its quote style
			}
			s = "> " + inline(l.cells, rowColor(l.cells), drop)
		default:
			s = inline(l.cells, rowColor(l.cells), 0)
			if l.mark != "" {
				s = l.mark + " " + s
			}
		}
		if inList && l.heading == 0 {
			s = strings.Repeat(" ", l.indent) + s
		}
		out[i] = s
	}
	return strings.Join(out, "\n"), inList
}

// heading is the level of the heading p draws as row, whose text starts
// in columns, 0 for none.
func heading(p model.Provider, row []vt.Cell, in int) int {
	body := row[in:]
	if slices.ContainsFunc(body, func(c vt.Cell) bool { return c.Link != "" }) {
		return 0
	}
	a := common(body)
	switch p {
	case model.ProviderCodex:
		if h := strings.TrimLeft(cellText(body), "#"); len(h) < len(cellText(body)) && len(cellText(body))-len(h) <= 6 && strings.HasPrefix(h, " ") {
			return -1
		}
	case model.ProviderClaude:
		if a&(vt.Bold|vt.Italic|vt.Underline) == vt.Bold|vt.Italic|vt.Underline {
			return 1
		}
	case model.ProviderAntigravity:
		switch {
		case a&(vt.Bold|vt.Underline) == vt.Bold|vt.Underline:
			return 2
		case a&(vt.Bold|vt.Italic) == vt.Bold|vt.Italic:
			return 4
		case a&vt.Bold != 0 && in > 0 && row[in-1].Attrs&vt.Bold != 0:
			return 1
		}
	}
	return 0
}

// wraps reports whether a row of text body continues the row prev, which
// the TUI wrapped at width columns: its first word would not have fitted
// on prev. A list item starts a line of its own.
func wraps(prev, body string, width int) bool {
	body = strings.TrimLeft(body, " ")
	word, _, _ := strings.Cut(body, " ")
	// why: a TUI keeps a margin or two at the right edge, so a row that wrapped can end short of width.
	return body != "" && strings.TrimSpace(prev) != "" && !marker.MatchString(body) &&
		utf8.RuneCountInString(strings.TrimRight(prev, " "))+1+utf8.RuneCountInString(word) > width-4
}

// joinCells is a and b, the rows a line wrapped into, joined by a space
// in their style where both sides share it; a link the TUI broke across
// the rows joins without one.
func joinCells(a, b []vt.Cell) []vt.Cell {
	if len(a) == 0 || len(b) == 0 {
		return slices.Concat(a, b)
	}
	l, r := a[len(a)-1], b[0]
	if l.Link != "" && l.Link == r.Link {
		return slices.Concat(a, b)
	}
	sp := vt.Cell{Content: " ", Width: 1}
	if l.Attrs == r.Attrs && l.FG == r.FG {
		sp.Attrs, sp.FG = l.Attrs, l.FG
	}
	return slices.Concat(a, []vt.Cell{sp}, b)
}

// span is a run of cells in one style.
type span struct {
	text                       string
	bold, italic, strike, code bool
	link                       string
}

// inline is cells' text with its marks: bold, italic, struck, code and
// links. rowFG is a color the whole line is in, which is the TUI's style
// for the line, not code; drop are attributes that are the line's style.
func inline(cells []vt.Cell, rowFG vt.Color, drop vt.Attr) string {
	var spans []span
	for _, c := range cells {
		a := c.Attrs &^ drop
		s := span{text: c.Content, link: c.Link}
		if c.Content != " " || len(spans) == 0 {
			s.code = c.FG != 0 && c.FG != rowFG && c.Attrs&(vt.Bold|vt.Italic|vt.Underline) == 0 && c.Link == ""
			s.bold, s.italic, s.strike = a&vt.Bold != 0 && !s.code, a&vt.Italic != 0 && !s.code, a&vt.Strike != 0
		} else {
			// A space takes the style of the span it is in, so a bold phrase stays one span.
			p := spans[len(spans)-1]
			s.bold, s.italic, s.strike, s.code = p.bold, p.italic, p.strike, p.code
			if c.Link != p.link {
				s = span{text: " "}
			}
		}
		if n := len(spans); n > 0 && sameSpan(spans[n-1], s) {
			spans[n-1].text += s.text
			continue
		}
		spans = append(spans, s)
	}
	var b strings.Builder
	skip := false // a ")" after a url that went into a link
	for i, s := range spans {
		t := s.text
		if skip {
			t, skip = strings.TrimPrefix(t, ")"), false
		}
		switch {
		case s.link != "" && t == s.link:
			out := b.String()
			switch {
			// Codex: "text (url)"; agy: "text url". The text went in as a link to url.
			case strings.HasSuffix(out, "]("+s.link+") ("):
				b.Reset()
				b.WriteString(strings.TrimSuffix(out, " ("))
				skip = true
			case strings.HasSuffix(out, "]("+s.link+") "):
				b.Reset()
				b.WriteString(strings.TrimSuffix(out, " "))
			// Claude Code: "text (url)", only url linked. The word before is
			// all of its text that can be told; more stays plain.
			case strings.HasSuffix(out, " (") && wordEnd(out[:len(out)-2]) < len(out)-2:
				w := wordEnd(out[:len(out)-2])
				b.Reset()
				b.WriteString(out[:w] + "[" + out[w:len(out)-2] + "](" + s.link + ")")
				skip = i+1 < len(spans)
			default:
				b.WriteString(t) // a bare url, which the reader links
			}
		case s.link != "" && strings.HasPrefix(s.link, t):
			b.WriteString(t) // the url, still drawing
		case s.link != "":
			b.WriteString("[" + strings.TrimSpace(t) + "](" + s.link + ")")
		default:
			b.WriteString(marked(t, s))
		}
	}
	return b.String()
}

func sameSpan(a, b span) bool {
	return a.bold == b.bold && a.italic == b.italic && a.strike == b.strike && a.code == b.code && a.link == b.link
}

// wordEnd is where the last word of s, letters and digits, starts; len(s)
// for none.
func wordEnd(s string) int {
	i := len(s)
	for i > 0 {
		r, n := utf8.DecodeLastRuneInString(s[:i])
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			break
		}
		i -= n
	}
	return i
}

// marked is t in span s's marks, the spaces at either end outside them,
// as markdown wants.
func marked(t string, s span) string {
	body := strings.TrimSpace(t)
	if body == "" || !s.bold && !s.italic && !s.strike && !s.code {
		return t
	}
	lead, trail := t[:strings.Index(t, body)], t[strings.Index(t, body)+len(body):]
	if s.code {
		tick := "`"
		if strings.Contains(body, "`") {
			tick = "`` "
			return lead + tick + body + " ``" + trail
		}
		return lead + tick + body + tick + trail
	}
	open := ""
	if s.strike {
		open += "~~"
	}
	if s.bold {
		open += "**"
	}
	if s.italic {
		open += "*"
	}
	close := []rune(open)
	slices.Reverse(close)
	return lead + open + body + string(close) + trail
}
