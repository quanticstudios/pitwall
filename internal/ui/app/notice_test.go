package app

import "testing"

// TestCopiedText checks the copy notice counts characters on one line and
// lines otherwise.
func TestCopiedText(t *testing.T) {
	for s, want := range map[string]string{
		"x":             "Copied 1 character",
		"héllo wörld":   "Copied 11 characters",
		"one\ntwo\nend": "Copied 3 lines",
		"a\n":           "Copied 2 lines",
	} {
		if got := copiedText(s); got != want {
			t.Errorf("copiedText(%q) = %q, want %q", s, got, want)
		}
	}
}
