package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
)

// The *-md-*.txt fixtures are pitwall's emulator reading Claude Code
// 2.1.332, Codex 0.172 and agy 1.5.18 in a 100x30 PTY, each asked for a
// reply in markdown: prose (headings, bold, italic, code, a link, lists,
// a quote), blocks (a code block and a table), headings (levels 3 and 4,
// a bold line, struck text, a bare link) and the prose one mid-stream.
// Paths and the account are cut from them. testdata/livemd holds the
// markdown each must read back as.
func TestLiveMarkdownFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/*-md-*.txt")
	if err != nil || len(files) != 12 {
		t.Fatalf("fixtures: %v, %v", files, err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".txt")
		p := model.Provider(strings.SplitN(name, "-", 2)[0])
		want, err := os.ReadFile("testdata/livemd/" + name + ".md")
		if err != nil {
			t.Fatal(err)
		}
		lv := LiveScreen(p, screenFixture(t, name+".txt"))
		if got := LiveMarkdown(p, lv.Cells, lv.Width); got != strings.TrimSuffix(string(want), "\n") {
			t.Errorf("%s:\n%s\nwant\n%s", name, got, want)
		}
	}
}

// What each TUI's styles read back as, one by one, with the styles that
// could be more than one thing left as what they show.
func TestLiveMarkdownStyles(t *testing.T) {
	const (
		b, i, u, s, x = "\x1b[1m", "\x1b[3m", "\x1b[4m", "\x1b[9m", "\x1b[0m"
		code          = "\x1b[38;2;177;185;249m" // Claude Code's inline code
		pink          = "\x1b[38;2;245;194;231m"
		kw, str       = "\x1b[38;5;12m", "\x1b[38;5;1m" // Claude Code's syntax colors
		link          = "\x1b]8;;https://example.com/a\x1b\\"
		unlink        = "\x1b]8;;\x1b\\"
	)
	claude, codex, agy := model.ProviderClaude, model.ProviderCodex, model.ProviderAntigravity
	for _, tc := range []struct {
		name string
		p    model.Provider
		rows []string
		want string
	}{
		{"marks", claude, []string{"a " + b + "bold" + x + ", " + i + "it" + x + ", " + b + i + "both" + x + " and " + code + "x := 1" + x + "."},
			"a **bold**, *it*, ***both*** and `x := 1`."},
		{"a bold phrase wrapped", claude, []string{
			"one two three four five six seven eight nine ten eleven twelve " + b + "thirteen",
			"fourteen" + x + " end"},
			"one two three four five six seven eight nine ten eleven twelve **thirteen fourteen** end"},
		{"code with a backtick", claude, []string{"run " + code + "a`b" + x}, "run `` a`b ``"},
		{"struck", codex, []string{"a " + s + "gone" + x + " word"}, "a ~~gone~~ word"},
		{"claude link", claude, []string{"see the docs (" + link + "https://example.com/a" + unlink + ") now"},
			"see the [docs](https://example.com/a) now"},
		{"claude link after a mark", claude, []string{"see " + b + "docs" + x + " (" + link + "https://example.com/a" + unlink + ")"},
			"see **docs** (https://example.com/a)"},
		{"codex link", codex, []string{"see " + link + "docs" + unlink + " (" + link + "https://example.com/a" + unlink + ")."},
			"see [docs](https://example.com/a)."},
		{"agy link", agy, []string{"see " + link + b + "docs" + unlink + x + " " + link + u + "https://example.com/a" + unlink + x + " now"},
			"see [docs](https://example.com/a) now"},
		{"a bare link", codex, []string{link + "https://example.com/a" + unlink}, "https://example.com/a"},
		{"a url broken across rows", claude, []string{
			"one two three four five six seven eight nine ten eleven " + link + "https://example.com/",
			"a" + unlink + " end"},
			"one two three four five six seven eight nine ten eleven https://example.com/a end"},
		// A bold line is a heading's style too, and a line in one color is
		// code's: they stay as they show.
		{"a bold line", claude, []string{b + "Section" + x}, "**Section**"},
		{"a line in one color", claude, []string{code + "make test" + x}, "make test"},
		{"underlined", claude, []string{u + "under" + x}, "under"},
		{"code in one color", codex, []string{pink + "fmt.Println()" + x}, "fmt.Println()"},
		{"claude heading", claude, []string{b + i + u + "Title" + x}, "# Title"},
		{"agy headings", agy, []string{b + " Title" + x, "", b + u + "Sub" + x, "", b + i + "Four" + x},
			"# Title\n\n## Sub\n\n#### Four"},
		{"codex heading", codex, []string{b + pink + "## Sub " + i + "it" + x}, "## Sub it"},
		{"claude quote", claude, []string{"\x1b[2m▎" + x + " " + i + "a quote" + x}, "> a quote"},
		{"agy quote", agy, []string{"│ a quote"}, "> a quote"},
		{"codex quote", codex, []string{pink + "> a quote" + x}, "> a quote"},
		{"lists", claude, []string{"- a", "  - b", "1. c", "   a. d", "   b. e"}, "- a\n  - b\n1. c\n   1. d\n   2. e"},
		{"bullets", codex, []string{"• a", "    • b"}, "- a\n    - b"},
		{"a paragraph after a list", codex, []string{"• a", "", "   indented"}, "- a\n\n   indented"},
		{"indent outside a list", claude, []string{"      Fast mode"}, "Fast mode"},
		{"a rule", claude, []string{"a", "", strings.Repeat("─", 40), "", "b"}, "a\n\n---\n\nb"},
		{"claude code", claude, []string{
			kw + "func" + x + " main() {",
			"    println(" + str + `"hi"` + x + ")",
			"",
			"}"},
			"```\nfunc main() {\n    println(\"hi\")\n```\n\n}"},
		{"claude code over a blank row", claude, []string{kw + "a" + x, "", kw + "b" + x}, "```\na\n\nb\n```"},
		{"agy code", agy, []string{"  " + kw + "x" + str + " := 1" + x, "      " + kw + "y" + str + "()" + x}, "```\nx := 1\n    y()\n```"},
		{"claude table", claude, []string{
			"┌─────┬───────┐",
			"│ " + b + "A" + x + "   │ B     │",
			"├─────┼───────┤",
			"│ " + code + "x" + x + "   │ two   │",
			"│     │ lines │",
			"├─────┼───────┤",
			"│ a|b │ c     │",
			"└─────┴───────┘"},
			"| **A** | B |\n| --- | --- |\n| `x` | two lines |\n| a\\|b | c |"},
		{"agy table", agy, []string{" A   │ B", "─────┼─────", " 1   │ 2", " 3   │ 4"},
			"| A | B |\n| --- | --- |\n| 1 | 2 |\n| 3 | 4 |"},
		{"codex table", codex, []string{b + pink + " A      B" + x, "━━━━━  ━━━━━", " 1      2", "─────  ─────", " 3      4"},
			"| A | B |\n| --- | --- |\n| 1 | 2 |\n| 3 | 4 |"},
		// Mid-stream: a mark the TUI has not closed yet, a link it has not
		// finished and a table with no rows yet stay sane.
		{"half a mark", codex, []string{"so " + b + "bol"}, "so **bol**"},
		{"half a link", codex, []string{"see the [docs]"}, "see the [docs]"},
		{"half a claude link", claude, []string{"see the docs (" + link + "https://exa" + unlink}, "see the docs (https://exa"},
		{"half a table", claude, []string{"┌─────┬─────┐", "│ A   │ B   │"}, "| A | B |\n| --- | --- |"},
	} {
		got := LiveMarkdown(tc.p, cellsOf(tc.rows...), 78)
		if got != tc.want {
			t.Errorf("%s:\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}
}

// A code fence's language, which no TUI draws, folds away with the
// fence, so a reply that opens on a code block still finds its item.
func TestFoldFence(t *testing.T) {
	if a, b := Fold("```go\nx := 1\n```", 60), Fold("```\nx := 1\n```", 60); a != b || a != "x1" {
		t.Errorf("Fold: %q, %q", a, b)
	}
}
