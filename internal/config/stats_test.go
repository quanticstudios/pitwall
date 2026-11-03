package config

import (
	"strings"
	"testing"
)

func TestStatsExclude(t *testing.T) {
	p := write(t, t.TempDir(), "config.toml", "[stats]\nexclude = [\"watcher*\", \"bot/*\", \"[\"]\n")
	s, probs := LoadFile(p)
	if len(probs) != 1 || !strings.Contains(probs[0].Msg, "not a glob") {
		t.Fatalf("problems %v", probs)
	}
	if len(s.Stats.Exclude) != 2 {
		t.Fatalf("exclude %v", s.Stats.Exclude)
	}
	for _, tc := range []struct {
		name, branch string
		want         bool
	}{
		{"watcher", "main", true},
		{"feature", "bot/dependencies", true},
		{"feature", "fix/watchers", false},
		{"feature", "bot/nested/branch", false},
		{"", "", false},
	} {
		if got := s.Stats.Excludes(tc.name, tc.branch); got != tc.want {
			t.Errorf("Excludes(%q, %q) = %v", tc.name, tc.branch, got)
		}
	}
	if !strings.Contains(Default(), "[stats]") || !strings.Contains(Default(), "# exclude = []") {
		t.Error("default config lacks stats.exclude")
	}
	if (Stats{}).Excludes("watcher", "bot/dependencies") {
		t.Error("default excludes a tab")
	}
}
