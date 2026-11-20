package panel

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/ui/syntax"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestToolHighlight: a Read preview's lines get their tokens as spans, a
// comment over two lines split at the newline; an edit lexes each line
// alone; a hunk head and ANSI runs keep theirs; a second frame reuses
// the lex.
func TestToolHighlight(t *testing.T) {
	d := &cdraw{t: &Transcript{}, th: theme.Dark()}
	lines := []monoLine{{new: 1, text: "/* a"}, {new: 2, text: "b */ x := \"s\""}, {new: 3, text: "\tret := 1"}}
	d.highlight("r", "go", lines, false)
	if len(lines[0].spans) != 1 || lines[0].spans[0] != (span{a: 0, b: 4, fg: -1, syn: syntax.Comment}) {
		t.Fatalf("line 1 spans %+v", lines[0].spans)
	}
	if s := lines[1].spans; len(s) < 3 || s[0] != (span{a: 0, b: 4, fg: -1, syn: syntax.Comment}) || s[len(s)-1].syn != syntax.String {
		t.Fatalf("line 2 spans %+v", s)
	}
	if s := lines[2].spans; len(s) == 0 || s[len(s)-1] != (span{a: 11, b: 12, fg: -1, syn: syntax.Number}) {
		t.Fatalf("a tab is not counted as the box draws it: %+v", s)
	}
	before := d.t.tool.syn["r"].spans
	again := []monoLine{{new: 1, text: "/* a"}, {new: 2, text: "b */ x := \"s\""}, {new: 3, text: "\tret := 1"}}
	d.highlight("r", "go", again, false)
	if &d.t.tool.syn["r"].spans[0] != &before[0] {
		t.Fatal("the same text lexed again")
	}

	edit := []monoLine{{kind: '@', text: "@@ -1 +1 @@"}, {kind: '-', text: "x := `a"}, {kind: '+', text: "y := 2"}, {kind: '+', text: "z", spans: []span{{a: 0, b: 1, fg: 1}}}}
	d.highlight("e", "go", edit, true)
	if edit[0].spans != nil || len(edit[2].spans) == 0 || edit[2].spans[len(edit[2].spans)-1].syn != syntax.Number || edit[3].spans[0].fg != 1 {
		t.Fatalf("edit spans %+v", edit)
	}
	d.highlight("n", "", lines[:1], false) // no language: nothing
	if _, ok := d.t.tool.syn["n"]; ok {
		t.Fatal("lexed a box without a language")
	}
}

// TestReadLinesTrimmedEnd: Read's empty last line, which comes trimmed
// to its number, is left out, as readRange leaves it out of the count.
func TestReadLinesTrimmedEnd(t *testing.T) {
	ls := readLines("     6\t\tfmt.Println()\n     7\t}\n     8")
	if len(ls) != 2 || ls[1].new != 7 || ls[1].text != "}" {
		t.Fatalf("%+v", ls)
	}
	if ls := readLines("42"); len(ls) != 1 || ls[0].text != "42" {
		t.Fatalf("a lone number is output, kept: %+v", ls)
	}
}
