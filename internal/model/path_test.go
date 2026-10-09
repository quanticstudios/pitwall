package model

import (
	"path/filepath"
	"testing"
)

func TestShortPath(t *testing.T) {
	for in, want := range map[string]string{
		"/home/me": "~", "/home/me/Work/x": "~/Work/x", "/home/meow": "/home/meow", "/tmp": "/tmp", "": "",
	} {
		if got := shortPath(in, "/home/me"); got != want {
			t.Errorf("shortPath(%q) = %q, want %q", in, got, want)
		}
	}
	// A path in this OS's form: C:\Users\me\x on Windows.
	home := filepath.FromSlash("/home/me")
	if got, want := shortPath(filepath.Join(home, "x"), home), "~"+filepath.FromSlash("/x"); got != want {
		t.Errorf("shortPath = %q, want %q", got, want)
	}
}
