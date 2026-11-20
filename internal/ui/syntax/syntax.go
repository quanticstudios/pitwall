// Package syntax splits source code into tokens for highlighting: a small
// lexer per common language (Go, JS/TS, Python, Rust, shell, JSON, YAML,
// TOML, SQL, CSS, HTML, Markdown, diff) and a C-like one for the rest. It
// colors nothing itself; the chat maps each Kind to a theme color.
package syntax

import (
	"path/filepath"
	"strings"
	"unicode"
)

// Kind is what a token is, as a highlighter colors it.
type Kind uint8

const (
	Plain   Kind = iota
	Keyword      // if, func, SELECT
	String
	Number  // numbers and literals: true, nil, null
	Func    // function names, JSON and YAML keys, CSS properties, shell commands
	Type    // types, classes, shell variables, HTML attribute names
	Comment // muted and italic
	Tag     // HTML tags, decorators, Rust attributes, CSS selectors
	Punct   // punctuation and operators
	Added   // a diff's lines
	Removed
	Hunk
	Header
)

// Token is runes [Start, End) of the source, of Kind. Lex never returns
// Plain ones.
type Token struct {
	Start, End int
	Kind       Kind
}

// Limit is how many runes Lex reads: the rest of a longer source stays
// plain, so a huge block costs a bounded time once.
const Limit = 100_000

// Lex is src's tokens in lang (a fence's info word or a name Lang
// knows), in order and not overlapping; nil when lang is one it does not
// know or plain text. A block without a language whose first line starts
// with a "$ " prompt lexes as a shell session.
func Lex(lang, src string) []Token {
	l := Lang(lang)
	if l == "" && Prompted(src) {
		l = "shell"
	}
	if l == "" {
		return nil
	}
	r := []rune(src)
	if len(r) > Limit {
		r = r[:Limit]
	}
	x := &lexer{r: r}
	switch l {
	case "diff":
		x.diff()
	case "markdown":
		x.markdown()
	case "html":
		x.html()
	default:
		x.code(specs[l], 0, len(r))
	}
	return x.out
}

// Prompted reports whether src's first non-blank line starts with a
// "$ " shell prompt.
func Prompted(src string) bool {
	for line := range strings.SplitSeq(src, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		return strings.HasPrefix(strings.TrimLeft(line, " \t"), "$ ")
	}
	return false
}

// Lang is the lexer for name, a fence's info word or an alias, any case:
// "go", "js", "python", "rust", "shell", "json", "yaml", "toml", "sql",
// "css", "html", "markdown", "diff" or "generic"; "" for plain text and
// names it does not know.
func Lang(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if i := strings.IndexAny(name, " {,"); i >= 0 { // ```go {title=x}
		name = name[:i]
	}
	return aliases[name]
}

var aliases = map[string]string{}

func init() {
	for lang, names := range map[string]string{
		"go":       "go golang",
		"js":       "js javascript jsx mjs cjs ts typescript tsx node",
		"python":   "py python python3 py3 pyi",
		"rust":     "rs rust",
		"shell":    "sh bash zsh shell console shellsession terminal fish ksh dockerfile docker make makefile",
		"json":     "json jsonc json5 jsonl ndjson",
		"yaml":     "yaml yml",
		"toml":     "toml ini cfg conf properties env dotenv",
		"sql":      "sql psql mysql postgres postgresql pgsql sqlite plsql",
		"css":      "css scss less sass",
		"html":     "html htm xml svg xhtml vue",
		"markdown": "md markdown mdx",
		"diff":     "diff patch udiff",
		"generic":  "c h cpp c++ cc cxx hpp java kotlin kt kts swift cs csharp c# rb ruby php lua dart scala zig groovy gradle proto protobuf objc objective-c elixir ex exs hs haskell ocaml ml fs fsharp clj clojure r perl pl nim v",
	} {
		for n := range strings.FieldsSeq(names) {
			aliases[n] = lang
		}
		aliases[lang] = lang
	}
}

// FromPath is the lexer for a file's name, as Lang names it; "" when its
// extension is not one it knows.
func FromPath(path string) string {
	base := strings.ToLower(filepath.Base(path))
	switch base {
	case "makefile", "gnumakefile", "dockerfile", ".bashrc", ".zshrc", ".profile", ".bash_profile", ".zprofile", ".envrc":
		return "shell"
	case "go.mod", "go.sum", "go.work":
		return ""
	}
	if strings.HasPrefix(base, "dockerfile.") || strings.HasSuffix(base, ".dockerfile") {
		return "shell"
	}
	ext := strings.TrimPrefix(filepath.Ext(base), ".")
	switch ext {
	case "", "txt", "log", "lock":
		return ""
	case "yml", "yaml":
		return "yaml"
	}
	return Lang(ext)
}

// lexer is one Lex: the runes it reads and the tokens so far.
type lexer struct {
	r   []rune
	out []Token
}

// emit adds runes [a, b) as k, joined to the token before when that is
// the same kind and only spaces part them, so a block has few runs.
func (x *lexer) emit(a, b int, k Kind) {
	if a >= b || k == Plain {
		return
	}
	if n := len(x.out); n > 0 && x.out[n-1].Kind == k && x.out[n-1].End <= a && blank(x.r[x.out[n-1].End:a]) {
		x.out[n-1].End = b
		return
	}
	x.out = append(x.out, Token{a, b, k})
}

func blank(rs []rune) bool {
	for _, c := range rs {
		if c != ' ' && c != '\t' {
			return false
		}
	}
	return true
}

// lineEnd is the index of the newline at or after i, or len(r).
func (x *lexer) lineEnd(i int) int {
	for i < len(x.r) && x.r[i] != '\n' {
		i++
	}
	return i
}

// has reports whether the runes at i start with s.
func (x *lexer) has(i int, s string) bool {
	for _, c := range s {
		if i >= len(x.r) || x.r[i] != c {
			return false
		}
		i++
	}
	return true
}

// diff colors a unified diff by line: file heads, hunk heads, added and
// removed lines.
func (x *lexer) diff() {
	hunk := false
	for i := 0; i < len(x.r); {
		e := x.lineEnd(i)
		switch {
		case x.has(i, "@@"):
			hunk = true
			x.emit(i, e, Hunk)
		case x.has(i, "diff ") || x.has(i, "index ") || x.has(i, "Index: "):
			hunk = false
			x.emit(i, e, Header)
		case !hunk && (x.has(i, "+++") || x.has(i, "---")):
			x.emit(i, e, Header)
		case x.has(i, "+"):
			x.emit(i, e, Added)
		case x.has(i, "-"):
			x.emit(i, e, Removed)
		}
		i = e + 1
	}
}

// markdown colors headings, fences, quotes, list markers, inline code,
// link targets and HTML comments.
func (x *lexer) markdown() {
	fence := false
	for i := 0; i < len(x.r); {
		e := x.lineEnd(i)
		s := i
		for s < e && (x.r[s] == ' ' || x.r[s] == '\t') {
			s++
		}
		switch {
		case !fence && x.has(s, "<!--"):
			j := x.find(s+4, "-->", len(x.r))
			x.emit(s, j, Comment)
			i = x.lineEnd(j) + 1 // shortcut: text after the comment on its last line stays plain
			continue
		case x.has(s, "```") || x.has(s, "~~~"):
			fence = !fence
			x.emit(s, e, Tag)
		case fence:
			x.emit(s, e, String)
		case s < e && x.r[s] == '#':
			x.emit(s, e, Keyword)
		case s < e && x.r[s] == '>':
			x.emit(s, e, Comment)
		default:
			if m := listMarker(x.r[s:e]); m > 0 {
				x.emit(s, s+m, Punct)
				s += m
			}
			x.inline(s, e)
		}
		i = e + 1
	}
}

// listMarker is how many runes a list item's marker takes at the start
// of line, "- " or "12. ", 0 for none.
func listMarker(line []rune) int {
	if len(line) >= 2 && strings.ContainsRune("-*+", line[0]) && line[1] == ' ' {
		return 1
	}
	n := 0
	for n < len(line) && unicode.IsDigit(line[n]) {
		n++
	}
	if n > 0 && n+1 < len(line) && (line[n] == '.' || line[n] == ')') && line[n+1] == ' ' {
		return n + 1
	}
	return 0
}

// inline colors a Markdown line's code spans and link targets in [i, e).
func (x *lexer) inline(i, e int) {
	for i < e {
		switch {
		case x.r[i] == '`':
			j := i + 1
			for j < e && x.r[j] != '`' {
				j++
			}
			x.emit(i, min(j+1, e), String)
			i = j + 1
		case x.has(i, "](") && i+2 < e:
			j := i + 2
			for j < e && x.r[j] != ')' {
				j++
			}
			x.emit(i+2, j, Func)
			i = j
		default:
			i++
		}
	}
}

// html colors tags, attributes and their values, comments and entities;
// a script or style element's body lexes as JS or CSS.
func (x *lexer) html() {
	r := x.r
	for i := 0; i < len(r); {
		switch {
		case x.has(i, "<!--"):
			j := x.find(i+4, "-->", len(r))
			x.emit(i, j, Comment)
			i = j
		case r[i] == '<' && i+1 < len(r) && (isLetter(r[i+1]) || r[i+1] == '/' || r[i+1] == '!' || r[i+1] == '?'):
			i = x.tag(i)
		case r[i] == '&':
			j := i + 1
			for j < len(r) && j-i < 10 && (isLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '#') {
				j++
			}
			if j < len(r) && r[j] == ';' {
				x.emit(i, j+1, Number)
				i = j + 1
				continue
			}
			i++
		default:
			i++
		}
	}
}

// tag colors the tag that starts at i and returns where it ends.
func (x *lexer) tag(i int) int {
	r := x.r
	j := i + 1
	if r[j] == '/' || r[j] == '!' || r[j] == '?' {
		j++
	}
	x.emit(i, j, Punct)
	k := j
	for k < len(r) && (isLetter(r[k]) || unicode.IsDigit(r[k]) || strings.ContainsRune("-:._", r[k])) {
		k++
	}
	x.emit(j, k, Tag)
	name := strings.ToLower(string(r[j:k]))
	closing := r[i+1] == '/'
	for k < len(r) {
		c := r[k]
		switch {
		case c == '>':
			x.emit(k, k+1, Punct)
			k++
			if !closing && (name == "script" || name == "style") {
				end := x.findFold(k, "</"+name)
				lang := "js"
				if name == "style" {
					lang = "css"
				}
				x.code(specs[lang], k, end)
				return end
			}
			return k
		case c == '"' || c == '\'':
			e := k + 1
			for e < len(r) && r[e] != c {
				e++
			}
			e = min(e+1, len(r))
			x.emit(k, e, String)
			k = e
		case isLetter(c) || c == '_' || c == ':' || c == '@':
			e := k + 1
			for e < len(r) && (isLetter(r[e]) || unicode.IsDigit(r[e]) || strings.ContainsRune("-:._@", r[e])) {
				e++
			}
			x.emit(k, e, Type)
			k = e
		case c == '=' || c == '/' || c == '?':
			x.emit(k, k+1, Punct)
			k++
		default:
			k++
		}
	}
	return k
}

// find is the index just after the first s at or after i, or end.
func (x *lexer) find(i int, s string, end int) int {
	for ; i < end; i++ {
		if x.has(i, s) {
			return i + len([]rune(s))
		}
	}
	return end
}

// findFold is the index of the first s at or after i in any case, or
// len(r).
func (x *lexer) findFold(i int, s string) int {
	n := len([]rune(s))
	for ; i+n <= len(x.r); i++ {
		if strings.EqualFold(string(x.r[i:i+n]), s) {
			return i
		}
	}
	return len(x.r)
}

func isLetter(c rune) bool { return c == '_' || unicode.IsLetter(c) }
