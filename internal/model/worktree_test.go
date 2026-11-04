package model

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestWorktreeUsers(t *testing.T) {
	root := t.TempDir()
	s := State{Workspaces: []Workspace{{ID: "owner", Path: root}, {ID: "fork", NameSet: true, Name: "side question", Path: root}, {ID: "nested", Label: "nested", Path: filepath.Join(root, "sub")}, {ID: "outside", Path: root + "-other"}}}
	if got := s.WorktreeUsers(root, "owner"); !slices.Equal(got, []string{"side question", "nested"}) {
		t.Fatalf("users %q", got)
	}
}
