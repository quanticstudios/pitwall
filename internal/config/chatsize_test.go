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

// TestChatCodeSizeConfig: chat_code_size stays 0, following chat_size,
// until set; a size out of range is reported and follows too.
func TestChatCodeSizeConfig(t *testing.T) {
	for _, c := range []struct {
		text string
		want float64
		prob bool
	}{
		{"[font]\nchat_size = 15\n", 0, false},
		{"[font]\nchat_code_size = 11\n", 11, false},
		{"[font]\nchat_code_size = 2\n", 0, true},
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		os.WriteFile(path, []byte(c.text), 0o600)
		s, probs := LoadFile(path)
		bad := slices.ContainsFunc(probs, func(p Problem) bool { return strings.Contains(p.String(), "font.chat_code_size") })
		if s.Font.ChatCodeSize != c.want || bad != c.prob {
			t.Errorf("%q: code size %v, problem %v %v", c.text, s.Font.ChatCodeSize, bad, probs)
		}
	}
}

// TestToolCallsConfig: [chat] tool_calls takes all, failed, none and zen,
// defaults to all, and reports anything else, using all.
func TestToolCallsConfig(t *testing.T) {
	for _, c := range []struct {
		text, want string
		prob       bool
	}{
		{"", ToolsAll, false},
		{"[chat]\ntool_calls = \"zen\"\n", ToolsZen, false},
		{"[chat]\ntool_calls = \"failed\"\n", ToolsFailed, false},
		{"[chat]\ntool_calls = \"none\"\n", ToolsNone, false},
		{"[chat]\ntool_calls = \"hideAll\"\n", ToolsAll, true},
	} {
		path := filepath.Join(t.TempDir(), "config.toml")
		os.WriteFile(path, []byte(c.text), 0o600)
		s, probs := LoadFile(path)
		bad := slices.ContainsFunc(probs, func(p Problem) bool { return strings.Contains(p.String(), "chat.tool_calls") })
		if s.Chat.ToolCalls != c.want || bad != c.prob {
			t.Errorf("%q: tool calls %q, problem %v %v", c.text, s.Chat.ToolCalls, bad, probs)
		}
	}
}
