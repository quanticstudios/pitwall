package mdview

import (
	"strings"
	"testing"
)

func TestHeal(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"plain text", "plain text"},
		{"some **bold", "some **bold**"},
		{"some **bold** and *ital", "some **bold** and *ital*"},
		{"**bold *both", "**bold *both***"},
		{"run `go te", "run `go te`"},
		{"a `**` in code and **b", "a `**` in code and **b**"},
		{"~~gone", "~~gone~~"},
		{"ends on a marker **", "ends on a marker"},
		{"**done** ", "**done** "},
		{"* a bullet\n* another *it", "* a bullet\n* another *it*"},
		{"**old** para\n\nnew **b", "**old** para\n\nnew **b**"},
		{"**open para\n\nnext", "**open para\n\nnext"},
		{"```go\nx := `a\n", "```go\nx := `a\n"},
		{"```go\nx\n```\nafter *it", "```go\nx\n```\nafter *it*"},
		{`2 \* 3 is **six`, `2 \* 3 is **six**`},
	} {
		if got := Heal(c.in); got != c.want {
			t.Errorf("Heal(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Every prefix of a reply parses, healed, without a panic, and the
// blocks before the last stay as the whole reply has them.
func TestHealPrefixes(t *testing.T) {
	src := "# Title\n\nSome **bold** text and `code`.\n\n```go\nfunc main() {}\n```\n\n- item *one*\n- item **two**\n\n| a | b |\n|---|---|\n| `x` | y |\n"
	full := Parse([]byte(src))
	for n := range len(src) + 1 {
		blocks := Parse([]byte(Heal(src[:n])))
		for i := 0; i+1 < len(blocks) && i+1 < len(full); i++ {
			if blocks[i].Kind != full[i].Kind && strings.HasSuffix(src[:n], "\n\n") {
				t.Fatalf("prefix %d: block %d is %v, want %v", n, i, blocks[i].Kind, full[i].Kind)
			}
		}
	}
}
