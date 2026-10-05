// Package gitstat creates and removes worktrees and reports branch stats,
// all by shelling out to git.
package gitstat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

type Worktree struct {
	Path   string
	Branch string
	Head   string
}

// RepoRoot returns the top-level of the repo containing path.
func RepoRoot(ctx context.Context, path string) (string, bool) {
	out, err := git(ctx, path, "rev-parse", "--show-toplevel")
	return strings.TrimSuffix(out, "\n"), err == nil
}

func Stats(ctx context.Context, worktree string) (model.BranchStats, error) {
	var stats model.BranchStats
	if _, err := git(ctx, worktree, "rev-parse", "--git-dir"); err != nil {
		return stats, err
	}
	base, err := defaultRef(ctx, worktree)
	if err != nil || base == "" {
		return stats, err
	}
	// Count committed changes from the merge base and staged/unstaged changes from HEAD.
	for _, revision := range []string{base + "...HEAD", "HEAD"} {
		out, err := git(ctx, worktree, "diff", "--numstat", revision, "--")
		if err != nil {
			return stats, err
		}
		for _, line := range strings.Split(out, "\n") {
			fields := strings.Split(line, "\t")
			// Binary files have no line counts.
			if len(fields) < 3 || fields[0] == "-" {
				continue
			}
			added, err := strconv.Atoi(fields[0])
			if err != nil {
				return stats, err
			}
			deleted, err := strconv.Atoi(fields[1])
			if err != nil {
				return stats, err
			}
			stats.Additions += added
			stats.Deletions += deleted
		}
	}
	out, err := git(ctx, worktree, "rev-list", "--left-right", "--count", base+"...HEAD", "--")
	if err != nil {
		return stats, err
	}
	if _, err := fmt.Sscan(out, &stats.Behind, &stats.Ahead); err != nil {
		return stats, err
	}
	if stats.Ahead == 0 && stats.Behind == 0 {
		stats.MergeStatus = model.MergeUpToDate
		return stats, nil
	}
	_, err = git(ctx, worktree, "merge-tree", "--write-tree", base, "HEAD")
	switch {
	case err == nil:
		stats.MergeStatus = model.MergeClean
	case exitCode(err) == 1 && ctx.Err() == nil:
		stats.MergeStatus = model.MergeConflicts
	default:
		return stats, err
	}
	return stats, nil
}

func ListWorktrees(ctx context.Context, repoRoot string) ([]Worktree, error) {
	out, err := git(ctx, repoRoot, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var trees []Worktree
	var tree Worktree
	for _, field := range strings.Split(out, "\x00") {
		switch {
		case strings.HasPrefix(field, "worktree "):
			tree.Path = strings.TrimPrefix(field, "worktree ")
		case strings.HasPrefix(field, "HEAD "):
			tree.Head = strings.TrimPrefix(field, "HEAD ")
		case strings.HasPrefix(field, "branch "):
			tree.Branch = strings.TrimPrefix(strings.TrimPrefix(field, "branch "), "refs/heads/")
		case field == "" && tree.Path != "":
			trees = append(trees, tree)
			tree = Worktree{}
		}
	}
	return trees, nil
}

// AddWorktree creates branch name off the default branch in a new worktree.
func AddWorktree(ctx context.Context, repoRoot, name string) (path, branch string, err error) {
	branch = strings.Trim(workspaceSegment.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-"), "-")
	if branch == "" {
		return "", "", fmt.Errorf("workspace name must contain a letter or digit")
	}
	base, err := defaultRef(ctx, repoRoot)
	if err != nil {
		return "", "", err
	}
	if base == "" {
		return "", "", fmt.Errorf("repository has no default branch")
	}
	path, err = filepath.Abs(filepath.Join(repoRoot, ".worktrees", branch))
	if err != nil {
		return "", "", err
	}
	_, err = git(ctx, repoRoot, "worktree", "add", "-b", branch, path, base)
	return path, branch, err
}

// ErrBranchKept wraps the error of a RemoveWorktree that removed the worktree
// but could not delete its branch, such as an unmerged one.
var ErrBranchKept = errors.New("worktree removed, branch kept")

func RemoveWorktree(ctx context.Context, repoRoot, path string, deleteBranch bool) error {
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoRoot, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	var branch string
	if deleteBranch {
		trees, err := ListWorktrees(ctx, repoRoot)
		if err != nil {
			return err
		}
		for _, tree := range trees {
			if filepath.Clean(tree.Path) == path {
				branch = tree.Branch
				break
			}
		}
	}
	if _, err := git(ctx, repoRoot, "worktree", "remove", "--", path); err != nil {
		return err
	}
	if branch != "" {
		if _, err := git(ctx, repoRoot, "branch", "-d", "--", branch); err != nil {
			return fmt.Errorf("%w: %w", ErrBranchKept, err)
		}
	}
	return nil
}

var workspaceSegment = regexp.MustCompile(`[^a-z0-9]+`)

// defaultRef prefers origin's default branch, then main/master, then the current branch.
// Comparison uses the remote ref when available, as aide does.
func defaultRef(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil && (exitCode(err) != 1 || ctx.Err() != nil) {
		return "", err
	}
	names := []string{strings.TrimPrefix(strings.TrimSpace(out), "origin/"), "main", "master"}
	for _, name := range names {
		if name == "" {
			continue
		}
		for _, ref := range []string{"refs/remotes/origin/" + name, "refs/heads/" + name} {
			if _, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", ref); err == nil {
				return ref, nil
			} else if exitCode(err) != 1 || ctx.Err() != nil {
				return "", err
			}
		}
	}
	out, err = git(ctx, dir, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		if exitCode(err) == 1 && ctx.Err() == nil {
			return "", nil
		}
		return "", err
	}
	ref := strings.TrimSpace(out)
	if _, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", ref); err != nil {
		if exitCode(err) == 1 && ctx.Err() == nil {
			return "", nil
		}
		return "", err
	}
	return ref, nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return string(out), ctx.Err()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return string(out), fmt.Errorf("git %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(exit.Stderr)), err)
		}
	}
	return string(out), err
}

func exitCode(err error) int {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// FileStat is one file's change from the default branch's merge base,
// counting commits and uncommitted work, like Stats.
type FileStat struct {
	Path     string // relative to the repo root
	Add, Del int    // both 0 for a binary file
	Status   byte   // 'A' added, 'M' modified, 'D' deleted, 'R' renamed, '?' untracked
}

// Files returns the per-file changes behind Stats' totals, and the base
// ref they are against ("main"). Untracked files come last, Status '?',
// with Add their line count; the other files' totals match Stats'.
func Files(ctx context.Context, worktree string) (base string, files []FileStat, err error) {
	ref, err := defaultRef(ctx, worktree)
	if err != nil || ref == "" {
		return "", nil, err
	}
	base = strings.TrimPrefix(strings.TrimPrefix(ref, "refs/remotes/origin/"), "refs/heads/")
	root, ok := RepoRoot(ctx, worktree)
	if !ok {
		return "", nil, fmt.Errorf("%s is not in a git repository", worktree)
	}
	byPath := map[string]*FileStat{}
	var order []string
	stat := func(path string) *FileStat {
		f := byPath[path]
		if f == nil {
			f = &FileStat{Path: path, Status: 'M'}
			byPath[path] = f
			order = append(order, path)
		}
		return f
	}
	// The same two diffs as Stats, so the totals match.
	for _, revision := range []string{ref + "...HEAD", "HEAD"} {
		out, err := git(ctx, root, "diff", "--numstat", "-z", revision, "--")
		if err != nil {
			return "", nil, err
		}
		fields := strings.Split(out, "\x00")
		for i := 0; i < len(fields); i++ {
			counts := strings.Split(fields[i], "\t")
			if len(counts) < 3 {
				continue
			}
			path := counts[2]
			if path == "" && i+2 < len(fields) { // a rename: old and new path follow
				path = fields[i+2]
				i += 2
			}
			f := stat(path)
			if counts[0] == "-" {
				continue
			}
			added, err := strconv.Atoi(counts[0])
			if err != nil {
				return "", nil, err
			}
			deleted, err := strconv.Atoi(counts[1])
			if err != nil {
				return "", nil, err
			}
			f.Add += added
			f.Del += deleted
		}
	}
	// Status is the change from the merge base to the work tree as a whole.
	mb, err := git(ctx, root, "merge-base", ref, "HEAD")
	if err != nil {
		return "", nil, err
	}
	out, err := git(ctx, root, "diff", "--name-status", "-z", strings.TrimSpace(mb), "--")
	if err != nil {
		return "", nil, err
	}
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		status := fields[i]
		if status == "" {
			break
		}
		path := fields[i+1]
		if (status[0] == 'R' || status[0] == 'C') && i+2 < len(fields) {
			i++
			path = fields[i+1]
		}
		if f := byPath[path]; f != nil {
			f.Status = status[0]
			if f.Status == 'C' || f.Status == 'T' {
				f.Status = 'M'
			}
		}
	}
	slices.Sort(order)
	for _, path := range order {
		files = append(files, *byPath[path])
	}
	out, err = git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", nil, err
	}
	for _, path := range strings.Split(out, "\x00") {
		if path != "" {
			files = append(files, FileStat{Path: path, Add: countLines(filepath.Join(root, path)), Status: '?'})
		}
	}
	return base, files, nil
}

// countLines is the line count of a text file, 0 for a binary or unreadable one.
func countLines(path string) int {
	// ponytail: reads the whole file; an untracked file large enough to matter is rare.
	b, err := os.ReadFile(path)
	if err != nil || len(b) == 0 || bytes.IndexByte(b, 0) >= 0 {
		return 0
	}
	n := bytes.Count(b, []byte("\n"))
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}
