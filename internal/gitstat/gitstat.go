// Package gitstat creates and removes worktrees and reports branch stats,
// all by shelling out to git.
package gitstat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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

// Stats counts the lines changed from the merge base with the default
// branch to the work tree, committed, staged and unstaged, untracked files
// left out, and how far the branch is ahead and behind.
func Stats(ctx context.Context, worktree string) (model.BranchStats, error) {
	var stats model.BranchStats
	if _, err := git(ctx, worktree, "rev-parse", "--git-dir"); err != nil {
		return stats, err
	}
	base, err := defaultRef(ctx, worktree)
	if err != nil || base == "" {
		return stats, err
	}
	stats.Base = base
	mb, err := mergeBase(ctx, worktree, base)
	if err != nil {
		return stats, err
	}
	files, err := numstat(ctx, worktree, mb)
	if err != nil {
		return stats, err
	}
	for _, f := range files {
		stats.Additions += f.Add
		stats.Deletions += f.Del
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

// Slug is the folder name AddWorktree gives a worktree called name: name
// lowered to letters, digits and dashes.
func Slug(name string) string {
	return strings.Trim(workspaceSegment.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-"), "-")
}

// AddWorktree makes a worktree in <repoRoot>/.worktrees/<Slug(name)>,
// checked out as from says, and returns its path and branch. It first
// keeps .worktrees out of git status (see excludeWorktrees).
func AddWorktree(ctx context.Context, repoRoot, name string, from model.WorktreeFrom) (path, branch string, err error) {
	slug := Slug(name)
	if slug == "" {
		return "", "", fmt.Errorf("workspace name must contain a letter or digit")
	}
	if strings.HasPrefix(from.Ref, "-") {
		return "", "", fmt.Errorf("no branch %q", from.Ref)
	}
	path, err = filepath.Abs(filepath.Join(repoRoot, ".worktrees", slug))
	if err != nil {
		return "", "", err
	}
	var args []string
	switch from.Kind {
	case model.FromNew:
		base := from.Ref
		if base == "" {
			if base, err = defaultRef(ctx, repoRoot); err != nil {
				return "", "", err
			}
			if base == "" {
				return "", "", fmt.Errorf("repository has no default branch")
			}
		}
		// why: a branch tracking its base has an upstream, so create_pr would skip push -u.
		branch, args = slug, []string{"worktree", "add", "--no-track", "-b", slug, path, base}
	case model.FromBranch:
		if !hasRef(ctx, repoRoot, "refs/heads/"+from.Ref) {
			return "", "", fmt.Errorf("no local branch %q", from.Ref)
		}
		branch, args = from.Ref, []string{"worktree", "add", path, from.Ref}
	case model.FromRemote:
		branch = model.LocalOf(from.Ref)
		if branch == "" || !hasRef(ctx, repoRoot, "refs/remotes/"+from.Ref) {
			return "", "", fmt.Errorf("no remote branch %q", from.Ref)
		}
		args = []string{"worktree", "add", "--track", "-b", branch, path, "refs/remotes/" + from.Ref}
	case model.FromPR:
		if from.PR <= 0 {
			return "", "", fmt.Errorf("no pull request #%d", from.PR)
		}
		if !GitHub(ctx, repoRoot) {
			return "", "", errors.New("origin is not a GitHub repository")
		}
		branch = "pr-" + strconv.Itoa(from.PR)
		// Without a leading +, a pr-N with commits of its own refuses the update.
		if _, err := git(ctx, repoRoot, "fetch", "origin", fmt.Sprintf("pull/%d/head:refs/heads/%s", from.PR, branch)); err != nil {
			return "", "", err
		}
		args = []string{"worktree", "add", path, branch}
	default:
		return "", "", fmt.Errorf("unknown worktree kind %d", from.Kind)
	}
	if err := excludeWorktrees(ctx, repoRoot); err != nil {
		return "", "", err
	}
	_, err = git(ctx, repoRoot, args...)
	return path, branch, err
}

// excludeWorktrees adds /.worktrees/ to the repo's .git/info/exclude
// unless git ignores .worktrees already, so worktrees never show as
// untracked files. .gitignore is the user's and stays as it is.
func excludeWorktrees(ctx context.Context, repoRoot string) error {
	if _, err := git(ctx, repoRoot, "check-ignore", "-q", ".worktrees/"); err == nil {
		return nil
	} else if exitCode(err) != 1 || ctx.Err() != nil {
		return err
	}
	out, err := git(ctx, repoRoot, "rev-parse", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	path := strings.TrimSpace(out)
	if !filepath.IsAbs(path) {
		path = filepath.Join(repoRoot, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	line := "/.worktrees/\n"
	if len(old) > 0 && old[len(old)-1] != '\n' {
		line = "\n" + line
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(line)
	return errors.Join(err, f.Close())
}

// hasRef reports whether ref, such as "refs/heads/main", exists.
func hasRef(ctx context.Context, dir, ref string) bool {
	_, err := git(ctx, dir, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// GitHub reports whether the repo's origin is on GitHub, so a pull request
// can be fetched from it. It reads the configured URL, before any
// url.<base>.insteadOf rewrite.
func GitHub(ctx context.Context, repoRoot string) bool {
	out, err := git(ctx, repoRoot, "config", "--get", "remote.origin.url")
	return err == nil && strings.Contains(strings.ToLower(out), "github.com")
}

// Refs is what a new worktree of a repo can start from.
type Refs struct {
	Default string   // the default branch, such as "origin/main"; "" for none
	Local   []string // local branches
	Remote  []string // remote branches, such as "origin/fix"
	GitHub  bool     // see GitHub
}

// ListRefs returns the repo's branches, in git's order.
func ListRefs(ctx context.Context, repoRoot string) (Refs, error) {
	var r Refs
	def, err := defaultRef(ctx, repoRoot)
	if err != nil {
		return r, err
	}
	r.Default = strings.TrimPrefix(strings.TrimPrefix(def, "refs/remotes/"), "refs/heads/")
	out, err := git(ctx, repoRoot, "for-each-ref", "--format=%(refname)", "refs/heads", "refs/remotes")
	if err != nil {
		return r, err
	}
	for _, ref := range strings.Fields(out) {
		if name, ok := strings.CutPrefix(ref, "refs/heads/"); ok {
			r.Local = append(r.Local, name)
		} else if name, ok := strings.CutPrefix(ref, "refs/remotes/"); ok && !strings.HasSuffix(name, "/HEAD") {
			r.Remote = append(r.Remote, name)
		}
	}
	r.GitHub = GitHub(ctx, repoRoot)
	return r, nil
}

// Status lists what git status shows in worktree: changed, staged and
// untracked paths, an untracked folder as one "dir/".
func Status(ctx context.Context, worktree string) ([]string, error) {
	out, err := git(ctx, worktree, "status", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		files = append(files, f[3:])
		if f[0] == 'R' || f[0] == 'C' { // the old path follows
			i++
		}
	}
	return files, nil
}

// Merged reports whether every commit of local branch is on the default
// branch, so git branch -d deletes it without losing work. Without a
// default branch nothing counts as merged.
func Merged(ctx context.Context, repoRoot, branch string) (bool, error) {
	base, err := defaultRef(ctx, repoRoot)
	if err != nil || base == "" {
		return false, err
	}
	_, err = git(ctx, repoRoot, "merge-base", "--is-ancestor", "refs/heads/"+branch, base)
	if err == nil {
		return true, nil
	}
	if exitCode(err) == 1 && ctx.Err() == nil {
		return false, nil
	}
	return false, err
}

// Prune drops git's records of worktrees whose folder is gone.
func Prune(ctx context.Context, repoRoot string) error {
	_, err := git(ctx, repoRoot, "worktree", "prune")
	return err
}

// Orphans lists the worktrees under <repoRoot>/.worktrees/ that used
// says no tab uses, with their last commit and whether they have changes.
func Orphans(ctx context.Context, repoRoot string, used func(path string) bool) ([]model.Orphan, error) {
	trees, err := ListWorktrees(ctx, repoRoot)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(repoRoot, ".worktrees")
	var out []model.Orphan
	for _, t := range trees {
		if filepath.Dir(filepath.Clean(t.Path)) != dir || used(t.Path) {
			continue
		}
		o := model.Orphan{Root: repoRoot, Path: t.Path, Branch: t.Branch}
		if s, err := git(ctx, t.Path, "log", "-1", "--format=%ct"); err == nil {
			if sec, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				o.Committed = time.Unix(sec, 0)
			}
		}
		changed, err := Status(ctx, t.Path)
		o.Dirty = err != nil || len(changed) > 0 // unreadable counts as changed: delete asks to force
		out = append(out, o)
	}
	return out, nil
}

// ErrBranchKept wraps the error of a RemoveWorktree that removed the worktree
// but could not delete its branch, such as an unmerged one.
var ErrBranchKept = errors.New("worktree removed, branch kept")

// RemoveWorktree removes the worktree at path, and its branch when
// deleteBranch is set. Git refuses a worktree with changes, and an
// unmerged branch, unless force is set. Git never removes the main worktree.
func RemoveWorktree(ctx context.Context, repoRoot, path string, deleteBranch, force bool) error {
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
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	if _, err := git(ctx, repoRoot, append(args, "--", path)...); err != nil {
		return err
	}
	if branch != "" {
		del := "-d"
		if force {
			del = "-D"
		}
		if _, err := git(ctx, repoRoot, "branch", del, "--", branch); err != nil {
			return fmt.Errorf("%w: %w", ErrBranchKept, err)
		}
	}
	return nil
}

// BranchName is the branch a base ref names: "main" for
// "refs/remotes/origin/main" or "refs/heads/main".
func BranchName(ref string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ref, "refs/remotes/origin/"), "refs/heads/")
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
// ref they are against, as Stats' Base. Untracked files come last, Status '?',
// with Add their line count when the file is regular (not followed through
// a symlink), not binary and at most 1 MiB, else 0; Stats leaves them out,
// so the other files' totals are Stats'.
func Files(ctx context.Context, worktree string) (base string, files []FileStat, err error) {
	base, err = defaultRef(ctx, worktree)
	if err != nil || base == "" {
		return "", nil, err
	}
	root, ok := RepoRoot(ctx, worktree)
	if !ok {
		return "", nil, fmt.Errorf("%s is not in a git repository", worktree)
	}
	mb, err := mergeBase(ctx, root, base)
	if err != nil {
		return "", nil, err
	}
	if files, err = numstat(ctx, root, mb); err != nil {
		return "", nil, err
	}
	out, err := git(ctx, root, "diff", "--name-status", "-z", mb, "--")
	if err != nil {
		return "", nil, err
	}
	status := map[string]byte{}
	fields := strings.Split(out, "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		st := fields[i]
		if st == "" {
			break
		}
		if st[0] == 'R' || st[0] == 'C' { // old path, then new path
			i++
		}
		if i+1 < len(fields) {
			status[fields[i+1]] = st[0]
		}
	}
	for i := range files {
		switch st := status[files[i].Path]; st {
		case 'A', 'D', 'R':
			files[i].Status = st
		default:
			files[i].Status = 'M'
		}
	}
	out, err = git(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", nil, err
	}
	for _, path := range strings.Split(out, "\x00") {
		if path == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", nil, err
		}
		files = append(files, FileStat{Path: path, Add: countLines(filepath.Join(root, path)), Status: '?'})
	}
	return base, files, nil
}

func mergeBase(ctx context.Context, dir, ref string) (string, error) {
	out, err := git(ctx, dir, "merge-base", ref, "HEAD")
	return strings.TrimSpace(out), err
}

// numstat is the line counts per file from rev to the work tree, by final
// path, in path order. A binary file counts 0.
func numstat(ctx context.Context, dir, rev string) ([]FileStat, error) {
	out, err := git(ctx, dir, "diff", "--numstat", "-z", rev, "--")
	if err != nil {
		return nil, err
	}
	var files []FileStat
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		counts := strings.SplitN(fields[i], "\t", 3)
		if len(counts) < 3 {
			continue
		}
		f := FileStat{Path: counts[2]}
		if f.Path == "" && i+2 < len(fields) { // a rename: old and new path follow
			f.Path = fields[i+2]
			i += 2
		}
		if counts[0] != "-" {
			if f.Add, err = strconv.Atoi(counts[0]); err != nil {
				return nil, err
			}
			if f.Del, err = strconv.Atoi(counts[1]); err != nil {
				return nil, err
			}
		}
		files = append(files, f)
	}
	slices.SortFunc(files, func(a, b FileStat) int { return strings.Compare(a.Path, b.Path) })
	return files, nil
}

// maxCount is the largest untracked file whose lines Files counts.
const maxCount = 1 << 20

// countLines is the line count of a regular text file of at most maxCount
// bytes, not followed through a symlink; 0 for anything else.
func countLines(path string) int {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxCount {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxCount+1))
	if err != nil || len(b) == 0 || len(b) > maxCount || bytes.IndexByte(b, 0) >= 0 {
		return 0
	}
	n := bytes.Count(b, []byte("\n"))
	if b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// Changes is what a worktree's branch changed since its merge base with the
// default branch, by final path relative to the repo root, for the conflict
// radar.
type Changes struct {
	Common      string   // the git directory the repository's worktrees share
	Root        string   // the worktree's top level
	Head        string   // HEAD's commit
	Committed   []string // from the merge base to HEAD
	Uncommitted []string // from HEAD to the work tree: staged, unstaged and untracked
}

// Changed returns worktree's Changes. A repository without a default
// branch has Common, Root and Head but no files.
func Changed(ctx context.Context, worktree string) (Changes, error) {
	var c Changes
	out, err := git(ctx, worktree, "rev-parse", "--path-format=absolute", "--git-common-dir", "--show-toplevel", "HEAD")
	if err != nil {
		return c, err
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 3 {
		return c, fmt.Errorf("git rev-parse: unexpected output %q", out)
	}
	c.Common, c.Root, c.Head = lines[0], lines[1], lines[2]
	base, err := defaultRef(ctx, c.Root)
	if err != nil || base == "" {
		return c, err
	}
	mb, err := mergeBase(ctx, c.Root, base)
	if err != nil {
		return c, err
	}
	if c.Committed, err = names(ctx, c.Root, "diff", "--name-only", "-z", mb, "HEAD", "--"); err != nil {
		return c, err
	}
	if c.Uncommitted, err = names(ctx, c.Root, "diff", "--name-only", "-z", "HEAD", "--"); err != nil {
		return c, err
	}
	untracked, err := names(ctx, c.Root, "ls-files", "--others", "--exclude-standard", "-z")
	c.Uncommitted = append(c.Uncommitted, untracked...)
	return c, err
}

// names is the NUL-separated paths git prints for args.
func names(ctx context.Context, dir string, args ...string) ([]string, error) {
	out, err := git(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths, nil
}

// Conflicts is the files that merging commits a and b of the repository at
// dir would leave in conflict, none when they merge cleanly. Nothing is
// written to the work tree or the index.
func Conflicts(ctx context.Context, dir, a, b string) ([]string, error) {
	out, err := git(ctx, dir, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", a, b)
	switch {
	case err == nil:
		return nil, nil
	case exitCode(err) != 1 || ctx.Err() != nil:
		return nil, err
	}
	// The merged tree, then each conflicted path once, then an empty field.
	fields := strings.Split(out, "\x00")
	var files []string
	for _, f := range fields[1:] {
		if f == "" {
			break
		}
		if !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	return files, nil
}
