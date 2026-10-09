package config

import "testing"

func TestGit(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		body    string
		method  string
		archive bool
		problem string
	}{
		{"", "squash", false, ""},
		{`merge_method = "rebase"` + "\narchive_on_merge = true", "rebase", true, ""},
		{`merge_method = "merge"`, "merge", false, ""},
		{`merge_method = "ff"`, "squash", false, `config.toml:2: git.merge_method: "ff" is not squash, merge or rebase; using squash`},
	} {
		s, probs := LoadFile(write(t, dir, "config.toml", "[git]\n"+tc.body+"\n"))
		if s.MergeMethod != tc.method || s.ArchiveOnMerge != tc.archive || msgs(probs) != tc.problem {
			t.Errorf("%q: method %q, archive %v, problems %q", tc.body, s.MergeMethod, s.ArchiveOnMerge, msgs(probs))
		}
	}
}
