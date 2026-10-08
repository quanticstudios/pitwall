package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// [worktrees] has port defaults and nothing to copy; a repo's own file
// wins key by key, and a path outside the repo is reported and dropped.
func TestLoadWorktrees(t *testing.T) {
	dir, repo := t.TempDir(), t.TempDir()
	cfg := write(t, dir, "config.toml", "")
	got, probs := LoadWorktrees(cfg, repo)
	if want := (WorktreeSettings{PortBase: 3000, PortStep: 10}); !reflect.DeepEqual(got, want) || probs != nil {
		t.Fatalf("defaults %+v %v, want %+v", got, probs, want)
	}

	write(t, dir, "config.toml", `[worktrees]
port_base = 4000
copy = [".env"]
setup = "make"
`)
	write(t, filepath.Join(repo, ".pitwall"), "worktree.toml", `link = ["node_modules", "../secrets"]
setup = "pnpm install"
`)
	got, probs = LoadWorktrees(cfg, repo)
	want := WorktreeSettings{PortBase: 4000, PortStep: 10, Copy: []string{".env"}, Link: []string{"node_modules"}, Setup: "pnpm install", SetupFromRepo: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("repo over user: %+v, want %+v", got, want)
	}
	if m := msgs(probs); !strings.Contains(m, `.pitwall/worktree.toml:1: link: "../secrets" is not a path inside the repo`) {
		t.Errorf("problems %q", m)
	}

	// The user's own setup stays theirs when the repo's file sets none.
	write(t, filepath.Join(repo, ".pitwall"), "worktree.toml", `link = ["node_modules"]`)
	if got, _ = LoadWorktrees(cfg, repo); got.Setup != "make" || got.SetupFromRepo {
		t.Errorf("user setup: %+v", got)
	}

	s, probs := LoadFile(write(t, dir, "config.toml", "[worktrees]\nport_step = 2.5\nport_base = 80\n"))
	if s.Worktrees.PortBase != 3000 || s.Worktrees.PortStep != 10 || len(probs) != 2 {
		t.Errorf("bad numbers: %+v %v", s.Worktrees, msgs(probs))
	}
}
