package agent

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Live is what an agent's TUI draws of its turn before its transcript has
// it: the reply block it streams, and for Antigravity, which writes a step
// only once it ends, the thought on its spinner row and the command it runs.
type Live struct {
	// Rows are the reply block's rows, top to bottom, with the two columns
	// of its bullet or indent cut. Top says Rows starts at the block's first
	// row; else the block's top has scrolled off the screen.
	Rows []string
	Top  bool
	// Cells are Rows' cells, row for row, cut the same way: what
	// LiveMarkdown reads the reply's markdown back from.
	Cells [][]vt.Cell
	// Width is the columns a row of the block's text wraps at.
	Width    int
	Thinking string
	Command  string
	// Status is the text on Claude Code's spinner row, such as
	// "Compacting conversation… (5s)", "" with none on screen.
	Status string
}

// LiveScreen reads Live off agent p's screen g: Claude Code, Codex or
// Antigravity. It takes only a block it can place for certain: the last
// one below the turn's prompt echo, opened by a bullet, or for
// Antigravity by a thought or call row, and closed by the next row at the
// left edge, a spinner's or the input box's. A block with a tool's output
// in it is a tool call, not text. The zero Live is a screen with none.
func LiveScreen(p model.Provider, g vt.Grid) Live {
	rows := screenRows(g)
	var lv Live
	var echo, anchor, tool func(string) bool
	onAnchor := true // the text starts on the anchor row, after its bullet
	end, start := -1, 0
	switch p {
	case model.ProviderClaude:
		for i := len(rows) - 2; i >= 0 && end < 0; i-- {
			if isRule(rows[i]) && strings.HasPrefix(rows[i+1], "❯") {
				end = i
			}
		}
		// why: the spinner row sits under the turn's text; what is below it (a tip, a status line some setups print with a bullet) is no reply.
		for i := end - 1; i >= 0 && i >= end-6; i-- {
			if c, size := utf8.DecodeRuneInString(rows[i]); strings.ContainsRune(claudeSpinners, c) && strings.Contains(rows[i], "…") {
				lv.Status, end = strings.TrimSpace(rows[i][size:]), i
				break
			}
		}
		echo = func(r string) bool { return strings.HasPrefix(r, "❯ ") }
		anchor = func(r string) bool {
			return r == "●" || r == "⏺" || strings.HasPrefix(r, "● ") || strings.HasPrefix(r, "⏺ ")
		}
		tool = func(r string) bool { return strings.HasPrefix(strings.TrimLeft(r, " "), "⎿") }
	case model.ProviderCodex:
		for i := len(rows) - 1; i >= 0 && end < 0; i-- {
			if codexGlyph(rows[i]) {
				end = i
			}
		}
		// why: Codex pins the turn's prompt, cut with an ellipsis, over the top row while the turn scrolls.
		if end > 0 && codexGlyph(rows[0]) && strings.HasSuffix(rows[0], "…") {
			start = 1
		}
		echo = func(r string) bool { return strings.HasPrefix(r, "› ") || strings.HasPrefix(r, "» ") }
		anchor = func(r string) bool { return strings.HasPrefix(r, "• ") && !strings.Contains(r, "esc to interrupt") }
		tool = func(r string) bool {
			r = strings.TrimLeft(r, " ")
			return strings.HasPrefix(r, "└") || strings.HasPrefix(r, "│")
		}
	case model.ProviderAntigravity:
		for i := len(rows) - 2; i >= 0 && end < 0; i-- {
			if isRule(rows[i]) && strings.HasPrefix(rows[i+1], ">") {
				end = i
			}
		}
		echo = func(r string) bool { return strings.HasPrefix(r, "> ") }
		// A thought or a call row opens a block; the spinner and its tip do not.
		anchor = func(r string) bool {
			c, _ := utf8.DecodeRuneInString(r)
			return r != "" && c != ' ' && !echo(r) && !spinner(c) && !strings.HasPrefix(r, "└")
		}
		tool = func(string) bool { return false }
		onAnchor = false
	default:
		return lv
	}
	if end < 0 {
		return lv
	}
	rows = rows[:end]
	if p == model.ProviderAntigravity {
		lv.Thinking, lv.Command = agyLive(rows[start:], anchor)
	}
	from, echoed := start, false
	for i := len(rows) - 1; i >= start; i-- {
		if echo(rows[i]) {
			from, echoed = i+1, true
			break
		}
	}
	at := -1
	for i := len(rows) - 1; i >= from; i-- {
		if anchor(rows[i]) {
			at = i
			break
		}
	}
	var block []string
	var ys []int // the screen row of each of block's
	switch {
	case at >= 0 && onAnchor:
		block, ys = append(block, "  "+cutCols(rows[at], 2)), append(ys, at)
		at, lv.Top = at+1, true
	case at >= 0:
		at, lv.Top = at+1, true
	case echoed:
		return lv // the reply has not begun
	default:
		at = start // the block's top scrolled off
	}
	for i, r := range rows[at:] {
		if c, _ := utf8.DecodeRuneInString(r); r != "" && c != ' ' {
			break
		}
		if tool(r) {
			return Live{Thinking: lv.Thinking, Command: lv.Command, Status: lv.Status} // a tool call
		}
		block, ys = append(block, r), append(ys, at+i)
	}
	for len(block) > 0 && blank(block[0]) {
		block, ys = block[1:], ys[1:]
	}
	// why: Claude Code 2.1.332 draws a notice such as "Fast mode is now available" far right under the reply while its spinner blinks off,
	// and Codex 0.172 its "Worked for 5s" under a turn that ended.
	for len(block) > 0 && (blank(block[len(block)-1]) || p == model.ProviderClaude && isNotice(rowCellsFrom(g, ys[len(ys)-1], 0)) ||
		p == model.ProviderCodex && strings.HasPrefix(strings.TrimSpace(block[len(block)-1]), "Worked for ")) {
		block, ys = block[:len(block)-1], ys[:len(ys)-1]
	}
	// why: Claude Code 2.1.330 draws a call that runs as a bulleted "Running 1 shell command…" row.
	if p == model.ProviderClaude && len(block) == 1 && (strings.HasSuffix(block[0], "…") || strings.Contains(block[0], "(ctrl+o to expand)")) {
		block = nil
	}
	if len(block) == 0 {
		lv.Top = false
		return lv
	}
	lv.Cells = make([][]vt.Cell, len(block))
	for i, r := range block {
		block[i], lv.Cells[i] = cutCols(r, 2), rowCellsFrom(g, ys[i], 2)
	}
	lv.Rows, lv.Width = block, g.Cols-2
	return lv
}

// isNotice reports whether row is a notice of Claude Code's, not its
// reply's: one color all through, far right of where the reply's text,
// two columns in, or a list's or code block's a few more, starts.
func isNotice(row []vt.Cell) bool {
	in := 0
	for in < len(row) && row[in].Content == " " {
		in++
	}
	return in >= 24 && in < len(row) && rowColor(row[in:]) != 0
}

// rowCellsFrom is row y of g's cells but its first n, the trailing half
// of a wide character and the blank cells at the end left out, so it
// lines up rune for rune with rowText.
func rowCellsFrom(g vt.Grid, y, n int) []vt.Cell {
	var out []vt.Cell
	for x := range g.Cols {
		c := g.At(x, y)
		switch {
		case c.Width == 0:
			continue
		case n > 0:
			n--
			continue
		case c.Content == "":
			c.Content = " "
		}
		out = append(out, c)
	}
	for len(out) > 0 && out[len(out)-1].Content == " " {
		out = out[:len(out)-1]
	}
	return out
}

// agyRun is an Antigravity call row while its command runs.
var agyRun = regexp.MustCompile(`^[●○◉] Running \((.+?)\)(?: \(ctrl\+o to expand\))?$`)

// agyLive is the thought on Antigravity's spinner row, while it shows one
// rather than a word or two of status, and the command of the call it
// runs, when that call is its last row.
func agyLive(rows []string, anchor func(string) bool) (thought, command string) {
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		c, n := utf8.DecodeRuneInString(r)
		if spinner(c) {
			t := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(r[n:]), "..."))
			if len(strings.Fields(t)) >= 4 {
				thought = t
			}
			continue
		}
		if blank(r) || strings.HasPrefix(r, "└") {
			continue
		}
		if anchor(r) {
			if m := agyRun.FindStringSubmatch(r); m != nil {
				command = m[1]
			}
		}
		break
	}
	return thought, command
}

// spinner reports whether c is a frame of Antigravity's braille spinner.
func spinner(c rune) bool { return c >= 0x2800 && c <= 0x28ff }

// claudeSpinners are the glyphs Claude Code's spinner row cycles through.
const claudeSpinners = "·✢✳✶✻✽*"

// cutCols is row without its first n runes.
func cutCols(row string, n int) string {
	for range n {
		_, size := utf8.DecodeRuneInString(row)
		if size == 0 {
			break
		}
		row = row[size:]
	}
	return row
}

// Fold is s cut to letters and digits, lower case, up to n bytes of
// them: a screen's text and the markdown it was drawn from fold alike,
// whatever the TUI made of its marks, wrapping and spacing. A link's
// target and a code fence's language, which a TUI does not draw, are
// cut too.
func Fold(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < n; {
		if strings.HasPrefix(s[i:], "```") {
			if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
				i += j + 1
				continue
			}
		}
		if strings.HasPrefix(s[i:], "](") {
			if j := strings.IndexByte(s[i:], ')'); j >= 0 {
				i += j + 1
				continue
			}
		}
		c, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			b.WriteRune(unicode.ToLower(c))
		}
	}
	return b.String()
}
