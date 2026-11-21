package gitstat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// turnsIn runs n turns in tab w of dir: a checkpoint, the turn's edit,
// then the turn end. It returns the checkpoints.
func turnsIn(t *testing.T, dir string, edits ...func()) []string {
	t.Helper()
	ctx := context.Background()
	var revs []string
	for _, edit := range edits {
		rev, err := Checkpoint(ctx, dir, "w")
		if err != nil {
			t.Fatal(err)
		}
		revs = append(revs, rev)
		edit()
		if _, err := EndTurn(ctx, dir, "w"); err != nil {
			t.Fatal(err)
		}
	}
	return revs
}

// TestPlanRewind: two turns edit, add and rename files; the plan to go
// back to turn 1 lists each file with what the rewind does to it, nothing
// unsaved, and changes no file or ref.
func TestPlanRewind(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	writeFile(t, dir, "old.txt", "a long enough line to be found as a rename\n")
	commit(t, dir)
	revs := turnsIn(t, dir,
		func() { writeFile(t, dir, "file.txt", "edited\n") },
		func() {
			writeFile(t, dir, "new.txt", "made by turn 2\n")
			if err := os.Rename(filepath.Join(dir, "old.txt"), filepath.Join(dir, "moved.txt")); err != nil {
				t.Fatal(err)
			}
		})
	refs := runGit(t, dir, "for-each-ref")
	p, err := PlanRewind(ctx, dir, "w", revs[0])
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{{Path: "file.txt", Status: "modified"}, {Path: "old.txt", From: "moved.txt", Status: "renamed"}, {Path: "new.txt", Status: "deleted"}}
	if !sameChanges(p.Files, want) || len(p.Unsaved) != 0 {
		t.Errorf("plan = %+v, want files %+v and nothing unsaved", p, want)
	}
	if got := runGit(t, dir, "for-each-ref"); got != refs || readFile(t, dir, "file.txt") != "edited\n" {
		t.Error("PlanRewind changed a ref or a file")
	}
	if p, err := PlanRewind(ctx, dir, "w", revs[1]); err != nil || len(p.Files) != 2 {
		t.Errorf("plan back to turn 2: %+v, %v; want new.txt and the rename", p, err)
	}
}

func sameChanges(got, want []Change) bool {
	key := func(cs []Change) map[string]Change {
		m := map[string]Change{}
		for _, c := range cs {
			m[c.Path] = c
		}
		return m
	}
	return len(got) == len(want) && reflect.DeepEqual(key(got), key(want))
}

// TestRewindRefuses: each refusal leaves every file and ref as it was.
func TestRewindRefuses(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, dir string, revs []string) string // returns the rev to rewind to
		want  func(error) bool
	}{
		{"no checkpoint", func(*testing.T, string, []string) string { return "" },
			func(err error) bool { return errors.Is(err, ErrNoCheckpoint) }},
		{"checkpoint gone", func(t *testing.T, dir string, _ []string) string {
			return strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
		}, func(err error) bool { return err != nil && strings.Contains(err.Error(), "checkpoint is gone") }},
		{"HEAD moved", func(t *testing.T, dir string, revs []string) string {
			commit(t, dir)
			return revs[0]
		}, func(err error) bool { return errors.Is(err, ErrHeadMoved) }},
		{"unsaved edit after the last turn", func(t *testing.T, dir string, revs []string) string {
			writeFile(t, dir, "mine.txt", "the user's own\n")
			return revs[0]
		}, func(err error) bool {
			var u *UnsavedError
			return errors.As(err, &u) && len(u.Files) == 1 && u.Files[0].Path == "mine.txt" &&
				strings.Contains(err.Error(), "mine.txt")
		}},
		{"no turn end: the last turn counts as unsaved", func(t *testing.T, dir string, revs []string) string {
			runGit(t, dir, "update-ref", "-d", "refs/pitwall/turns/w/end")
			return revs[0]
		}, func(err error) bool {
			var u *UnsavedError
			return errors.As(err, &u) && len(u.Files) == 1 && u.Files[0].Path == "two.txt"
		}},
		{"a folder in the way", func(t *testing.T, dir string, revs []string) string {
			// file.txt was a file at turn 1; an ignored file now sits under a folder of that name.
			if err := os.Remove(filepath.Join(dir, "file.txt")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, dir, ".git/info/exclude", "file.txt/\n")
			if err := os.Mkdir(filepath.Join(dir, "file.txt"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, dir, "file.txt/ignored", "no snapshot has me\n")
			runGit(t, dir, "update-ref", "-d", "refs/pitwall/turns/w/end")
			return revs[0]
		}, func(err error) bool { return err != nil && strings.Contains(err.Error(), "in the way") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := repo(t)
			revs := turnsIn(t, dir,
				func() { writeFile(t, dir, "one.txt", "turn 1\n") },
				func() { writeFile(t, dir, "two.txt", "turn 2\n") })
			rev := tc.setup(t, dir, revs)
			refs, tree := runGit(t, dir, "for-each-ref"), snap(t, dir)
			// PlanRewind refuses alike, short of unsaved files, which it lists.
			p, perr := PlanRewind(ctx, dir, "w", rev)
			if perr == nil && tc.want(&UnsavedError{Files: p.Unsaved}) {
				perr = &UnsavedError{Files: p.Unsaved}
			}
			if !tc.want(perr) {
				t.Errorf("PlanRewind = %+v, %v", p, perr)
			}
			b, err := RewindTurn(ctx, dir, "w", rev, false)
			if !tc.want(err) || b != "" {
				t.Errorf("RewindTurn = %q, %v", b, err)
			}
			if runGit(t, dir, "for-each-ref") != refs || snap(t, dir) != tree {
				t.Error("a refused rewind changed a ref or a file")
			}
		})
	}
}

func snap(t *testing.T, dir string) string {
	t.Helper()
	tree, err := snapshot(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// TestRewindStash: the user's own edit after the last turn is refused,
// then stashed: the rewind puts the files back, and the backup it
// returns, also under BackupRef, holds the edit and the turns' files, so
// git restore from it gets them back.
func TestRewindStash(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	revs := turnsIn(t, dir,
		func() { writeFile(t, dir, "file.txt", "turn 1\n") },
		func() { writeFile(t, dir, "two.txt", "turn 2\n") })
	writeFile(t, dir, "mine.txt", "the user's own\n")
	p, err := PlanRewind(ctx, dir, "w", revs[0])
	if err != nil || len(p.Unsaved) != 1 || len(p.Files) != 3 {
		t.Fatalf("plan = %+v, %v; want 3 files, mine.txt unsaved", p, err)
	}
	backup, err := RewindTurn(ctx, dir, "w", revs[0], true)
	if err != nil || backup == "" {
		t.Fatalf("stash and rewind: %q, %v", backup, err)
	}
	for _, f := range []string{"two.txt", "mine.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s survived the rewind", f)
		}
	}
	if got := readFile(t, dir, "file.txt"); got != "one\ntwo\nthree\n" {
		t.Errorf("file.txt = %q, want turn 1's start", got)
	}
	if refs := runGit(t, dir, "for-each-ref", "--format=%(objectname)", BackupRef+"w/"); strings.TrimSpace(refs) != backup {
		t.Errorf("backup refs %q, want %s", refs, backup)
	}
	if runGit(t, dir, "for-each-ref", "refs/pitwall/turns/w/end") != "" {
		t.Error("the rewind kept the turn end of a turn it dropped")
	}
	runGit(t, dir, "restore", "--source="+backup, "--worktree", "--", ".")
	if readFile(t, dir, "mine.txt") != "the user's own\n" || readFile(t, dir, "two.txt") != "turn 2\n" || readFile(t, dir, "file.txt") != "turn 1\n" {
		t.Error("git restore from the backup did not bring the files back")
	}
	// The backup survives the tab's checkpoints going.
	if err := DropTurns(ctx, dir, "w"); err != nil {
		t.Fatal(err)
	}
	if runGit(t, dir, "for-each-ref", "refs/pitwall/turns/") != "" || runGit(t, dir, "for-each-ref", BackupRef) == "" {
		t.Error("DropTurns left a turn ref or dropped the backup")
	}
}

// TestEndTurnDropped: the next checkpoint drops the turn end, so a turn
// that is running counts nothing after its own checkpoint as covered.
func TestEndTurnDropped(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	turnsIn(t, dir, func() { writeFile(t, dir, "one.txt", "1\n") })
	if runGit(t, dir, "for-each-ref", "refs/pitwall/turns/w/end") == "" {
		t.Fatal("no turn end")
	}
	if _, err := Checkpoint(ctx, dir, "w"); err != nil {
		t.Fatal(err)
	}
	if runGit(t, dir, "for-each-ref", "refs/pitwall/turns/w/end") != "" {
		t.Error("the next checkpoint kept the turn end")
	}
	if _, err := EndTurn(ctx, filepath.Join(dir, "nope"), "w"); err == nil {
		t.Error("EndTurn outside a repo")
	}
	if _, err := EndTurn(ctx, dir, "bad/tab"); err == nil {
		t.Error("EndTurn with a tab id that cannot name a ref")
	}
}

// TestCheckpointNear finds the checkpoint taken nearest a prompt's time,
// within a few seconds, and none for a time far from every checkpoint.
func TestCheckpointNear(t *testing.T) {
	dir := repo(t)
	ctx := context.Background()
	revs := turnsIn(t, dir, func() { writeFile(t, dir, "one.txt", "1\n") })
	now := time.Now()
	if got, err := CheckpointNear(ctx, dir, "w", now); err != nil || got != revs[0] {
		t.Errorf("CheckpointNear(now) = %q, %v; want %s", got, err, revs[0])
	}
	if got, err := CheckpointNear(ctx, dir, "w", now.Add(-time.Hour)); err != nil || got != "" {
		t.Errorf("CheckpointNear(an hour ago) = %q, %v; want none", got, err)
	}
	if got, _ := CheckpointNear(ctx, dir, "other", now); got != "" {
		t.Errorf("another tab's checkpoint: %q", got)
	}
}
