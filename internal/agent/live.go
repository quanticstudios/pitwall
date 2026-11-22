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
	switch {
	case at >= 0 && onAnchor:
		block = append(block, "  "+cutCols(rows[at], 2))
		at, lv.Top = at+1, true
	case at >= 0:
		at, lv.Top = at+1, true
	case echoed:
		return lv // the reply has not begun
	default:
		at = start // the block's top scrolled off
	}
	for _, r := range rows[at:] {
		if c, _ := utf8.DecodeRuneInString(r); r != "" && c != ' ' {
			break
		}
		if tool(r) {
			return Live{Thinking: lv.Thinking, Command: lv.Command, Status: lv.Status} // a tool call
		}
		block = append(block, r)
	}
	for len(block) > 0 && blank(block[0]) {
		block = block[1:]
	}
	for len(block) > 0 && blank(block[len(block)-1]) {
		block = block[:len(block)-1]
	}
	// why: Claude Code 2.1.330 draws a call that runs as a bulleted "Running 1 shell command…" row.
	if p == model.ProviderClaude && len(block) == 1 && (strings.HasSuffix(block[0], "…") || strings.Contains(block[0], "(ctrl+o to expand)")) {
		block = nil
	}
	if len(block) == 0 {
		lv.Top = false
		return lv
	}
	for i, r := range block {
		block[i] = cutCols(r, 2)
	}
	lv.Rows, lv.Width = block, g.Cols-2
	return lv
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

// Unwrap joins rows a TUI wrapped at width columns back into lines: a row
// whose first word would have fitted on the row above starts a line of
// its own, as does a list item, and a blank row stays a paragraph break.
// shortcut: widths are runes, so wide characters join early; count cells if CJK replies wrap wrong.
func Unwrap(rows []string, width int) string {
	var b strings.Builder
	prev := ""
	for i, r := range rows {
		r = strings.TrimRight(r, " ")
		body := strings.TrimLeft(r, " ")
		word, _, _ := strings.Cut(body, " ")
		// why: a TUI keeps a margin or two at the right edge, so a row that wrapped can end short of width.
		wrapped := i > 0 && body != "" && prev != "" && !listItem.MatchString(body) &&
			utf8.RuneCountInString(prev)+1+utf8.RuneCountInString(word) > width-4
		switch {
		case i == 0:
		case wrapped:
			b.WriteByte(' ')
			r = body
		default:
			b.WriteByte('\n')
		}
		b.WriteString(r)
		prev = r
	}
	return b.String()
}

// listItem is a row that opens a list item or a quote.
var listItem = regexp.MustCompile(`^(?:[-*•◦>|]|\d+[.)])\s`)

// Fold is s cut to letters and digits, lower case, up to n bytes of
// them: a screen's text and the markdown it was drawn from fold alike,
// whatever the TUI made of its marks, wrapping and spacing. A link's
// target, which a TUI does not draw, is cut too.
func Fold(s string, n int) string {
	var b strings.Builder
	for i := 0; i < len(s) && b.Len() < n; {
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
