package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// CodexRollout finds a session's rollout by the id in its name, in any
// day's folder, and nothing for an id that matches no single file.
func TestCodexRollout(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string) string {
		p := filepath.Join(dir, "sessions", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	want := write("2026/11/20/rollout-2026-11-20T18-42-57-01a1fa0d-4e35-7940-a299-46e5827e5015.jsonl")
	write("2026/11/21/rollout-2026-11-21T09-00-00-01a1fee8-dd7a-7da3-b443-d061661d6061.jsonl")
	write("2026/11/21/rollout-2026-11-21T10-00-00-01a1feed-0000-7000-8000-000000000001.jsonl")
	write("2026/11/22/rollout-2026-11-22T10-00-00-01a1feed-0000-7000-8000-000000000001.jsonl")
	for sid, w := range map[string]string{
		"01a1fa0d-4e35-7940-a299-46e5827e5015": want,
		"01a1fee8-0000-7000-8000-000000000000": "", // no file
		"01a1feed-0000-7000-8000-000000000001": "", // two files
		"*":                                    "",
		"../x":                                 "",
		"":                                     "",
	} {
		if got := CodexRollout(dir, sid); got != w {
			t.Errorf("CodexRollout(%q) = %q, want %q", sid, got, w)
		}
	}
}
