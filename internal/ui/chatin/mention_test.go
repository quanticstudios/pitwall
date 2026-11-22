package chatin

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestMention(t *testing.T) {
	for _, c := range []struct {
		text  string
		caret int
		start int
		query string
		ok    bool
	}{
		{"@", 1, 0, "", true},
		{"see @int", 8, 4, "int", true},
		{"see @internal/ui x", 9, 4, "inte", true}, // the query stops at the caret
		{"mail me@example.com", 19, 0, "", false},  // an @ inside a word is no mention
		{"see @x done", 11, 0, "", false},          // the caret is past it
		{"@ ", 2, 0, "", false},
		{"", 0, 0, "", false},
		{"x", 5, 0, "", false},
	} {
		start, query, ok := Mention(c.text, c.caret)
		if ok != c.ok || ok && (start != c.start || query != c.query) {
			t.Errorf("Mention(%q, %d) = %d %q %v, want %d %q %v", c.text, c.caret, start, query, ok, c.start, c.query, c.ok)
		}
	}
	for p, want := range map[string]string{"a.go": "@a.go ", "internal/": "@internal/", "my file.txt": `@"my file.txt" `} {
		if got := MentionText(p); got != want {
			t.Errorf("MentionText(%q) = %q, want %q", p, got, want)
		}
	}
}

// Typing @ opens the popover; Down and Enter pick a file into the draft
// as @path, Tab a folder, which keeps it open inside; Esc closes it and
// Enter then sends; Backspace takes a picked mention whole; a slash
// command's word opens nothing.
func TestMentionPopover(t *testing.T) {
	h := newHarness(t)
	h.paths = []string{"main.go", "internal/", "internal/ui/app.go"}
	h.typ("look at @ma")
	if !h.c.mention.open() || h.c.mention.rows[0] != "main.go" {
		t.Fatalf("rows %q", h.c.mention.rows)
	}
	if m := h.press(key.NameReturn, 0); len(m) != 0 {
		t.Fatalf("Enter on the popover sent %+v", m)
	}
	if h.c.Text() != "look at @main.go " || h.c.mention.open() {
		t.Fatalf("picked %q, open %v", h.c.Text(), h.c.mention.open())
	}
	h.press(key.NameDeleteBackward, 0) // the space
	h.press(key.NameDeleteBackward, 0) // the whole mention
	if h.c.Text() != "look at " {
		t.Fatalf("Backspace left %q", h.c.Text())
	}
	h.typ("@int")
	h.press(key.NameDownArrow, 0)
	if h.c.mention.sel != 1 {
		t.Fatalf("Down: sel %d", h.c.mention.sel)
	}
	h.press(key.NameUpArrow, 0)
	h.press(key.NameTab, 0)
	if h.c.Text() != "look at @internal/" || !h.c.mention.open() {
		t.Fatalf("a folder: %q, open %v", h.c.Text(), h.c.mention.open())
	}
	h.press(key.NameEscape, 0)
	if h.c.mention.open() {
		t.Fatal("Esc left the popover open")
	}
	h.frame()
	if h.c.mention.open() {
		t.Fatal("the popover came back on the text Esc closed it on")
	}
	m := h.press(key.NameReturn, 0)
	if len(m) != 1 || m[0].(proto.ChatSend).Text != "look at @internal/" {
		t.Fatalf("Enter after Esc sent %+v", m)
	}
	h.c.SetText("/@ma")
	h.frame()
	if h.c.mention.open() {
		t.Fatal("a slash command opened the mention popover")
	}
}

// The branch chip copies the branch and shows its check for 1.5 s.
// Ctrl+Backspace and Alt+Backspace right after a picked mention take the
// whole chip, a quoted path with a space included, as Backspace does.
func TestMentionWordDelete(t *testing.T) {
	for _, mods := range []key.Modifiers{key.ModCtrl, key.ModAlt} {
		for _, p := range []string{"main.go", "my file.txt"} {
			h := newHarness(t)
			h.paths = []string{p}
			h.typ("look at @" + p[:2])
			h.press(key.NameReturn, 0)
			if h.press(key.NameDeleteBackward, mods); h.c.Text() != "look at " {
				t.Errorf("%v+Backspace after @%s left %q", mods, p, h.c.Text())
			}
		}
	}
}

func TestBranchChip(t *testing.T) {
	h := newHarness(t)
	h.branch = "v030-composer"
	h.frame()
	h.c.branch.b.Click()
	h.frame()
	if len(h.copied) != 1 || h.copied[0] != "v030-composer" {
		t.Fatalf("copied %q", h.copied)
	}
	if !Copied(h.c.branch.copied, h.now) || Copied(h.c.branch.copied, h.now.Add(copiedFor)) || Copied(time.Time{}, h.now) {
		t.Fatal("the check does not hold for copiedFor")
	}
	h.branch = ""
	h.c.branch.b.Click()
	h.frame()
	if len(h.copied) != 1 {
		t.Fatal("a chip outside a repo copied")
	}
}

func TestMentionSpansAndZeroAllocs(t *testing.T) {
	type span [2]int
	collect := func(txt, tok string) []span {
		var out []span
		mentionSpans(txt, tok, func(s, e int) { out = append(out, span{s, e}) })
		return out
	}
	if got, want := collect("@a.go and 界 @a.go x@a.go @a.gox @a.go", "@a.go"), []span{{0, 5}, {12, 17}, {32, 37}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spans = %v, want %v", got, want)
	}
	if got, want := collect(`see @"my file.txt" ok`, `@"my file.txt"`), []span{{4, 18}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("quoted spans = %v, want %v", got, want)
	}
	long := strings.Repeat("hello world 界 ", 100) + "@main.go " + strings.Repeat("tail ", 100)
	allocs := testing.AllocsPerRun(100, func() {
		count := 0
		mentionSpans(long, "@main.go", func(s, e int) { count += e - s })
		if count != 8 {
			t.Fatalf("count = %d", count)
		}
	})
	if allocs != 0 {
		t.Fatalf("mentionSpans allocated %.1f times per run, want 0", allocs)
	}
}
