package gitstat

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
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
	p, err := Diff(ctx, filepath.Join(dir), "")
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

	if err := Discard(ctx, dir, "", []string{"long.txt", "added.txt", "moved.txt", "renamed.txt", "gone.txt", "file.txt"}, false); err != nil {
		t.Fatal(err)
	}
	if err := Discard(ctx, dir, "", []string{"new.txt"}, true); err != nil {
		t.Fatal(err)
	}
	if err := Discard(ctx, dir, "", []string{"../x"}, true); err == nil {
		t.Error("Discard deleted outside the repository")
	}
	p, err = Diff(ctx, dir, "")
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

// TestDiffStackedBase makes a branch stacked on a feature branch with
// AddWorktree's --from: against the default branch its diff has both
// branches' files, against the feature branch only its own. Discard puts a
// file back as the feature branch has it, and CreatedFrom names the
// feature branch.
func TestDiffStackedBase(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	runGit(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "feature.txt", "f\n")
	commit(t, dir)
	runGit(t, dir, "checkout", "-q", "main")
	path, branch, err := AddWorktree(ctx, dir, "stacked", model.WorktreeFrom{Ref: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "stacked.txt", "s\n")
	commit(t, path)

	files := func(base string) []string {
		t.Helper()
		p, err := Diff(ctx, path, base)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range strings.Split(string(p.Diff), "\n") {
			if f, ok := strings.CutPrefix(l, "+++ b/"); ok {
				out = append(out, f)
			}
		}
		return out
	}
	if got := files(""); !slices.Equal(got, []string{"feature.txt", "stacked.txt"}) {
		t.Errorf("against main: %q", got)
	}
	if got := files("feature"); !slices.Equal(got, []string{"stacked.txt"}) {
		t.Errorf("against feature: %q, want only the stacked branch's file", got)
	}
	writeFile(t, path, "feature.txt", "changed\n")
	if err := Discard(ctx, path, "feature", []string{"feature.txt"}, false); err != nil {
		t.Fatal(err)
	}
	if got := files("feature"); !slices.Equal(got, []string{"stacked.txt"}) {
		t.Errorf("against feature after Discard: %q", got)
	}
	if got := CreatedFrom(ctx, path); got != "refs/heads/feature" || branch != "stacked" {
		t.Errorf("CreatedFrom = %q, branch %q", got, branch)
	}
	if got := CreatedFrom(ctx, dir); got != "" {
		t.Errorf("CreatedFrom(main) = %q", got)
	}
}

// TestDiffTagShadowsBranch has a tag named feature on main beside the
// branch feature: a branch stacked on the branch starts from it and
// records it in full, so its diff from that record lists only its own
// file and Discard puts feature.txt back as the branch has it instead of
// deleting it, as the tag's tree would.
func TestDiffTagShadowsBranch(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	runGit(t, dir, "tag", "feature")
	runGit(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "feature.txt", "f\n")
	commit(t, dir)
	runGit(t, dir, "checkout", "-q", "main")
	path, _, err := AddWorktree(ctx, dir, "stacked", model.WorktreeFrom{Ref: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, "feature.txt")); err != nil {
		t.Fatalf("the worktree started from the tag, not the branch: %v", err)
	}
	writeFile(t, path, "stacked.txt", "s\n")
	commit(t, path)
	writeFile(t, path, "feature.txt", "changed\n")
	base := CreatedFrom(ctx, path)
	if base != "refs/heads/feature" {
		t.Fatalf("CreatedFrom = %q", base)
	}
	if err := Discard(ctx, path, base, []string{"feature.txt"}, false); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(path, "feature.txt")); err != nil || string(b) != "f\n" {
		t.Errorf("feature.txt after Discard = %q, %v; want the branch's f", b, err)
	}
	p, err := Diff(ctx, path, base)
	if err != nil {
		t.Fatal(err)
	}
	if d := string(p.Diff); !strings.Contains(d, "+++ b/stacked.txt") || strings.Contains(d, "feature.txt") {
		t.Errorf("diff from %s:\n%s", base, d)
	}
}
