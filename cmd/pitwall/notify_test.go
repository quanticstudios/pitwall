package main

import "testing"

func TestOscNotify(t *testing.T) {
	for in, want := range map[string]string{
		"tests passed":      "\x1b]9;tests passed\a",
		"a\x07b\x1b]0;x\nc": "\x1b]9;a b ]0;x c\a",
		"✳ done":            "\x1b]9;✳ done\a",
	} {
		if got := oscNotify(in); got != want {
			t.Errorf("oscNotify(%q) = %q, want %q", in, got, want)
		}
	}
}
