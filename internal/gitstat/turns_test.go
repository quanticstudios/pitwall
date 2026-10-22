package gitstat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestCheckpoint snapshots a work tree with staged, unstaged, untracked
// and ignored files and a stash: the checkpoint has all but the ignored
// file, the index, the stash and HEAD stay, and only the latest KeepTurns
// checkpoints are kept.
func TestCheckpoint(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	writeFile(t, dir, ".gitignore", ".worktrees/\n*.log\n")
	commit(t, dir)
	writeFile(t, dir, "file.txt", "stashed\n")
	runGit(t, dir, "stash")
	writeFile(t, dir, "staged.txt", "s\n")
	runGit(t, dir, "add", "staged.txt")
	writeFile(t, dir, "file.txt", "unstaged\n")
	writeFile(t, dir, "new.txt", "u\n")
	writeFile(t, dir, "x.log", "ignored\n")
	state := func() string {
		return runGit(t, dir, "rev-parse", "HEAD", "refs/stash") + "\n" + runGit(t, dir, "ls-files", "--stage") + "\n" + runGit(t, dir, "status", "--porcelain")
	}
	before := state()

	if err := Checkpoint(ctx, dir, "w1"); err != nil {
		t.Fatal(err)
	}
	if after := state(); after != before {
		t.Errorf("checkpoint changed the repository:\n%s\nwas\n%s", after, before)
	}
	ref := "refs/pitwall/turns/w1/1"
	if got := runGit(t, dir, "ls-tree", "-r", "--name-only", ref); got != ".gitignore\nfile.txt\nnew.txt\nstaged.txt" {
		t.Errorf("checkpoint files %q", got)
	}
	if got := runGit(t, dir, "show", ref+":file.txt"); got != "unstaged" {
		t.Errorf("file.txt in the checkpoint: %q", got)
	}
	if got := runGit(t, dir, "rev-parse", ref+"^"); got != runGit(t, dir, "rev-parse", "HEAD") {
		t.Errorf("checkpoint parent %s is not HEAD", got)
	}

	for range KeepTurns + 1 {
		if err := Checkpoint(ctx, dir, "w1"); err != nil {
			t.Fatal(err)
		}
	}
	refs := strings.Fields(runGit(t, dir, "for-each-ref", "--format=%(refname:lstrip=4)", "refs/pitwall/turns/w1/"))
	if len(refs) != KeepTurns || strings.Contains(" "+strings.Join(refs, " ")+" ", " 2 ") {
		t.Errorf("kept %v, want turns 3 to 22", refs)
	}
	if turn, err := LatestTurn(ctx, dir, "w1"); err != nil || turn.N != KeepTurns+2 || turn.Moved {
		t.Errorf("LatestTurn = %+v, %v", turn, err)
	}
	if err := Checkpoint(ctx, dir, "../x"); err == nil {
		t.Error("a tab id with a slash named a ref")
	}
	if err := DropTurns(ctx, dir, "w1"); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, dir, "for-each-ref", "refs/pitwall/"); got != "" {
		t.Errorf("refs after DropTurns: %s", got)
	}
}

// diffPaths is the paths of a patch's files.
func diffPaths(p Patch) []string {
	var out []string
	for _, line := range strings.Split(string(p.Diff), "\n") {
		if rest, ok := strings.CutPrefix(line, "diff --git a/"); ok {
			out = append(out, strings.SplitN(rest, " ", 2)[0])
		}
	}
	return out
}

// TestUndoTurn: a turn modifies, deletes and adds files, one in a new
// folder. The turn's diff lists those alone; undo puts every one back and
// leaves ignored files, the index and HEAD; redo brings the turn back; and
// once a commit moves HEAD, undo refuses.
func TestUndoTurn(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	writeFile(t, dir, ".gitignore", ".worktrees/\n*.log\n")
	writeFile(t, dir, "mod.txt", "old\n")
	writeFile(t, dir, "del.txt", "del\n")
	commit(t, dir)
	writeFile(t, dir, "pre.txt", "untracked before the turn\n")
	if err := Checkpoint(ctx, dir, "w"); err != nil {
		t.Fatal(err)
	}
	head, index := runGit(t, dir, "rev-parse", "HEAD"), runGit(t, dir, "ls-files", "--stage")

	// The turn.
	writeFile(t, dir, "mod.txt", "new\n")
	if err := os.Remove(filepath.Join(dir, "del.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "added.txt", "a\n")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "sub/n.txt", "n\n")
	writeFile(t, dir, "b.log", "ignored\n")

	turn, err := LatestTurn(ctx, dir, "w")
	if err != nil || turn.Rev == "" {
		t.Fatalf("LatestTurn = %+v, %v", turn, err)
	}
	p, err := TurnDiff(ctx, dir, turn.Rev)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := diffPaths(p), []string{"added.txt", "del.txt", "mod.txt", "sub/n.txt"}; !reflect.DeepEqual(got, want) {
		t.Errorf("turn diff files %q, want %q", got, want)
	}

	if err := UndoTurn(ctx, dir, "w"); err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "missing"
		}
		return string(b)
	}
	for name, want := range map[string]string{"mod.txt": "old\n", "del.txt": "del\n", "added.txt": "missing", "sub/n.txt": "missing",
		"pre.txt": "untracked before the turn\n", "b.log": "ignored\n"} {
		if got := read(name); got != want {
			t.Errorf("after undo %s = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "sub")); err == nil {
		t.Error("undo left the turn's empty folder")
	}
	if runGit(t, dir, "rev-parse", "HEAD") != head || runGit(t, dir, "ls-files", "--stage") != index {
		t.Error("undo moved HEAD or changed the index")
	}
	if p, err := TurnDiff(ctx, dir, turn.Rev); err != nil || len(p.Diff) != 0 {
		t.Errorf("turn diff after undo: %q, %v", p.Diff, err)
	}
	if turn, _ := LatestTurn(ctx, dir, "w"); turn.Redo == "" {
		t.Error("undo kept no redo snapshot")
	}

	if err := RedoTurn(ctx, dir, "w"); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"mod.txt": "new\n", "del.txt": "missing", "added.txt": "a\n", "sub/n.txt": "n\n"} {
		if got := read(name); got != want {
			t.Errorf("after redo %s = %q, want %q", name, got, want)
		}
	}
	if turn, _ := LatestTurn(ctx, dir, "w"); turn.Redo != "" {
		t.Error("redo kept its snapshot")
	}

	commit(t, dir) // the agent commits
	if turn, _ := LatestTurn(ctx, dir, "w"); !turn.Moved {
		t.Error("LatestTurn missed the commit")
	}
	if err := UndoTurn(ctx, dir, "w"); !errors.Is(err, ErrHeadMoved) {
		t.Errorf("undo after a commit: %v", err)
	}
	if got := read("added.txt"); got != "a\n" {
		t.Errorf("a refused undo changed added.txt to %q", got)
	}
}

// TestUndoRefuses: undo and redo refuse, changing nothing, where they
// could lose a file no snapshot has or overwrite one: an ignored file in a
// folder the turn put where a file was, a file a .gitignore change shows
// as new, files changed since the undo, and a second undo. A file marked
// assume-unchanged is still snapshotted as it is on disk.
func TestUndoRefuses(t *testing.T) {
	ctx := context.Background()
	read := func(dir, name string) string {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "missing"
		}
		return string(b)
	}
	setup := func() string {
		dir := repo(t)
		writeFile(t, dir, ".gitignore", ".worktrees/\n*.log\n")
		writeFile(t, dir, "f", "a file\n")
		commit(t, dir)
		return dir
	}

	// A folder where a file was, holding an ignored file.
	dir := setup()
	if err := Checkpoint(ctx, dir, "w"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "f")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "f"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "f/new.txt", "n\n")
	writeFile(t, dir, "f/private.log", "secret\n")
	if err := UndoTurn(ctx, dir, "w"); err == nil || !strings.Contains(err.Error(), "f/private.log") {
		t.Errorf("undo over a folder with an ignored file: %v", err)
	}
	if read(dir, "f/private.log") != "secret\n" || read(dir, "f/new.txt") != "n\n" {
		t.Error("a refused undo changed files")
	}

	// A .gitignore change that makes an old ignored file look added.
	dir = setup()
	writeFile(t, dir, "old.log", "kept\n")
	if err := Checkpoint(ctx, dir, "w"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, ".gitignore", ".worktrees/\n")
	if err := UndoTurn(ctx, dir, "w"); err == nil || !strings.Contains(err.Error(), ".gitignore") {
		t.Errorf("undo after a .gitignore change: %v", err)
	}
	if read(dir, "old.log") != "kept\n" {
		t.Error("undo deleted a file the turn did not make")
	}

	// A second undo, then a redo over files changed since the undo.
	dir = setup()
	if err := Checkpoint(ctx, dir, "w"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "f", "the turn\n")
	if err := UndoTurn(ctx, dir, "w"); err != nil {
		t.Fatal(err)
	}
	redo := runGit(t, dir, "rev-parse", "refs/pitwall/turns/w/redo")
	if err := UndoTurn(ctx, dir, "w"); err == nil {
		t.Error("a second undo ran")
	}
	if got := runGit(t, dir, "rev-parse", "refs/pitwall/turns/w/redo"); got != redo {
		t.Error("a second undo replaced the redo snapshot")
	}
	writeFile(t, dir, "f", "edited after the undo\n")
	if err := RedoTurn(ctx, dir, "w"); err == nil {
		t.Error("redo overwrote an edit made after the undo")
	}
	if read(dir, "f") != "edited after the undo\n" {
		t.Error("a refused redo changed f")
	}

	// assume-unchanged.
	dir = setup()
	runGit(t, dir, "update-index", "--assume-unchanged", "f")
	writeFile(t, dir, "f", "changed\n")
	if err := Checkpoint(ctx, dir, "w"); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, dir, "show", "refs/pitwall/turns/w/1:f"); got != "changed" {
		t.Errorf("an assume-unchanged file snapshotted as %q", got)
	}
}
