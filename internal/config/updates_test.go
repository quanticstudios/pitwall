package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScreenPatternsSetting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"", true},
		{"[updates]\ncheck = false\n", true},
		{"[updates]\nscreen_patterns = false\n", false},
		{"[updates]\nscreen_patterns = true\n", true},
	} {
		if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
			t.Fatal(err)
		}
		s, problems := LoadFile(path)
		if len(problems) != 0 || s.UpdateScreenPatterns != tc.want {
			t.Errorf("%q: UpdateScreenPatterns = %v, problems %v", tc.text, s.UpdateScreenPatterns, problems)
		}
	}
	if !strings.Contains(Default(), "# screen_patterns = true") {
		t.Fatal("default config omits screen_patterns")
	}
	if !strings.Contains(string(Schema()), `"screen_patterns"`) {
		t.Fatal("schema omits screen_patterns")
	}
}
