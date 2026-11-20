package syntax

import (
	"strings"
	"unicode"
)

// spec is one language for the shared lexer, code.
type spec struct {
	line   []string    // line comment openers; "#" only where a word may start
	block  [][2]string // block comment openers and closers
	quotes string      // the runes that open a string
	multi  string      // the quotes whose strings may span lines
	triple bool        // Python's """ and '''
	kw     map[string]bool
	types  map[string]bool
	lits   map[string]bool // literals, colored as numbers
	fold   bool            // keywords in any case, as SQL's
	caps   bool            // a capitalized name is a type
	dash   bool            // names may hold '-': CSS, shell, YAML
	dollar bool            // JS: '$' is a name's rune
	at     bool            // @name is a decorator
	keys   rune            // a name or string before it is a key: ':' JSON and YAML, '=' TOML
	css    bool            // selectors outside braces, properties inside
	shell  bool            // commands, $variables, a "$ " prompt
	rust   bool            // lifetimes, macros!, #[attributes]
	python bool            // f"", r"", b"" string prefixes
	hashPP bool            // a # that starts a line before a name is a preprocessor line
	noEsc  string          // quotes whose strings take no backslash escapes
}

func set(words string) map[string]bool {
	m := map[string]bool{}
	for w := range strings.FieldsSeq(words) {
		m[w] = true
	}
	return m
}

var specs = map[string]*spec{
	"go": {
		line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: "\"'`", multi: "`", noEsc: "`",
		kw:    set("break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var"),
		types: set("bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint uint8 uint16 uint32 uint64 uintptr any comparable"),
		lits:  set("true false nil iota"), caps: true,
	},
	"js": {
		line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: "\"'`", multi: "`",
		kw:    set("abstract as async await break case catch class const continue debugger declare default delete do else enum export extends finally for from function get if implements import in instanceof interface keyof let namespace new of package private protected public readonly return satisfies set static super switch this throw try type typeof var void while with yield"),
		types: set("string number boolean any unknown never object symbol bigint"),
		lits:  set("true false null undefined NaN Infinity"), caps: true, dollar: true, at: true,
	},
	"python": {
		line: []string{"#"}, quotes: "\"'", triple: true,
		kw:    set("and as assert async await break case class continue def del elif else except finally for from global if import in is lambda match nonlocal not or pass raise return try while with yield self cls"),
		types: set("int float str bool list dict set tuple bytes object type complex frozenset"),
		lits:  set("True False None"), caps: true, at: true, python: true,
	},
	"rust": {
		line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: "\"'", multi: "\"",
		kw:    set("as async await break const continue crate dyn else enum extern fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait type unsafe use where while"),
		types: set("i8 i16 i32 i64 i128 isize u8 u16 u32 u64 u128 usize f32 f64 bool char str"),
		lits:  set("true false None Some Ok Err"), caps: true, rust: true,
	},
	"shell": {
		line: []string{"#"}, quotes: "\"'`", multi: "\"'`", noEsc: "'",
		kw:   set("if then else elif fi for while until do done case esac in function return local export readonly declare typeset unset source alias exit break continue select time"),
		dash: true, shell: true,
	},
	"json": {
		line: []string{"//"}, block: [][2]string{{"/*", "*/"}}, quotes: "\"'",
		lits: set("true false null"), keys: ':',
	},
	"yaml": {
		line: []string{"#"}, quotes: "\"'",
		lits: set("true false null yes no on off True False Null TRUE FALSE NULL"), dash: true, keys: ':',
	},
	"toml": {
		line: []string{"#", ";"}, quotes: "\"'", triple: true,
		lits: set("true false"), dash: true, keys: '=',
	},
	"sql": {
		line: []string{"--"}, block: [][2]string{{"/*", "*/"}}, quotes: "'\"", multi: "'",
		kw:    set("select from where and or not insert into values update set delete create table index view drop alter add column primary key foreign references join left right inner outer full cross on as group by order having limit offset union all distinct case when then else end is like ilike in between exists returning with recursive begin commit rollback transaction default unique check constraint if replace asc desc cascade grant revoke trigger function procedure language returns declare explain analyze using over partition window materialized schema database"),
		types: set("int integer bigint smallint serial bigserial text varchar char character boolean bool date time timestamp timestamptz interval uuid json jsonb numeric decimal real float double precision blob bytea"),
		lits:  set("true false null"), fold: true,
	},
	"css": {
		block: [][2]string{{"/*", "*/"}}, quotes: "\"'",
		kw: set("important"), dash: true, css: true,
	},
	"generic": {
		line: []string{"//", "#"}, block: [][2]string{{"/*", "*/"}}, quotes: "\"'`", multi: "`",
		kw:    set("abstract and as async await break case catch class const continue def default defer delete do elif else elsif end enum except export extends extern final finally fn for foreach from fun func function go if impl implements import in include inline interface is let loop match mod module mut namespace new not object of or override package private protected public raise record require return sealed static struct super switch template then this throw throws trait try type typedef typename union unless until use using val var virtual void volatile when where while with yield"),
		types: set("int long short char float double bool boolean byte string String void unsigned signed size_t uint8_t uint16_t uint32_t uint64_t int8_t int16_t int32_t int64_t auto"),
		lits:  set("true false null nil None NULL nullptr undefined True False"), caps: true, at: true, hashPP: true,
	},
}

// decls are the keywords whose next name is the one they declare.
var funcDecl, typeDecl = set("func fn def function fun sub"), set("class struct enum trait interface type impl union record object")

// code lexes runes [from, to) in s.
func (x *lexer) code(s *spec, from, to int) {
	r := x.r
	console := s.shell && x.prompted(from, to)
	cmd := true           // shell: the next word is a command
	depth := 0            // css: brace depth
	prev, prevAt := "", 0 // the last keyword and its end, for the name it declares
	lineAt := true        // only spaces since the line began
	for i := from; i < to; {
		c := r[i]
		if c == '\n' {
			i++
			cmd, lineAt = true, true
			if console {
				i = x.consoleLine(i, to)
			}
			continue
		}
		if i == from && console {
			if i = x.consoleLine(i, to); i >= to {
				break
			}
			continue
		}
		if c == ' ' || c == '\t' || c == '\r' {
			i++
			continue
		}
		wasLine := lineAt
		lineAt = false
		// comments
		if j, ok := x.comment(s, i, to, wasLine); ok {
			x.emit(i, j, Comment)
			i = j
			continue
		}
		// strings
		if strings.ContainsRune(s.quotes, c) && x.opens(s, i) {
			j := x.str(s, i, to)
			k := String
			if s.keys != 0 && x.next(j, to) == s.keys {
				k = Func
			}
			x.emit(i, j, k)
			if !(s.shell && i > 0 && r[i-1] == '=') { // FOO="x" cmd
				cmd = false
			}
			i = j
			continue
		}
		switch {
		case s.rust && c == '#' && i+1 < to && (r[i+1] == '[' || r[i+1] == '!'):
			j := i
			for j < to && r[j] != ']' && r[j] != '\n' {
				j++
			}
			j = min(j+1, to)
			x.emit(i, j, Tag)
			i = j
			continue
		case s.hashPP && c == '#' && wasLine && i+1 < to && isLetter(r[i+1]):
			j := x.lineEnd(i)
			x.emit(i, min(j, to), Tag)
			i = j
			continue
		case s.css && c == '#' && depth > 0:
			j := x.word(i+1, to, false, false)
			x.emit(i, j, Number)
			i = j
			continue
		case s.css && (c == '#' || c == '.') && depth == 0 && i+1 < to && isLetter(r[i+1]):
			j := x.word(i+1, to, true, false)
			x.emit(i, j, Tag)
			i = j
			continue
		case (s.at || s.css) && c == '@' && i+1 < to && isLetter(r[i+1]):
			j := x.word(i+1, to, s.dash, false)
			k := Tag
			if s.css {
				k = Keyword
			}
			x.emit(i, j, k)
			i = j
			continue
		case s.shell && c == '$':
			j := x.variable(i, to)
			x.emit(i, j, Type)
			if i == 0 || r[i-1] != '=' {
				cmd = false
			}
			i = j
			continue
		case s.rust && c == '\'' && x.lifetime(i, to):
			j := x.word(i+1, to, false, false)
			x.emit(i, j, Type)
			i = j
			continue
		case unicode.IsDigit(c) || c == '.' && i+1 < to && unicode.IsDigit(r[i+1]) && (i == 0 || !isWord(r[i-1])):
			j := x.number(i, to)
			if s.shell && j < to && isWord(r[j]) { // 2to3, a file name
				j = x.shellWord(i, to)
				if cmd {
					x.emit(i, j, Func)
				}
				i, cmd = j, false
				continue
			}
			x.emit(i, j, Number)
			i, cmd = j, false
			continue
		}
		if s.shell && !strings.ContainsRune("|&;()<>{}=!", c) {
			j := x.shellWord(i, to)
			w := string(r[i:j])
			switch {
			case s.kw[w] && cmd:
				x.emit(i, j, Keyword)
				cmd = w != "in" && w != "function" && w != "case" && w != "export" && w != "local" && w != "readonly" && w != "declare" && w != "unset"
			case j < to && r[j] == '=' && ident(r[i:j]):
				x.emit(i, j, Type) // FOO=bar cmd: the value is not a command
				if k := j + 1; k < to && !strings.ContainsRune(" \t\n\"'`$", r[k]) {
					x.emit(j, k, Punct)
					j = x.shellWord(k, to)
				}
			case cmd:
				x.emit(i, j, Func)
				cmd = false
			}
			i = j
			continue
		}
		if isLetter(c) || s.dollar && c == '$' {
			j := x.word(i, to, s.dash, s.dollar)
			w := string(r[i:j])
			if s.python && j < to && (r[j] == '"' || r[j] == '\'') && len(w) <= 2 && strings.Trim(strings.ToLower(w), "frbu") == "" {
				e := x.str(s, j, to)
				x.emit(i, e, String)
				i = e
				continue
			}
			lw := w
			if s.fold {
				lw = strings.ToLower(w)
			}
			nx := x.next(j, to)
			k := Plain
			switch {
			case s.keys != 0 && nx == s.keys && (s.keys != ':' || x.keyColon(j, to)):
				k = Func
			case s.css && depth == 0:
				k = Tag
			case s.css && nx == ':':
				k = Func
			case s.css && nx == '(':
				k = Func
			case s.kw[lw]:
				k = Keyword
			case s.lits[w] || s.fold && s.lits[lw]:
				k = Number
			case s.types[lw]:
				k = Type
			case funcDecl[prev] && blank(r[prevAt:i]):
				k = Func
			case typeDecl[prev] && blank(r[prevAt:i]):
				k = Type
			case nx == '(' || s.rust && nx == '!':
				k = Func
			case s.caps && unicode.IsUpper(r[i]):
				k = Type
			}
			prev = ""
			if k == Keyword {
				prev, prevAt = lw, j
			}
			x.emit(i, j, k)
			i = j
			continue
		}
		if s.css {
			switch c {
			case '{':
				depth++
			case '}':
				depth = max(0, depth-1)
			}
		}
		if s.shell && strings.ContainsRune("|&;(", c) {
			cmd = true
		}
		if strings.ContainsRune("{}[]()<>,.;:=+-*/%!&|^~?\\", c) {
			x.emit(i, i+1, Punct)
		}
		i++
	}
}

// comment is where the comment at i ends, if one starts there. A "#"
// opens one only where a word may start, so a#b and ${#a} are not.
func (x *lexer) comment(s *spec, i, to int, lineStart bool) (int, bool) {
	for _, b := range s.block {
		if x.has(i, b[0]) {
			return x.find(i+len([]rune(b[0])), b[1], to), true
		}
	}
	for _, o := range s.line {
		if !x.has(i, o) {
			continue
		}
		if o == "#" {
			if i > 0 && !strings.ContainsRune(" \t\n;(", x.r[i-1]) {
				continue
			}
			if s.hashPP && lineStart && i+1 < to && isLetter(x.r[i+1]) {
				continue // #include
			}
		}
		return min(x.lineEnd(i), to), true
	}
	return 0, false
}

// opens reports whether the quote at i opens a string: an apostrophe
// inside a word, as in don't, does not.
func (x *lexer) opens(s *spec, i int) bool {
	if x.r[i] != '\'' || i == 0 {
		return true
	}
	if s.rust {
		return !x.lifetime(i, len(x.r))
	}
	return !isWord(x.r[i-1])
}

// str is where the string that opens at i ends: after its closing quote,
// at the line's end for a quote that does not span lines, or at to.
func (x *lexer) str(s *spec, i, to int) int {
	q := x.r[i]
	if s.triple && x.has(i, string([]rune{q, q, q})) {
		return x.find(i+3, string([]rune{q, q, q}), to)
	}
	esc := !strings.ContainsRune(s.noEsc, q)
	multi := strings.ContainsRune(s.multi, q)
	for j := i + 1; j < to; j++ {
		switch c := x.r[j]; {
		case c == '\\' && esc:
			j++
		case c == q:
			return j + 1
		case c == '\n' && !multi:
			return j
		}
	}
	return to
}

// lifetime reports whether the ' at i is a Rust lifetime, 'a, rather
// than a char, 'a' or '\n'.
func (x *lexer) lifetime(i, to int) bool {
	if i+1 >= to || !isLetter(x.r[i+1]) {
		return false
	}
	return !(i+2 < to && x.r[i+2] == '\'')
}

// word is where the name that starts at i ends.
func (x *lexer) word(i, to int, dash, dollar bool) int {
	j := i
	for j < to {
		c := x.r[j]
		if isWord(c) || dollar && c == '$' || dash && c == '-' && j+1 < to && isWord(x.r[j+1]) {
			j++
			continue
		}
		break
	}
	return max(j, i+1)
}

// shellWord is where the shell word at i ends: at a space, a quote, a
// variable or an operator.
func (x *lexer) shellWord(i, to int) int {
	j := i
	for j < to && !strings.ContainsRune(" \t\r\n|&;()<>\"'`$=", x.r[j]) {
		j++
	}
	return max(j, i+1)
}

// variable is where the shell variable at i ends: $NAME, ${...}, $(,
// $1, $?.
func (x *lexer) variable(i, to int) int {
	j := i + 1
	if j >= to {
		return j
	}
	switch c := x.r[j]; {
	case c == '{':
		for j < to && x.r[j] != '}' && x.r[j] != '\n' {
			j++
		}
		return min(j+1, to)
	case c == '(':
		return j // $( then a command
	case isWord(c):
		for j < to && isWord(x.r[j]) {
			j++
		}
		return j
	case strings.ContainsRune("?@*#!$-", c):
		return j + 1
	}
	return j
}

// number is where the number at i ends: digits, a hex or a float with
// its exponent and suffix, not a range's "..".
func (x *lexer) number(i, to int) int {
	j := i
	for j < to {
		c := x.r[j]
		switch {
		case isWord(c):
			j++
		case c == '.' && j+1 < to && unicode.IsDigit(x.r[j+1]):
			j++
		case (c == '+' || c == '-') && j > i && (x.r[j-1] == 'e' || x.r[j-1] == 'E') && !x.has(i, "0x"):
			j++
		default:
			return j
		}
	}
	return j
}

// next is the first rune at or after i on the line that is not a space,
// 0 for none.
func (x *lexer) next(i, to int) rune {
	for ; i < to; i++ {
		switch c := x.r[i]; c {
		case ' ', '\t':
		case '\n':
			return 0
		default:
			return c
		}
	}
	return 0
}

// keyColon reports whether the ':' after the name ending at j is a key's:
// the end of the line or a space follows it, not a URL's "://" or a
// time's "12:30".
func (x *lexer) keyColon(j, to int) bool {
	for j < to && (x.r[j] == ' ' || x.r[j] == '\t') {
		j++
	}
	return j+1 >= to || x.r[j+1] == ' ' || x.r[j+1] == '\n' || x.r[j+1] == '\t'
}

// prompted reports whether [from, to) is a shell session: its first
// non-blank line starts with a "$ " prompt.
func (x *lexer) prompted(from, to int) bool { return Prompted(string(x.r[from:to])) }

// consoleLine handles a session's line at i: it mutes a "$ " prompt and
// returns where its command starts, and leaves lines without one, the
// output, plain, returning the next prompt's command or to.
func (x *lexer) consoleLine(i, to int) int {
	for i < to {
		j := i
		for j < to && (x.r[j] == ' ' || x.r[j] == '\t') {
			j++
		}
		if j < to && x.r[j] == '$' && (j+1 == to || x.r[j+1] == ' ' || x.r[j+1] == '\n') {
			x.emit(j, j+1, Punct)
			return j + 1
		}
		e := x.lineEnd(i)
		if e >= to {
			return to
		}
		i = e + 1
	}
	return to
}

// ident reports whether rs is a shell variable's name.
func ident(rs []rune) bool {
	for i, c := range rs {
		if !isWord(c) || i == 0 && unicode.IsDigit(c) {
			return false
		}
	}
	return len(rs) > 0
}

func isWord(c rune) bool { return c == '_' || unicode.IsLetter(c) || unicode.IsDigit(c) }
