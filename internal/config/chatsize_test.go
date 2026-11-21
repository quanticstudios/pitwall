package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestChatSizeConfig: chat_size follows ui_size until set, and a size out
// of range is reported and follows too.
func TestChatSizeConfig(t *testing.T) {
	for _, c := range []struct {
		text string
		want float64
		prob bool
	}{
		{"[font]\nui_size = 15\n", 15, false},
		{"[font]\nui_size = 15\nchat_size = 18\n", 18, false},
		{"[font]\nchat_size = 99\n", DefaultUISize, true},
		{"", DefaultUISize, false},
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		os.WriteFile(path, []byte(c.text), 0o600)
		s, probs := LoadFile(path)
		bad := slices.ContainsFunc(probs, func(p Problem) bool { return strings.Contains(p.String(), "font.chat_size") })
		if s.Font.ChatSize != c.want || bad != c.prob {
			t.Errorf("%q: chat size %v, problem %v %v", c.text, s.Font.ChatSize, bad, probs)
		}
	}
}
