package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiffAndDiscard reads the patch of a branch with committed, staged and
// unstaged work, then discards every file: the patch has every unchanged
// line as context and the untracked file by path, and after Discard
// neither a tracked change nor the untracked file is left.
func TestDiffAndDiscard(t *testing.T) {
	dir := repo(t)
	long := strings.Repeat("same\n", 50)
	writeFile(t, dir, "long.txt", "top\n"+long+"bottom\n")
	writeFile(t, dir, "moved.txt", "1\n2\n3\n4\n5\n6\n7\n8\n")
	writeFile(t, dir, "gone.txt", "a\n")
	commit(t, dir)
	runGit(t, dir, "checkout", "-b", "feature")
	writeFile(t, dir, "long.txt", "TOP\n"+long+"bottom\n")
	writeFile(t, dir, "added.txt", "x\n")
	commit(t, dir)
	runGit(t, dir, "mv", "moved.txt", "renamed.txt")
	runGit(t, dir, "rm", "-q", "gone.txt")
	writeFile(t, dir, "file.txt", "one\n")
	writeFile(t, dir, "new.txt", "u\n")

	ctx := context.Background()
	p, err := Diff(ctx, filepath.Join(dir))
	if err != nil {
		t.Fatal(err)
	}
	d := string(p.Diff)
	for _, w := range []string{"+++ b/long.txt", "\n bottom\n", "+++ b/added.txt", "rename from moved.txt", "rename to renamed.txt",
		"--- a/gone.txt", "+++ b/file.txt"} {
		if !strings.Contains(d, w) {
			t.Errorf("no %q in\n%s", w, d)
		}
	}
	if p.Base != "refs/heads/main" || len(p.Untracked) != 1 || p.Untracked[0] != "new.txt" {
		t.Errorf("Base %q, Untracked %q", p.Base, p.Untracked)
	}
	if b, ok := ReadUntracked(p.Root, "new.txt"); !ok || string(b) != "u\n" {
		t.Errorf("ReadUntracked = %q, %v", b, ok)
	}

	if err := Discard(ctx, dir, []string{"long.txt", "added.txt", "moved.txt", "renamed.txt", "gone.txt", "file.txt"}, false); err != nil {
		t.Fatal(err)
	}
	if err := Discard(ctx, dir, []string{"new.txt"}, true); err != nil {
		t.Fatal(err)
	}
	if err := Discard(ctx, dir, []string{"../x"}, true); err == nil {
		t.Error("Discard deleted outside the repository")
	}
	p, err = Diff(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Diff) != 0 || len(p.Untracked) != 0 {
		t.Errorf("after Discard: %q, untracked %q", p.Diff, p.Untracked)
	}
	if _, err := os.Stat(filepath.Join(dir, "moved.txt")); err != nil {
		t.Errorf("the rename's old path is not back: %v", err)
	}
}
