package syntax

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// kindOf is the kind of the token that covers the n-th (from 0) match
// of word in src whole, Plain when none does.
func kindOf(toks []Token, src, word string, n int) Kind {
	at := -1
	for off := 0; n >= 0; n-- {
		i := strings.Index(src[off:], word)
		if i < 0 {
			return 255
		}
		at = off + i
		off = at + len(word)
	}
	a := utf8.RuneCountInString(src[:at])
	b := a + utf8.RuneCountInString(word)
	for _, t := range toks {
		if t.Start <= a && b <= t.End {
			return t.Kind
		}
	}
	return Plain
}

type want struct {
	word string
	n    int
	kind Kind
}

func check(t *testing.T, lang, src string, wants []want) {
	t.Helper()
	toks := Lex(lang, src)
	end := 0
	for _, tk := range toks {
		if tk.Start < end || tk.End <= tk.Start {
			t.Fatalf("%s: tokens overlap or are empty: %+v", lang, toks)
		}
		end = tk.End
	}
	if end > utf8.RuneCountInString(src) {
		t.Fatalf("%s: a token ends at %d past the source", lang, end)
	}
	for _, w := range wants {
		if got := kindOf(toks, src, w.word, w.n); got != w.kind {
			t.Errorf("%s: %q #%d is %d, want %d", lang, w.word, w.n, got, w.kind)
		}
	}
}

func TestGo(t *testing.T) {
	src := "package main\n\n// Sum adds.\nfunc (s *Set) Sum(xs []int) (string, error) {\n\tx := 0x1f + 2.5e-3\n\treturn fmt.Sprintf(\"%d\", x), nil /* done */\n}\nvar raw = `a\nb`\n"
	check(t, "go", src, []want{
		{"package", 0, Keyword}, {"main", 0, Plain}, {"// Sum adds.", 0, Comment},
		{"s", 1, Plain}, {"Set", 0, Type}, {"Sum", 1, Func}, {"int", 0, Type}, {"error", 0, Type},
		{"0x1f", 0, Number}, {"2.5e-3", 0, Number}, {"Sprintf", 0, Func}, {"\"%d\"", 0, String},
		{"nil", 0, Number}, {"/* done */", 0, Comment}, {"`a\nb`", 0, String}, {"{", 0, Punct},
	})
}

func TestJS(t *testing.T) {
	src := "import { x } from 'y';\n@Component()\nexport async function load($el: HTMLElement): Promise<void> {\n  const s = `t ${x}`; // note\n  return null;\n}\n"
	check(t, "ts", src, []want{
		{"import", 0, Keyword}, {"'y'", 0, String}, {"@Component", 0, Tag}, {"load", 0, Func},
		{"HTMLElement", 0, Type}, {"Promise", 0, Type}, {"`t ${x}`", 0, String}, {"// note", 0, Comment},
		{"null", 0, Number}, {"async", 0, Keyword},
	})
}

func TestPython(t *testing.T) {
	src := "@dataclass\nclass Point(Base):\n    \"\"\"A point.\n    two lines\"\"\"\n    def norm(self) -> float:\n        return f'{self.x}' if True else None  # why\n"
	check(t, "python", src, []want{
		{"@dataclass", 0, Tag}, {"class", 1, Keyword}, {"Point", 0, Type}, {"\"\"\"A point.\n    two lines\"\"\"", 0, String},
		{"norm", 0, Func}, {"float", 0, Type}, {"f'{self.x}'", 0, String}, {"True", 0, Number}, {"# why", 0, Comment},
	})
}

func TestRust(t *testing.T) {
	src := "#[derive(Debug)]\nfn first<'a>(s: &'a str) -> Option<char> {\n    let c = 'x';\n    println!(\"{}\", c);\n    Some(c)\n}\n"
	check(t, "rust", src, []want{
		{"#[derive(Debug)]", 0, Tag}, {"fn", 0, Keyword}, {"first", 0, Func}, {"'a", 0, Type}, {"'a", 1, Type},
		{"str", 0, Type}, {"Option", 0, Type}, {"'x'", 0, String}, {"println", 0, Func}, {"Some", 0, Number},
	})
}

func TestShell(t *testing.T) {
	src := "# build it\nFOO=bar go build -o out ./cmd && echo \"$HOME\" | grep -c x # count\nif [ -f a#b ]; then rm ${DIR}/x; fi\n"
	check(t, "bash", src, []want{
		{"# build it", 0, Comment}, {"FOO", 0, Type}, {"bar", 0, Plain}, {"go", 0, Func}, {"build", 1, Plain},
		{"echo", 0, Func}, {"\"$HOME\"", 0, String}, {"grep", 0, Func}, {"# count", 0, Comment},
		{"if", 0, Keyword}, {"a#b", 0, Plain}, {"then", 0, Keyword}, {"rm", 0, Func}, {"${DIR}", 0, Type}, {"fi", 0, Keyword},
	})
}

func TestConsole(t *testing.T) {
	src := "$ go test ./...\nok  pitwall 0.2s\n$ echo hi\nhi\n"
	check(t, "console", src, []want{
		{"$", 0, Punct}, {"go", 0, Func}, {"ok", 0, Plain}, {"0.2s", 0, Plain}, {"echo", 0, Func}, {"hi", 1, Plain},
	})
	if Lex("", src) == nil {
		t.Error("a block without a language but with a $ prompt is not a shell session")
	}
	if Lex("", "just some text") != nil || Lex("text", "x = 1") != nil {
		t.Error("plain text lexed")
	}
}

func TestDataLangs(t *testing.T) {
	check(t, "json", `{"name": "pitwall", "n": -1.5, "ok": true, "x": null}`, []want{
		{`"name"`, 0, Func}, {`"pitwall"`, 0, String}, {"1.5", 0, Number}, {"true", 0, Number}, {"null", 0, Number},
	})
	check(t, "yaml", "# ci\non:\n  push:\n    branches: [main]\nurl: http://x.io\ndesc: don't 'quote' here\nn: 3\n", []want{
		{"# ci", 0, Comment}, {"push", 0, Func}, {"branches", 0, Func}, {"main", 0, Plain}, {"http", 0, Plain},
		{"don", 0, Plain}, {"'quote'", 0, String}, {"3", 0, Number},
	})
	check(t, "toml", "[tool]\nname = \"x\" # c\n", []want{{"name", 0, Func}, {`"x"`, 0, String}, {"# c", 0, Comment}})
	check(t, "sql", "SELECT id, count(*) FROM users WHERE name = 'a''b' -- c\n", []want{
		{"SELECT", 0, Keyword}, {"count", 0, Func}, {"FROM", 0, Keyword}, {"'a'", 0, String}, {"-- c", 0, Comment},
	})
	check(t, "css", "/* x */\n.btn:hover, #main { color: #fff; margin: 4px !important; }\n@media (max-width: 600px) {}\n", []want{
		{"/* x */", 0, Comment}, {".btn", 0, Tag}, {"#main", 0, Tag}, {"color", 0, Func}, {"#fff", 0, Number},
		{"4px", 0, Number}, {"important", 0, Keyword}, {"@media", 0, Keyword},
	})
}

func TestMarkupLangs(t *testing.T) {
	check(t, "html", "<!-- hi -->\n<a href=\"/x\" data-id='2'>Tom &amp; Jerry</a>\n<script>const x = 1;</script>\n", []want{
		{"<!-- hi -->", 0, Comment}, {"a", 0, Tag}, {"href", 0, Type}, {`"/x"`, 0, String}, {"&amp;", 0, Number},
		{"Tom", 0, Plain}, {"const", 0, Keyword}, {"script", 1, Tag},
	})
	check(t, "md", "<!-- note\nmore -->\n# T", []want{{"<!-- note\nmore -->", 0, Comment}, {"# T", 0, Keyword}})
	check(t, "md", "# Title\n\n- item with `code` and [link](http://x)\n> quoted\n```go\nx := 1\n```\n", []want{
		{"# Title", 0, Keyword}, {"-", 0, Punct}, {"`code`", 0, String}, {"http://x", 0, Func}, {"> quoted", 0, Comment},
		{"```go", 0, Tag}, {"x := 1", 0, String},
	})
	check(t, "diff", "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\n--- removed sql comment\n+new\n same\n", []want{
		{"diff --git a/x b/x", 0, Header}, {"--- a/x", 0, Header}, {"+++ b/x", 0, Header}, {"@@ -1 +1 @@", 0, Hunk},
		{"-old", 0, Removed}, {"--- removed sql comment", 0, Removed}, {"+new", 0, Added}, {" same", 0, Plain},
	})
}

func TestGeneric(t *testing.T) {
	check(t, "c", "#include <stdio.h>\nint main(void) { printf(\"hi\\n\"); return 0; } // end\n", []want{
		{"#include <stdio.h>", 0, Tag}, {"int", 0, Type}, {"main", 0, Func}, {`"hi\n"`, 0, String}, {"return", 0, Keyword}, {"// end", 0, Comment},
	})
}

// TestUnterminated: a string or comment left open ends at its line or
// the source, and nothing after it is lost or out of range.
func TestUnterminated(t *testing.T) {
	check(t, "go", "x := \"open\ny := 2", []want{{"\"open", 0, String}, {"2", 0, Number}})
	check(t, "go", "a /* never closed\nb", []want{{"/* never closed\nb", 0, Comment}})
	check(t, "python", "s = '''still going", []want{{"'''still going", 0, String}})
	check(t, "html", "<a href=\"x", []want{{"\"x", 0, String}})
	check(t, "bash", "echo 'no end", []want{{"'no end", 0, String}})
	for lang := range specs {
		for _, src := range []string{"", "\"", "'", "/*", "$", "#", "`", "\\", "<", "@", "0x", "'a", "$ "} {
			Lex(lang, src) // must not panic
		}
	}
}

// TestHuge: a big block lexes once in bounded time, and only its first
// Limit runes.
func TestHuge(t *testing.T) {
	line := "func f(x int) string { return fmt.Sprint(x) } // c\n"
	src := strings.Repeat(line, 2*Limit/len(line))
	start := time.Now()
	toks := Lex("go", src)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("lexed %d bytes in %v", len(src), d)
	}
	if len(toks) == 0 || toks[len(toks)-1].End > Limit {
		t.Fatalf("%d tokens, last ends at %d", len(toks), toks[len(toks)-1].End)
	}
}

func TestLang(t *testing.T) {
	for in, want := range map[string]string{"Go": "go", "tsx": "js", "zsh": "shell", "yml": "yaml", "c++": "generic", "go {title=x}": "go", "text": "", "": ""} {
		if got := Lang(in); got != want {
			t.Errorf("Lang(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{"a/b.go": "go", "x.test.ts": "js", "Makefile": "shell", "c.yml": "yaml", "notes.txt": "", "README": ""} {
		if got := FromPath(in); got != want {
			t.Errorf("FromPath(%q) = %q, want %q", in, got, want)
		}
	}
}
