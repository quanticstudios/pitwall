package model

import "testing"

func TestPopupSize(t *testing.T) {
	for _, bad := range []string{"", "0", "-3", "101%", "80 %x", "abc"} {
		if _, _, err := ParsePopupSize(bad); err == nil {
			t.Errorf("ParsePopupSize(%q) took it", bad)
		}
	}
	for _, c := range []struct {
		spec          string
		extent, floor int
		want          int
	}{
		{"80%", 100, 20, 80},
		{"30", 100, 20, 30},
		{"500", 100, 20, 100}, // cut to the area
		{"10%", 100, 20, 20},  // the floor
		{"10%", 12, 20, 12},   // the floor gives way to a small area
		{"", 100, 20, 80},     // the fallback
		{"bad", 50, 5, 40},    // a bad spec takes the fallback too
		{"100%", 0, 20, 0},    // no area
	} {
		if got := ResolvePopupSize(c.spec, "80%", c.extent, c.floor); got != c.want {
			t.Errorf("ResolvePopupSize(%q, %d, %d) = %d, want %d", c.spec, c.extent, c.floor, got, c.want)
		}
	}
}
