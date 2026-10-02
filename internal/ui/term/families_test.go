package term

import (
	"slices"
	"testing"
)

func TestFamiliesOrder(t *testing.T) {
	got := families("JetBrainsMono Nerd Font, emoji")
	want := []string{"JetBrainsMono Nerd Font", "JetBrainsMono NF", "monospace", "emoji"}
	if !slices.Equal(got, want) {
		t.Fatalf("families = %v, want %v", got, want)
	}
	if a := nerdAlias("Hack Nerd Font Mono"); a != "Hack NFM" {
		t.Fatalf("nerdAlias = %q", a)
	}
	if a := nerdAlias("Iosevka"); a != "Iosevka" {
		t.Fatalf("nerdAlias = %q", a)
	}
}
