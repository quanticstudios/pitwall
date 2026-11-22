//go:build unix

package app

import (
	"slices"
	"testing"
)

func TestOpenCommandUnix(t *testing.T) {
	if cmd := openCommandForOS("darwin", "https://example.com"); !slices.Equal(cmd.Args, []string{"open", "--", "https://example.com"}) {
		t.Fatalf("darwin args = %q", cmd.Args)
	}
	if cmd := openCommandForOS("linux", "https://example.com"); !slices.Equal(cmd.Args, []string{"xdg-open", "https://example.com"}) {
		t.Fatalf("linux args = %q", cmd.Args)
	}
}
