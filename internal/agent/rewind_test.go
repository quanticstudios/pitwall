package agent

import (
	"slices"
	"testing"
)

// The picker and the confirm menu as Claude Code 2.1.329 drew them.
func TestRewindScreens(t *testing.T) {
	picker := gridOf(
		"❯ Reply with just the word CHARLIE.",
		"● CHARLIE",
		"▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔",
		"  Rewind",
		"  Restore the code and/or conversation to the point before…",
		"    Reply with just the word ALPHA.",
		"    No code changes",
		"  ❯ Reply with just the word BRAVO.",
		"    2 files changed +4 -1",
		"    (current)",
		"  Enter to continue · Esc to cancel")
	if l, ok := RewindPick(picker); !ok || l != "Reply with just the word BRAVO." {
		t.Errorf("pick %q %v", l, ok)
	}
	if _, _, ok := RewindConfirm(picker); ok {
		t.Error("the picker read as the confirm menu")
	}
	// The transcript's own prompt rows above the rule are not the picker's.
	if _, ok := RewindPick(gridOf("❯ Reply with just the word CHARLIE.", "● CHARLIE", "────────", "❯ ", "────────")); ok {
		t.Error("the prompt box read as the picker")
	}
	confirm := gridOf(
		"▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔",
		"  Rewind",
		"  Restore to the point before you sent this message:",
		"  │ Reply with just the word CHARLIE.",
		"  │ (21s ago)",
		"  The conversation goes back to before this message.",
		"  ❯ 1. Restore conversation",
		"    2. Summarize from here",
		"    3. Never mind")
	opts, at, ok := RewindConfirm(confirm)
	if !ok || at != 0 || !slices.Equal(opts, []string{"Restore conversation", "Summarize from here", "Never mind"}) {
		t.Errorf("confirm %q at %d %v", opts, at, ok)
	}
	if _, ok := RewindPick(confirm); ok {
		t.Error("the confirm menu read as the picker")
	}
}

func TestRewindMatch(t *testing.T) {
	for _, tc := range []struct {
		label, prompt string
		want          bool
	}{
		{"Reply with just the word ALPHA.", "Reply with just the word ALPHA.", true},
		{"Fix the  bug", "Fix the bug", true},
		{"Fix the bug in parse.go and add…", "Fix the bug in parse.go and add a test\nfor the empty case", true},
		{"first line", "\n\nfirst line\nsecond", true},
		{"Fix the bug", "Fix the bug in parse.go", false}, // not cut: the whole line
		{"(current)", "anything", false},
		{"", "", false},
	} {
		if got := RewindMatch(tc.label, tc.prompt); got != tc.want {
			t.Errorf("RewindMatch(%q, %q) = %v", tc.label, tc.prompt, got)
		}
	}
}
