package gitstat

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/review"
)

// TestStageHunks splits a file's unstaged diff into hunks and stages the
// second alone, after the first moved the line numbers, then a hunk on a
// last line with no newline, then unstages the first; whole files stage
// and unstage too. Each time git's own diffs say only that part moved.
func TestStageHunks(t *testing.T) {
	dir := repo(t)
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprint("line ", i))
	}
	writeFile(t, dir, "a.txt", strings.Join(lines, "\n")+"\n")
	writeFile(t, dir, "eol.txt", "x\ny")
	commit(t, dir)
	edited := append([]string{}, lines...)
	edited[1] = "TWO\nextra\nmore"
	edited[17] = "EIGHTEEN"
	writeFile(t, dir, "a.txt", strings.Join(edited, "\n")+"\n")
	writeFile(t, dir, "eol.txt", "x\nY")
	writeFile(t, dir, "new.txt", "n\n")

	ctx := context.Background()
	read := func() (unstaged, staged map[string]*review.File) {
		t.Helper()
		s, err := ReadStaging(ctx, dir)
		if err != nil {
			t.Fatal(err)
		}
		unstaged, staged = map[string]*review.File{}, map[string]*review.File{}
		for _, f := range review.Parse(s.Unstaged) {
			unstaged[f.Path] = &f
		}
		for _, f := range review.Parse(s.Staged) {
			staged[f.Path] = &f
		}
		return unstaged, staged
	}
	unstaged, staged := read()
	a := unstaged["a.txt"]
	if a == nil || len(staged) != 0 {
		t.Fatalf("unstaged %v, staged %v", unstaged, staged)
	}
	hunks := review.Hunks(a.Lines, 3)
	if len(hunks) != 2 {
		t.Fatalf("a.txt hunks %v", hunks)
	}
	if h, _ := review.HunkAt(hunks, hunks[0].End); h != hunks[1] {
		t.Errorf("HunkAt after the first hunk = %v", h)
	}
	if err := ApplyCached(ctx, dir, review.Patch(a, hunks[1]), false); err != nil {
		t.Fatal(err)
	}
	cached := runGit(t, dir, "diff", "--cached", "--", "a.txt")
	work := runGit(t, dir, "diff", "--", "a.txt")
	if !strings.Contains(cached, "+EIGHTEEN") || strings.Contains(cached, "+TWO") || !strings.Contains(work, "+TWO") || strings.Contains(work, "EIGHTEEN") {
		t.Fatalf("after staging hunk 2:\ncached\n%s\nwork\n%s", cached, work)
	}

	e := unstaged["eol.txt"]
	if e == nil || len(e.NoEOL) != 2 {
		t.Fatalf("eol.txt %+v", e)
	}
	if err := ApplyCached(ctx, dir, review.Patch(e, review.Hunks(e.Lines, 3)[0]), false); err != nil {
		t.Fatal(err)
	}
	if out := runGit(t, dir, "diff", "--", "eol.txt"); out != "" {
		t.Errorf("eol.txt still unstaged:\n%s", out)
	}

	_, staged = read()
	sa := staged["a.txt"]
	if sa == nil {
		t.Fatalf("staged %v", staged)
	}
	if err := ApplyCached(ctx, dir, review.Patch(sa, review.Hunks(sa.Lines, 3)[0]), true); err != nil {
		t.Fatal(err)
	}
	if out := runGit(t, dir, "diff", "--cached", "--name-only"); out != "eol.txt" {
		t.Errorf("staged after unstaging a.txt's hunk: %q", out)
	}

	if err := StageFiles(ctx, dir, []string{"a.txt", "new.txt"}, false); err != nil {
		t.Fatal(err)
	}
	if out := runGit(t, dir, "diff", "--cached", "--name-only"); out != "a.txt\neol.txt\nnew.txt" {
		t.Errorf("staged after StageFiles: %q", out)
	}
	if err := StageFiles(ctx, dir, []string{"a.txt", "new.txt", "eol.txt"}, true); err != nil {
		t.Fatal(err)
	}
	if out := runGit(t, dir, "diff", "--cached", "--name-only"); out != "" {
		t.Errorf("staged after unstaging every file: %q", out)
	}
	if err := ApplyCached(ctx, dir, []byte("not a patch\n"), false); err == nil {
		t.Error("ApplyCached took a broken patch")
	}
}
