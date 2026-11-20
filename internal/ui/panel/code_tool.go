package panel

import (
	"strings"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/ui/mdview"
	"github.com/quanticstudios/pitwall/internal/ui/syntax"
)

// synLines is a tool box's tokens per line, lexed once per text.
type synLines struct {
	lang, src string
	spans     [][]span
}

// highlight gives the code lines of box id, a Read preview's or an
// edit's, their tokens in lang as spans, lexed once per text. An edit
// lexes each line alone, as its old and new lines interleave; a file's
// lines lex as one text. Lines with spans of their own keep them.
func (d *cdraw) highlight(id, lang string, lines []monoLine, perLine bool) {
	if syntax.Lang(lang) == "" || len(lines) == 0 {
		return
	}
	texts := make([]string, len(lines))
	var sb strings.Builder
	for i, l := range lines {
		texts[i] = mdview.Detab(l.text) // as box draws it, so the runes match
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(texts[i])
	}
	ts := &d.t.tool
	c, ok := ts.syn[id]
	if !ok || c.lang != lang || c.src != sb.String() {
		c = synLines{lang: lang, src: sb.String(), spans: lexLines(lang, texts, perLine)}
		if ts.syn == nil {
			ts.syn = map[string]synLines{}
		}
		ts.syn[id] = c
	}
	for i := range lines {
		if lines[i].spans == nil && (lines[i].kind == 0 || lines[i].kind == '+' || lines[i].kind == '-' || lines[i].kind == ' ') {
			lines[i].spans = c.spans[i]
		}
	}
}

// lexLines is texts' tokens as each line's spans: each line lexed alone
// when perLine, else all of them as one text, a token that runs over a
// line's end split at it.
func lexLines(lang string, texts []string, perLine bool) [][]span {
	out := make([][]span, len(texts))
	if perLine {
		for i, t := range texts {
			for _, tk := range syntax.Lex(lang, t) {
				out[i] = append(out[i], span{a: tk.Start, b: tk.End, fg: -1, syn: tk.Kind})
			}
		}
		return out
	}
	toks := syntax.Lex(lang, strings.Join(texts, "\n"))
	line, start := 0, 0 // the line a token is on, and the rune its line starts at
	end := utf8.RuneCountInString(texts[0])
	for _, tk := range toks {
		for tk.Start < tk.End && line < len(texts) {
			if tk.Start > end { // past this line and its newline
				line++
				if line == len(texts) {
					break
				}
				start = end + 1
				end = start + utf8.RuneCountInString(texts[line])
				continue
			}
			e := min(tk.End, end)
			if e > tk.Start {
				out[line] = append(out[line], span{a: tk.Start - start, b: e - start, fg: -1, syn: tk.Kind})
			}
			if tk.End <= end {
				break
			}
			tk.Start = end + 1
		}
	}
	return out
}
