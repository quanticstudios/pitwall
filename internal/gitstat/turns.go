package gitstat

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// A checkpoint is a tab's work tree as a commit object on HEAD, taken when
// its agent starts a turn and kept under refs/pitwall/turns/<tab>/<n>, so
// the review view can show what the turn changed and put it back. No
// branch, the index, the stash or HEAD changes.

// KeepTurns is how many checkpoints a tab keeps.
const KeepTurns = 20

// ErrHeadMoved is UndoTurn's and RedoTurn's refusal when HEAD is no longer
// the commit the snapshot was taken on: putting files back then would undo
// part of a commit.
var ErrHeadMoved = errors.New("HEAD moved since the turn started")

// Turn is a tab's latest checkpoint.
type Turn struct {
	Rev   string // the checkpoint's commit, "" for none
	N     int    // its number
	Redo  string // the snapshot UndoTurn took before it restored, "" for none
	Moved bool   // HEAD is no longer the commit the checkpoint was taken on
}

// turnsRef is the ref folder of tab ws's checkpoints.
func turnsRef(ws string) (string, error) {
	if ws == "" || strings.ContainsFunc(ws, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) {
		return "", fmt.Errorf("tab id %q cannot name a ref", ws)
	}
	return "refs/pitwall/turns/" + ws + "/", nil
}

type turnRef struct {
	name, rev, parent string
	n                 int
}

// turns is the checkpoints under prefix in number order, and the redo
// snapshot.
func turns(ctx context.Context, root, prefix string) (list []turnRef, redo turnRef, err error) {
	out, err := git(ctx, root, "for-each-ref", "--format=%(refname) %(objectname) %(parent)", prefix)
	if err != nil {
		return nil, redo, err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		t := turnRef{name: f[0], rev: f[1], parent: f[2]}
		tail := strings.TrimPrefix(t.name, prefix)
		if tail == "redo" {
			redo = t
			continue
		}
		if n, err := strconv.Atoi(tail); err == nil {
			t.n = n
			list = append(list, t)
		}
	}
	slices.SortFunc(list, func(a, b turnRef) int { return a.n - b.n })
	return list, redo, nil
}

// snapshot writes root's work tree as a tree object, untracked files
// included and ignored ones left out, through an index of its own.
func snapshot(ctx context.Context, root string) (string, error) {
	tmp, err := os.MkdirTemp("", "pitwall-turn-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	index := filepath.Join(tmp, "index")
	env := []string{"GIT_INDEX_FILE=" + index}
	// why: a copy of the repository's index keeps its stat data, so add -A
	// hashes only the files that changed instead of every file in HEAD.
	seeded := false
	if p, err := git(ctx, root, "rev-parse", "--path-format=absolute", "--git-path", "index"); err == nil {
		if b, err := os.ReadFile(strings.TrimSpace(p)); err == nil {
			seeded = os.WriteFile(index, b, 0o600) == nil
		}
	}
	if !seeded {
		if _, err := gitWith(ctx, root, env, "", "read-tree", "HEAD"); err != nil {
			return "", err
		}
	}
	if _, err := gitWith(ctx, root, env, "", "add", "-A"); err != nil {
		return "", err
	}
	out, err := gitWith(ctx, root, env, "", "write-tree")
	return strings.TrimSpace(out), err
}

// commitTree makes an unsigned commit of tree on HEAD, under pitwall's
// name whatever the user's config says.
func commitTree(ctx context.Context, root, tree, msg string) (string, error) {
	env := []string{"GIT_AUTHOR_NAME=pitwall", "GIT_AUTHOR_EMAIL=pitwall@localhost",
		"GIT_COMMITTER_NAME=pitwall", "GIT_COMMITTER_EMAIL=pitwall@localhost"}
	out, err := gitWith(ctx, root, env, "", "commit-tree", "--no-gpg-sign", "-p", "HEAD", "-m", msg, tree)
	return strings.TrimSpace(out), err
}

// Checkpoint snapshots worktree's files as tab ws's next checkpoint, then
// drops the checkpoints past the latest KeepTurns and the redo snapshot,
// which was the turn before's.
func Checkpoint(ctx context.Context, worktree, ws string) error {
	prefix, err := turnsRef(ws)
	if err != nil {
		return err
	}
	root, ok := RepoRoot(ctx, worktree)
	if !ok {
		return fmt.Errorf("%s is not in a git repository", worktree)
	}
	list, redo, err := turns(ctx, root, prefix)
	if err != nil {
		return err
	}
	tree, err := snapshot(ctx, root)
	if err != nil {
		return err
	}
	n := 1
	if len(list) > 0 {
		n = list[len(list)-1].n + 1
	}
	rev, err := commitTree(ctx, root, tree, fmt.Sprintf("pitwall checkpoint: tab %s, turn %d", ws, n))
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "create %s%d %s\n", prefix, n, rev)
	for _, t := range list {
		if t.n <= n-KeepTurns {
			fmt.Fprintf(&b, "delete %s\n", t.name)
		}
	}
	if redo.name != "" {
		fmt.Fprintf(&b, "delete %s\n", redo.name)
	}
	_, err = gitWith(ctx, root, nil, b.String(), "update-ref", "--stdin")
	return err
}

// LatestTurn is tab ws's latest checkpoint in worktree's repository, a
// zero Turn for none.
func LatestTurn(ctx context.Context, worktree, ws string) (Turn, error) {
	prefix, err := turnsRef(ws)
	if err != nil {
		return Turn{}, err
	}
	list, redo, err := turns(ctx, worktree, prefix)
	if err != nil || len(list) == 0 {
		return Turn{}, err
	}
	last := list[len(list)-1]
	head, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return Turn{}, err
	}
	return Turn{Rev: last.rev, N: last.n, Redo: redo.rev, Moved: strings.TrimSpace(head) != last.parent}, nil
}

// TurnDiff is the change from checkpoint rev to worktree's files now, in
// Diff's format. Untracked files are in the diff, as added files, so the
// Patch lists none.
func TurnDiff(ctx context.Context, worktree, rev string) (Patch, error) {
	var p Patch
	root, ok := RepoRoot(ctx, worktree)
	if !ok {
		return p, fmt.Errorf("%s is not in a git repository", worktree)
	}
	tree, err := snapshot(ctx, root)
	if err != nil {
		return p, err
	}
	out, err := git(ctx, root, "-c", "core.quotePath=true", "diff", "--no-color", "--no-ext-diff", "--no-textconv",
		"--src-prefix=a/", "--dst-prefix=b/", "-M", fmt.Sprintf("--unified=%d", fullContext), rev, tree, "--")
	p.Root, p.Diff = root, []byte(out)
	return p, err
}

// UndoTurn puts worktree's files back as they were at tab ws's latest
// checkpoint: files the turn added are removed, ones it changed or deleted
// written back. It snapshots them first as the redo snapshot RedoTurn puts
// back. It refuses with ErrHeadMoved once HEAD is not the checkpoint's
// parent, and never touches HEAD, the index or a commit.
func UndoTurn(ctx context.Context, worktree, ws string) error {
	prefix, err := turnsRef(ws)
	if err != nil {
		return err
	}
	root, ok := RepoRoot(ctx, worktree)
	if !ok {
		return fmt.Errorf("%s is not in a git repository", worktree)
	}
	list, _, err := turns(ctx, root, prefix)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return errors.New("no checkpoint to go back to")
	}
	last := list[len(list)-1]
	if err := onHead(ctx, root, last.parent); err != nil {
		return err
	}
	now, err := snapshot(ctx, root)
	if err != nil {
		return err
	}
	redo, err := commitTree(ctx, root, now, fmt.Sprintf("pitwall checkpoint: tab %s, before undoing turn %d", ws, last.n))
	if err != nil {
		return err
	}
	if _, err := git(ctx, root, "update-ref", prefix+"redo", redo); err != nil {
		return err
	}
	return restore(ctx, root, now, last.rev)
}

// RedoTurn puts back the files UndoTurn replaced, and drops the redo
// snapshot. Like UndoTurn, it refuses with ErrHeadMoved.
func RedoTurn(ctx context.Context, worktree, ws string) error {
	prefix, err := turnsRef(ws)
	if err != nil {
		return err
	}
	root, ok := RepoRoot(ctx, worktree)
	if !ok {
		return fmt.Errorf("%s is not in a git repository", worktree)
	}
	_, redo, err := turns(ctx, root, prefix)
	if err != nil {
		return err
	}
	if redo.name == "" {
		return errors.New("nothing to redo")
	}
	if err := onHead(ctx, root, redo.parent); err != nil {
		return err
	}
	now, err := snapshot(ctx, root)
	if err != nil {
		return err
	}
	if err := restore(ctx, root, now, redo.rev); err != nil {
		return err
	}
	_, err = git(ctx, root, "update-ref", "-d", redo.name)
	return err
}

// DropTurns deletes tab ws's checkpoints from the repository at dir.
func DropTurns(ctx context.Context, dir, ws string) error {
	prefix, err := turnsRef(ws)
	if err != nil {
		return err
	}
	list, redo, err := turns(ctx, dir, prefix)
	if err != nil {
		return err
	}
	var b strings.Builder
	for _, t := range append(list, redo) {
		if t.name != "" {
			fmt.Fprintf(&b, "delete %s\n", t.name)
		}
	}
	if b.Len() == 0 {
		return nil
	}
	_, err = gitWith(ctx, dir, nil, b.String(), "update-ref", "--stdin")
	return err
}

func onHead(ctx context.Context, root, rev string) error {
	head, err := git(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != rev {
		return ErrHeadMoved
	}
	return nil
}

// restore makes root's files those of commit rev, where tree now is a
// snapshot of them: a file rev lacks is removed, with the folders that
// leaves empty, and the others are written from rev by git restore, which
// leaves the index alone. Ignored files stay as they are.
func restore(ctx context.Context, root, now, rev string) error {
	out, err := git(ctx, root, "diff-tree", "-r", "--no-renames", "--name-status", "-z", now, rev)
	if err != nil {
		return err
	}
	f := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var write []string
	for i := 0; i+1 < len(f); i += 2 {
		status, p := f[i], f[i+1]
		if status != "D" {
			write = append(write, p)
			continue
		}
		if err := os.Remove(filepath.Join(root, p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for d := filepath.Dir(p); d != "." && d != "/"; d = filepath.Dir(d) {
			if os.Remove(filepath.Join(root, d)) != nil {
				break // not empty
			}
		}
	}
	if len(write) == 0 {
		return nil
	}
	_, err = gitWith(ctx, root, nil, strings.Join(write, "\x00"),
		"--literal-pathspecs", "restore", "--source="+rev, "--worktree", "--pathspec-from-file=-", "--pathspec-file-nul")
	return err
}
