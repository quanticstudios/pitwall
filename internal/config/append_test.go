package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendString(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
		bad             bool
	}{
		{"missing", "[font]\nui_size = 14\n", "[font]\nui_size = 14\n\n[stats]\nexclude = [\"watcher\"]\n", false},
		{"default", "[stats]\n# exclude = []\n", "[stats]\nexclude = [\"watcher\"]\n", false},
		{"inline", "[stats]\nexclude = [\"bot/*\"] # keep\n", "[stats]\nexclude = [\"bot/*\",\"watcher\"] # keep\n", false},
		{"trailing comma", "[stats]\nexclude = [\"bot/*\", ]\n", "[stats]\nexclude = [\"bot/*\", \"watcher\"]\n", false},
		{"empty comment", "[stats]\nexclude = [ # keep\n]\n", "[stats]\nexclude = [ # keep\n\"watcher\"]\n", false},
		{"literal and multiline", "[stats]\nexclude = [\n '''a]#b''', # keep\n]\n", "[stats]\nexclude = [\n '''a]#b''', # keep\n\"watcher\"]\n", false},
		{"duplicate", "[stats]\nexclude = [ 'watcher', # keep\n]\n", "[stats]\nexclude = [ 'watcher', # keep\n]\n", false},
		{"wrong type", "[stats]\nexclude = \"watcher\"\n", "[stats]\nexclude = \"watcher\"\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(tc.src), 0600); err != nil {
				t.Fatal(err)
			}
			err := AppendString(path, "stats", "exclude", "watcher")
			if (err != nil) != tc.bad {
				t.Fatalf("error %v, want bad %v", err, tc.bad)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(b) != tc.want {
				t.Fatalf("got %q, want %q", b, tc.want)
			}
		})
	}
}
