package model

import "testing"

func TestShortPath(t *testing.T) {
	for in, want := range map[string]string{
		"/home/me": "~", "/home/me/Work/x": "~/Work/x", "/home/meow": "/home/meow", "/tmp": "/tmp", "": "",
	} {
		if got := shortPath(in, "/home/me"); got != want {
			t.Errorf("shortPath(%q) = %q, want %q", in, got, want)
		}
	}
}
