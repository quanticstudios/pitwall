package config

import (
	"slices"
	"strings"
	"testing"
)

func TestOpenPath(t *testing.T) {
	for _, command := range []string{``, `open_path = []`, `open_path = ["bat", "--paging=always"]`} {
		s, probs := LoadFile(write(t, t.TempDir(), "config.toml", "[terminal]\n"+command))
		if len(probs) != 0 {
			t.Fatal(probs)
		}
		want := []string(nil)
		if strings.Contains(command, "bat") {
			want = []string{"bat", "--paging=always"}
		}
		if !slices.Equal(s.OpenPath, want) {
			t.Fatalf("%s: %q, want %q", command, s.OpenPath, want)
		}
	}
	if !strings.Contains(string(Schema()), `"open_path"`) {
		t.Fatal("open_path missing from schema")
	}
	s, probs := LoadFile(write(t, t.TempDir(), "config.toml", "[terminal]\nopen_path = [\"\"]"))
	if len(s.OpenPath) != 0 || len(probs) != 1 || !strings.Contains(probs[0].Msg, "needs a command") {
		t.Fatalf("empty command: %q, %v", s.OpenPath, probs)
	}
}
