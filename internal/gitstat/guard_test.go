package gitstat

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/gitenv"
	"github.com/quanticstudios/pitwall/internal/model"
)

// Adapted from tuios (MIT): internal/childproc/git_test.go

// probes writes shell scripts that record their name when git runs them.
type probes struct {
	t      *testing.T
	dir    string
	marker string
}

func newProbes(t *testing.T) *probes {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the probes are shell scripts")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := longTempDir(t)
	return &probes{t: t, dir: dir, marker: filepath.Join(dir, "ran")}
}

func (p *probes) script(name string) string {
	p.t.Helper()
	path := filepath.Join(p.dir, "probe-"+name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho "+name+" >> '"+p.marker+"'\nexit 0\n"), 0o755); err != nil {
		p.t.Fatal(err)
	}
	return path
}

// ran returns the probes that ran since the last call, and forgets them.
func (p *probes) ran() []string {
	data, _ := os.ReadFile(p.marker)
	_ = os.Remove(p.marker)
	f := strings.Fields(string(data))
	slices.Sort(f)
	return slices.Compact(f)
}

// isolate points git at a global config this test owns, holding global.
func isolate(t *testing.T, dir, global string) {
	t.Helper()
	path := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(path, []byte("[user]\n\tname = t\n\temail = t@example.invalid\n[init]\n\tdefaultBranch = main\n"+global), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// plain runs git as a person's shell would, with nothing of Command's.
func plain(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	_ = cmd.Run()
}

// mustPlain is plain that fails the test when git does, for a repository's
// setup.
func mustPlain(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// staleStat gives a file the same size and a new mtime, so git must read it
// through its filter to know it changed.
func staleStat(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
}

// poisonedRepo is a repository, with a branch feat that conflicts with main,
// whose own config names a program for each key that can run one. a.txt has a
// same-size edit, so a read of it goes through its filter.
func poisonedRepo(t *testing.T, p *probes) string {
	t.Helper()
	isolate(t, p.dir, "")
	root := filepath.Join(p.dir, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "init", "-q")
	writeFile(t, root, "a.txt", "main\n")
	writeFile(t, root, "b.proc", "proc\n")
	writeFile(t, root, "c", "no attributes\n")
	mustPlain(t, root, "add", ".")
	mustPlain(t, root, "commit", "-qm", "first")
	mustPlain(t, root, "switch", "-qc", "feat")
	writeFile(t, root, "a.txt", "feat\n")
	mustPlain(t, root, "commit", "-qam", "feat")
	mustPlain(t, root, "switch", "-q", "main")
	writeFile(t, root, "a.txt", "mine\n")
	mustPlain(t, root, "commit", "-qam", "mine")

	hooks := filepath.Join(p.dir, "hooks")
	if err := os.Mkdir(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := p.script("hook-file")
	for _, name := range []string{"reference-transaction", "post-index-change", "post-checkout"} {
		if err := os.Symlink(hook, filepath.Join(hooks, name)); err != nil {
			t.Fatal(err)
		}
		mustPlain(t, root, "config", "--add", "hook.planted.event", name)
		mustPlain(t, root, "config", "--add", "hook..event", name)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("*.txt filter=drv diff=drv merge=drv\n*.proc filter=proc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{
		{"core.fsmonitor", p.script("fsmonitor")},
		{"core.hooksPath", hooks},
		{"hook.planted.command", p.script("hook-config")},
		{"hook..command", p.script("hook-empty")},
		{"diff.external", p.script("external")},
		{"diff.drv.textconv", p.script("textconv")},
		{"diff.drv.command", p.script("driver-command")},
		{"filter.drv.clean", p.script("clean")},
		{"filter.drv.smudge", p.script("smudge")},
		{"filter.drv.required", "true"},
		{"filter.proc.process", p.script("process")},
		{"merge.drv.driver", p.script("merge")},
		{"core.pager", p.script("pager")},
		{"pager.status", p.script("pager")},
		{"pager.diff", p.script("pager")},
		{"core.editor", p.script("editor")},
		{"sequence.editor", p.script("editor")},
		{"commit.gpgSign", "true"},
		{"gpg.program", p.script("gpg")},
		{"core.sshCommand", p.script("ssh")},
		{"core.askPass", p.script("askpass")},
		{"credential.helper", "!" + p.script("credential")},
		{"remote.origin.url", "ssh://github.com.invalid/o/r"},
		{"remote.origin.uploadpack", p.script("uploadpack")},
		{"remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*"},
	} {
		mustPlain(t, root, "config", kv[0], kv[1])
	}
	staleStat(t, filepath.Join(root, "a.txt"), "edit\n")
	writeFile(t, root, "c", "changed\n")
	return root
}

// TestPoisonRunsUnderPlainGit is what makes TestRepoConfigRunsNothing mean
// anything: the same commands from a shell run each program.
func TestPoisonRunsUnderPlainGit(t *testing.T) {
	p := newProbes(t)
	root := poisonedRepo(t, p)
	index := filepath.Join(p.dir, "index")
	plain(t, root, nil, "status", "--porcelain", "-z")
	plain(t, root, nil, "-c", "core.quotePath=true", "diff", "--no-color", "HEAD", "--")
	plain(t, root, nil, "diff", "--no-color", "--no-ext-diff", "HEAD", "--")
	plain(t, root, []string{"GIT_INDEX_FILE=" + index}, "add", "-A")
	plain(t, root, nil, "update-ref", "refs/pitwall/x", "HEAD")
	plain(t, root, nil, "merge-tree", "--write-tree", "main", "feat")
	plain(t, root, nil, "worktree", "add", "-q", filepath.Join(p.dir, "wt"), "feat")
	got := p.ran()
	want := []string{"clean", "driver-command", "external", "fsmonitor", "hook-file", "merge", "process", "smudge", "textconv"}
	// An older git has no hooks in config, and no planted hook to list.
	if out, err := exec.Command("git", "-C", root, "hook", "list", "reference-transaction").Output(); err == nil && strings.Contains(string(out), "planted") {
		want = append(want, "hook-config", "hook-empty")
	}
	for _, name := range want {
		if !slices.Contains(got, name) {
			t.Errorf("plain git did not run the %s probe, so a test of Command checks nothing there; it ran %v", name, got)
		}
	}
}

// TestRepoConfigRunsNothing runs every path that reaches git by itself in a
// repository whose config names a program for each key that can run one.
func TestRepoConfigRunsNothing(t *testing.T) {
	p := newProbes(t)
	root := poisonedRepo(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	wt := filepath.Join(root, ".worktrees", "made")
	var rev string
	steps := []struct {
		name string
		run  func() error
	}{
		{"Status", func() error { _, err := Status(ctx, root); return err }},
		{"Stats", func() error { _, err := Stats(ctx, root); return err }},
		{"Files", func() error { _, _, err := Files(ctx, root); return err }},
		{"Diff", func() error { _, err := Diff(ctx, root); return err }},
		{"Changed", func() error { _, err := Changed(ctx, root); return err }},
		{"ReadStaging", func() error { _, err := ReadStaging(ctx, root); return err }},
		{"Conflicts", func() error { _, err := Conflicts(ctx, root, "main", "feat"); return err }},
		{"ListRefs", func() error { _, err := ListRefs(ctx, root); return err }},
		{"Merged", func() error { _, err := Merged(ctx, root, "feat"); return err }},
		{"Checkpoint", func() (err error) { rev, err = Checkpoint(ctx, root, "w"); return err }},
		{"TurnDiff", func() error { _, err := TurnDiff(ctx, root, rev); return err }},
		{"CheckpointDiff", func() error { _, _, err := CheckpointDiff(ctx, root, rev, ""); return err }},
		{"DropTurns", func() error { return DropTurns(ctx, root, "w") }},
		{"AddWorktree", func() error {
			_, _, err := AddWorktree(ctx, root, "made", model.WorktreeFrom{Kind: model.FromBranch, Ref: "feat"})
			return err
		}},
		{"Orphans", func() error {
			_, err := Orphans(ctx, root, func(string) bool { return false })
			return err
		}},
		{"RemoveWorktree", func() error { return RemoveWorktree(ctx, root, wt, false, true) }},
		{"Prune", func() error { return Prune(ctx, root) }},
		// The remote does not resolve, so this is only for the programs a
		// fetch starts; its failure is the point.
		{"AddWorktree from a pull request", func() error {
			_, _, _ = AddWorktree(ctx, root, "pr", model.WorktreeFrom{Kind: model.FromPR, PR: 1})
			return nil
		}},
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			t.Errorf("%s: %v", s.name, err)
		}
		if got := p.ran(); len(got) > 0 {
			t.Errorf("%s ran the repository's programs: %v", s.name, got)
		}
	}
}

// TestGlobalFilterStillApplies is a filter git-lfs would install: from the
// person's global config it keeps working, even when the repository names the
// same filter with a program of its own.
func TestGlobalFilterStillApplies(t *testing.T) {
	p := newProbes(t)
	isolate(t, p.dir, "[filter \"lfs\"]\n\tclean = tr a-z A-Z\n\tsmudge = tr A-Z a-z\n\trequired = true\n")
	root := filepath.Join(p.dir, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("*.dat filter=lfs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "f.dat", "first\n")
	mustPlain(t, root, "add", ".")
	mustPlain(t, root, "commit", "-qm", "first")
	mustPlain(t, root, "config", "filter.lfs.clean", p.script("clean"))
	writeFile(t, root, "new.dat", "second\n")

	ctx := context.Background()
	rev, err := Checkpoint(ctx, root, "w")
	if err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, root, "show", rev+":new.dat"); got != "SECOND" {
		t.Errorf("the checkpoint holds %q, want the global clean filter's SECOND", got)
	}
	if name, err := repoFilter(ctx, root); err != nil || name != "" {
		t.Errorf("repoFilter = %q, %v, want none: the person's config names the same filter", name, err)
	}
	if err := StageFiles(ctx, root, []string{"new.dat"}, false); err != nil {
		t.Errorf("StageFiles: %v", err)
	}
	path, _, err := AddWorktree(ctx, root, "made", model.WorktreeFrom{Kind: model.FromNew})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, path, "f.dat"); got != "first\n" {
		t.Errorf("a new worktree holds %q, want the global smudge filter's first", got)
	}
	if got := p.ran(); len(got) > 0 {
		t.Errorf("ran the repository's programs: %v", got)
	}
}

// TestCheckpointCapturesFiles checks the snapshot in a poisoned repository:
// what it holds, and that undo puts it back without running anything.
func TestCheckpointCapturesFiles(t *testing.T) {
	p := newProbes(t)
	root := poisonedRepo(t, p)
	writeFile(t, root, "new.txt", "new\n")
	writeFile(t, root, ".gitignore", "ignored\n")
	writeFile(t, root, "ignored", "not this\n")
	if err := os.Remove(filepath.Join(root, "b.proc")); err != nil {
		t.Fatal(err)
	}
	rev, err := Checkpoint(context.Background(), root, "w")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Fields(runGit(t, root, "ls-tree", "--name-only", rev))
	if want := []string{".gitignore", "a.txt", "c", "new.txt"}; !slices.Equal(got, want) {
		t.Errorf("the checkpoint holds %v, want %v", got, want)
	}
	if got := runGit(t, root, "show", rev+":a.txt"); got != "edit" {
		t.Errorf("a.txt in the checkpoint is %q, want the work tree's edit, unfiltered", got)
	}
	if got := p.ran(); len(got) > 0 {
		t.Errorf("ran the repository's programs: %v", got)
	}
}

// TestSubmoduleConfigRunsNothing is a planted submodule: its own config names
// a filter, whose name the outer repository's config cannot tell Command, and
// a .gitmodules says to look inside it, which beats diff.ignoreSubmodules.
func TestSubmoduleConfigRunsNothing(t *testing.T) {
	p := newProbes(t)
	isolate(t, p.dir, "")
	root := filepath.Join(p.dir, "repo")
	const link = "s[u]b" // a pattern, to show the exclude takes it literally
	nested := filepath.Join(root, link)
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "init", "-q")
	mustPlain(t, nested, "init", "-q")
	writeFile(t, nested, "s.txt", "s\n")
	mustPlain(t, nested, "add", ".")
	mustPlain(t, nested, "commit", "-qm", "s")
	writeFile(t, root, ".gitmodules", "[submodule \"m\"]\n\tpath = "+link+"\n\turl = ../x\n\tignore = none\n")
	writeFile(t, root, "sub/kept.txt", "old\n") // what the pattern would match
	mustPlain(t, root, "add", ".")
	mustPlain(t, root, "commit", "-qm", "first")
	mustPlain(t, nested, "config", "filter.x.clean", p.script("nested-clean"))
	if err := os.MkdirAll(filepath.Join(nested, ".git", "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".git", "info", "attributes"), []byte("*.txt filter=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleStat(t, filepath.Join(nested, "s.txt"), "S\n")
	writeFile(t, root, "sub/kept.txt", "new\n")

	plain(t, root, nil, "status", "--porcelain")
	if got := p.ran(); !slices.Contains(got, "nested-clean") {
		t.Fatalf("plain git status did not run the submodule's filter, so nothing here can be checked; it ran %v", got)
	}
	index := filepath.Join(p.dir, "index")
	plain(t, root, []string{"GIT_INDEX_FILE=" + index}, "read-tree", "HEAD")
	plain(t, root, []string{"GIT_INDEX_FILE=" + index}, "add", "-A")
	if got := p.ran(); !slices.Contains(got, "nested-clean") {
		t.Fatalf("plain git add -A did not run the submodule's filter, so nothing here can be checked; it ran %v", got)
	}
	ctx := context.Background()
	rev, err := Checkpoint(ctx, root, "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Status(ctx, root); err != nil {
		t.Error(err)
	}
	if _, err := Changed(ctx, root); err != nil {
		t.Error(err)
	}
	if got := p.ran(); len(got) > 0 {
		t.Errorf("Checkpoint, Status and diff ran the submodule's programs: %v", got)
	}
	if got := runGit(t, root, "show", rev+":sub/kept.txt"); got != "new" {
		t.Errorf("sub/kept.txt in the checkpoint is %q, want new", got)
	}

	// A moved submodule is recorded, as add -A would.
	writeFile(t, nested, "n.txt", "n\n")
	mustPlain(t, nested, "-c", "filter.x.clean=cat", "add", "n.txt")
	mustPlain(t, nested, "-c", "filter.x.clean=cat", "commit", "-qm", "moved")
	moved := runGit(t, nested, "rev-parse", "HEAD")
	if rev, err = Checkpoint(ctx, root, "w"); err != nil {
		t.Fatal(err)
	}
	if got := runGit(t, root, "rev-parse", rev+":"+link); got != moved {
		t.Errorf("the checkpoint has the submodule at %s, want its HEAD %s", got, moved)
	}
	if got := p.ran(); len(got) > 0 {
		t.Errorf("Checkpoint ran the submodule's programs: %v", got)
	}
}

// TestRemoveWorktreeReadsItsConfig is a filter set only in the worktree's own
// config, which the repository's root cannot see and the git status git runs
// to check the folder does.
func TestRemoveWorktreeReadsItsConfig(t *testing.T) {
	p := newProbes(t)
	isolate(t, p.dir, "")
	root := filepath.Join(p.dir, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "init", "-q")
	writeFile(t, root, "a.txt", "a\n")
	mustPlain(t, root, "add", ".")
	mustPlain(t, root, "commit", "-qm", "first")
	ctx := context.Background()
	wt, _, err := AddWorktree(ctx, root, "w", model.WorktreeFrom{Kind: model.FromNew})
	if err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "config", "extensions.worktreeConfig", "true")
	mustPlain(t, wt, "config", "--worktree", "filter.y.clean", p.script("worktree-clean"))
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("*.txt filter=y\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	staleStat(t, filepath.Join(wt, "a.txt"), "a\n")
	if err := RemoveWorktree(ctx, root, wt, false, false); err != nil {
		t.Fatal(err)
	}
	if got := p.ran(); len(got) > 0 {
		t.Errorf("RemoveWorktree ran the worktree's programs: %v", got)
	}
}

// TestRepoFilterRefuses is why a command that writes must not read past a
// filter: with git-crypt's config in .git/config, staging would store the
// plain secret.
func TestRepoFilterRefuses(t *testing.T) {
	p := newProbes(t)
	isolate(t, p.dir, "")
	root := filepath.Join(p.dir, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "init", "-q")
	writeFile(t, root, "a.txt", "a\n")
	mustPlain(t, root, "add", ".")
	mustPlain(t, root, "commit", "-qm", "first")
	ctx := context.Background()
	if _, err := Checkpoint(ctx, root, "w"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "a.txt", "secret\n")
	mustPlain(t, root, "config", "filter.crypt.clean", p.script("clean"))
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("*.txt filter=crypt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if key, err := repoFilter(ctx, root); err != nil || key != "filter.crypt.clean" {
		t.Fatalf("repoFilter = %q, %v, want filter.crypt.clean", key, err)
	}
	for name, err := range map[string]error{
		"StageFiles":  StageFiles(ctx, root, []string{"a.txt"}, false),
		"ApplyCached": ApplyCached(ctx, root, []byte("patch"), false),
		"Discard":     Discard(ctx, root, []string{"a.txt"}, false),
		"UndoTurn":    UndoTurn(ctx, root, "w"),
		"RedoTurn":    RedoTurn(ctx, root, "w"),
	} {
		if err == nil || !strings.Contains(err.Error(), "filter.crypt.clean") {
			t.Errorf("%s = %v, want a refusal naming the filter", name, err)
		}
	}
	if got := runGit(t, root, "diff", "--cached", "--name-only"); got != "" {
		t.Errorf("staged %q", got)
	}
	if got := readFile(t, root, "a.txt"); got != "secret\n" {
		t.Errorf("a.txt is %q, a refusal must leave it", got)
	}
	if got := p.ran(); len(got) > 0 {
		t.Errorf("ran the repository's programs: %v", got)
	}
}

// TestEmptyNamesRunNothing: git takes an empty subsection as a name, and an
// attribute's empty value picks it, so filter..clean runs for filter=.
func TestEmptyNamesRunNothing(t *testing.T) {
	p := newProbes(t)
	isolate(t, p.dir, "")
	root := filepath.Join(p.dir, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "init", "-q")
	writeFile(t, root, "a.txt", "main\n")
	mustPlain(t, root, "add", ".")
	mustPlain(t, root, "commit", "-qm", "first")
	mustPlain(t, root, "switch", "-qc", "feat")
	writeFile(t, root, "a.txt", "feat\n")
	mustPlain(t, root, "commit", "-qam", "feat")
	mustPlain(t, root, "switch", "-q", "main")
	writeFile(t, root, "a.txt", "mine\n")
	mustPlain(t, root, "commit", "-qam", "mine")
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "attributes"), []byte("*.txt filter= merge= diff=\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kv := range [][2]string{
		{"filter..clean", p.script("clean")},
		{"merge..driver", p.script("merge")},
		{"diff..command", p.script("driver-command")},
		{"diff..textconv", p.script("textconv")},
	} {
		mustPlain(t, root, "config", kv[0], kv[1])
	}
	staleStat(t, filepath.Join(root, "a.txt"), "edit\n")
	plain(t, root, nil, "status", "--porcelain")
	plain(t, root, nil, "diff", "HEAD", "--")
	plain(t, root, nil, "diff", "--no-ext-diff", "HEAD", "--")
	plain(t, root, nil, "merge-tree", "--write-tree", "main", "feat")
	got := p.ran()
	for _, name := range []string{"clean", "driver-command", "merge", "textconv"} {
		if !slices.Contains(got, name) {
			t.Errorf("plain git did not run the %s probe, so nothing here checks it; it ran %v", name, got)
		}
	}
	ctx := context.Background()
	if _, err := Status(ctx, root); err != nil {
		t.Error(err)
	}
	if _, err := Diff(ctx, root); err != nil {
		t.Error(err)
	}
	if _, err := Conflicts(ctx, root, "main", "feat"); err != nil {
		t.Error(err)
	}
	if _, err := Checkpoint(ctx, root, "w"); err != nil {
		t.Error(err)
	}
	if got := p.ran(); len(got) > 0 {
		t.Errorf("ran the repository's programs: %v", got)
	}
	// The diff flags stop these two as well, so only the override can show here,
	// for the git inside gh, which takes no flags from pitwall.
	env, err := Env(ctx, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"diff..command", "diff..textconv"} {
		if !slices.ContainsFunc(env, func(kv string) bool { return strings.HasSuffix(kv, "="+key) }) {
			t.Errorf("Env has no override for %s", key)
		}
	}
	if err := StageFiles(ctx, root, []string{"a.txt"}, false); err == nil || !strings.Contains(err.Error(), "filter..clean") {
		t.Errorf("StageFiles = %v, want a refusal naming filter..clean", err)
	}
}

// TestConditionalConfigRunsNothing: a file the repository includes under a
// condition that holds only in the new worktree names a filter and a hook. Env
// reads config in the source worktree, where the condition does not hold.
func TestConditionalConfigRunsNothing(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, root, poison string)
	}{
		{"onbranch", func(t *testing.T, root, poison string) {
			mustPlain(t, root, "config", "includeIf.onbranch:made.path", poison)
		}},
		{"gitdir", func(t *testing.T, root, poison string) {
			mustPlain(t, root, "config", "includeIf.gitdir:**/worktrees/made.path", poison)
		}},
		{"relative and nested in an included file", func(t *testing.T, root, poison string) {
			outer := filepath.Join(root, ".git", "outer-config")
			if err := os.WriteFile(outer, []byte("[includeIf \"onbranch:made\"]\n\tpath = late-config\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mustPlain(t, root, "config", "include.path", "outer-config")
		}},
		{"plain include in a conditional file", func(t *testing.T, root, poison string) {
			outer := filepath.Join(root, ".git", "outer-config")
			if err := os.WriteFile(outer, []byte("[include]\n\tpath = late-config\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mustPlain(t, root, "config", "includeIf.onbranch:made.path", "outer-config")
		}},
		{"nested in a conditional file", func(t *testing.T, root, poison string) {
			outer := filepath.Join(root, ".git", "outer-config")
			if err := os.WriteFile(outer, []byte("[includeIf \"onbranch:made\"]\n\tpath = late-config\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			mustPlain(t, root, "config", "includeIf.onbranch:made.path", "outer-config")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newProbes(t)
			isolate(t, p.dir, "")
			root := filepath.Join(p.dir, "repo")
			if err := os.Mkdir(root, 0o755); err != nil {
				t.Fatal(err)
			}
			mustPlain(t, root, "init", "-q")
			writeFile(t, root, ".gitattributes", "*.txt filter=late\n")
			writeFile(t, root, "a.txt", "a\n")
			mustPlain(t, root, "add", ".")
			mustPlain(t, root, "commit", "-qm", "first")
			poison := filepath.Join(root, ".git", "late-config")
			body := "[filter \"late\"]\n\tsmudge = " + p.script("smudge") + "\n[hook \"late\"]\n\tcommand = " + p.script("hook") +
				"\n\tevent = reference-transaction\n\tevent = post-checkout\n"
			if err := os.WriteFile(poison, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			c.setup(t, root, poison)

			wt := filepath.Join(p.dir, "made")
			plain(t, root, nil, "worktree", "add", "-q", "-b", "made", wt)
			if got := p.ran(); !slices.Contains(got, "smudge") {
				t.Fatalf("plain git worktree add did not run the included filter, so nothing here can be checked; it ran %v", got)
			}
			plain(t, root, nil, "worktree", "remove", "--force", wt)
			plain(t, root, nil, "branch", "-qD", "made")
			p.ran()

			ctx := context.Background()
			path, _, err := AddWorktree(ctx, root, "made", model.WorktreeFrom{Kind: model.FromNew})
			if err != nil {
				t.Fatal(err)
			}
			if got := p.ran(); len(got) > 0 {
				t.Errorf("AddWorktree ran the included programs: %v", got)
			}
			if got := readFile(t, path, "a.txt"); got != "a\n" {
				t.Errorf("a.txt in the worktree is %q, want its blob unfiltered", got)
			}
			if _, err := Status(ctx, path); err != nil {
				t.Error(err)
			}
			if got := p.ran(); len(got) > 0 {
				t.Errorf("Status in the worktree ran the included programs: %v", got)
			}
		})
	}
}

// conditionalRepo is a repository whose .gitattributes picks the filter late, and
// a config file holding a program for it with a condition of its own that holds
// on main, where the file below is read, and not on the new branch made.
func conditionalRepo(t *testing.T, p *probes, nested string) string {
	t.Helper()
	isolate(t, p.dir, "")
	root := filepath.Join(p.dir, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "init", "-q")
	writeFile(t, root, ".gitattributes", "*.txt filter=late\n")
	writeFile(t, root, "a.txt", "a\n")
	mustPlain(t, root, "add", ".")
	mustPlain(t, root, "commit", "-qm", "first")
	body := "[filter \"late\"]\n\tsmudge = " + p.script("smudge") + "\n[includeIf \"onbranch:main\"]\n\tpath = " + nested + "\n"
	if err := os.WriteFile(filepath.Join(root, ".git", "late-config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "config", "includeIf.onbranch:made.path", "late-config")
	return root
}

// TestNestedConditionRunsNothing: late-config's nested condition holds on main,
// so a read of the file with --includes depends on main, and fails when what it
// includes is malformed. On made that include is not read, and git takes the
// file and runs its filter.
func TestNestedConditionRunsNothing(t *testing.T) {
	p := newProbes(t)
	root := conditionalRepo(t, p, "bad-config")
	if err := os.WriteFile(filepath.Join(root, ".git", "bad-config"), []byte("[invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(p.dir, "made")
	plain(t, root, nil, "worktree", "add", "-q", "-b", "made", wt)
	if got := p.ran(); !slices.Contains(got, "smudge") {
		t.Fatalf("plain git worktree add did not run the filter, so nothing here can be checked; it ran %v", got)
	}
	plain(t, root, nil, "worktree", "remove", "--force", wt)
	plain(t, root, nil, "branch", "-qD", "made")
	p.ran()

	ctx := context.Background()
	path, _, err := AddWorktree(ctx, root, "made", model.WorktreeFrom{Kind: model.FromNew})
	if err == nil || !strings.Contains(err.Error(), "bad-config") {
		t.Errorf("AddWorktree = %v, want a refusal naming bad-config", err)
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("AddWorktree made %s though it refused", path)
	}
	if got := p.ran(); len(got) > 0 {
		t.Errorf("AddWorktree ran the included programs: %v", got)
	}

	// With the file read by itself, a good nested file does not hide the filter.
	if err := os.WriteFile(filepath.Join(root, ".git", "bad-config"), []byte("[user]\n\tname = t\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AddWorktree(ctx, root, "made", model.WorktreeFrom{Kind: model.FromNew}); err != nil {
		t.Fatal(err)
	}
	if got := p.ran(); len(got) > 0 {
		t.Errorf("AddWorktree ran the included programs: %v", got)
	}
}

// TestUnreadableIncludeFailsClosed is every way a file the repository includes
// can fail to read. No program runs, and each error says which file, not that
// there is no repository or no branch.
func TestUnreadableIncludeFailsClosed(t *testing.T) {
	for _, c := range []struct {
		name, include, want string
		setup               func(t *testing.T, root string)
	}{
		{"malformed", "bad-config", "bad-config", func(t *testing.T, root string) {
			if err := os.WriteFile(filepath.Join(root, ".git", "bad-config"), []byte("[invalid\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"unreadable", "secret-config", "secret-config", func(t *testing.T, root string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads a file of any mode")
			}
			if err := os.WriteFile(filepath.Join(root, ".git", "secret-config"), []byte("[user]\n\tname = t\n"), 0o000); err != nil {
				t.Fatal(err)
			}
		}},
		{"a home that does not expand", "~no-such-user-here/config", "~no-such-user-here/config", func(*testing.T, string) {}},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := newProbes(t)
			root := conditionalRepo(t, p, "unused")
			c.setup(t, root)
			mustPlain(t, root, "config", "includeIf.onbranch:never.path", c.include)
			ctx := context.Background()
			for name, err := range map[string]error{
				"AddWorktree": func() error {
					_, _, err := AddWorktree(ctx, root, "made", model.WorktreeFrom{Kind: model.FromNew})
					return err
				}(),
				"AddWorktree from a branch": func() error {
					_, _, err := AddWorktree(ctx, root, "other", model.WorktreeFrom{Kind: model.FromBranch, Ref: "main"})
					return err
				}(),
				"Checkpoint": func() error { _, err := Checkpoint(ctx, root, "w"); return err }(),
				"Status":     func() error { _, err := Status(ctx, root); return err }(),
				"Diff":       func() error { _, err := Diff(ctx, root); return err }(),
			} {
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Errorf("%s = %v, want an error naming %s", name, err, c.want)
				}
			}
			if got := p.ran(); len(got) > 0 {
				t.Errorf("ran the included programs: %v", got)
			}
		})
	}
}

// TestRemoveWorktreeFailsClosed: a worktree's own config that cannot be read
// stops the git status run in that folder from going ahead unguarded. Force runs
// no such status, so it still deletes a worktree that is broken this way.
func TestRemoveWorktreeFailsClosed(t *testing.T) {
	p := newProbes(t)
	isolate(t, p.dir, "")
	root := filepath.Join(p.dir, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "init", "-q")
	writeFile(t, root, "a.txt", "a\n")
	mustPlain(t, root, "add", ".")
	mustPlain(t, root, "commit", "-qm", "first")
	ctx := context.Background()
	wt, _, err := AddWorktree(ctx, root, "w", model.WorktreeFrom{Kind: model.FromNew})
	if err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "config", "extensions.worktreeConfig", "true")
	if err := os.WriteFile(filepath.Join(root, ".git", "worktrees", "w", "bad-config"), []byte("[invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, wt, "config", "--worktree", "includeIf.onbranch:never.path", "bad-config")
	if err := RemoveWorktree(ctx, root, wt, false, false); err == nil || !strings.Contains(err.Error(), "bad-config") {
		t.Errorf("RemoveWorktree = %v, want a refusal naming bad-config", err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the worktree is gone after a refusal: %v", err)
	}
	if err := RemoveWorktree(ctx, root, wt, false, true); err != nil {
		t.Errorf("RemoveWorktree with force: %v", err)
	}
}

// TestParseConfigRefusesOtherOutput: output not in the form expected must not
// read as a config with fewer keys.
func TestParseConfigRefusesOtherOutput(t *testing.T) {
	entries, err := parseConfig([]byte("local\x00file:c\x00a.b\nv\x00global\x00file:g\x00c.d\x00"))
	if err != nil || len(entries) != 2 || entries[0].value != "v" || entries[0].origin != "c" || entries[0].trusted || !entries[1].trusted {
		t.Errorf("parseConfig = %+v, %v", entries, err)
	}
	if entries, err := parseConfig(nil); err != nil || len(entries) != 0 {
		t.Errorf("parseConfig of nothing = %+v, %v", entries, err)
	}
	for _, out := range []string{"local\x00a.b\nv\x00", "local\x00file:c\x00a.b\nv"} {
		if _, err := parseConfig([]byte(out)); err == nil {
			t.Errorf("parseConfig(%q) took it", out)
		}
	}
}

// TestEnvReadsInactiveIncludes is the conditions and paths no command here makes
// true: hasconfig, for a remote added later, and a path from home.
func TestEnvReadsInactiveIncludes(t *testing.T) {
	p := newProbes(t)
	isolate(t, p.dir, "")
	t.Setenv("HOME", p.dir)
	root := filepath.Join(p.dir, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "init", "-q")
	if err := os.WriteFile(filepath.Join(p.dir, "late-config"), []byte("[filter \"late\"]\n\tclean = planted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "config", "includeIf.hasconfig:remote.*.url:https://late/**.path", "~/late-config")
	mustPlain(t, root, "config", "includeIf.onbranch:gone.path", "does-not-exist")
	env, err := Env(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(env, func(kv string) bool { return strings.HasSuffix(kv, "=filter.late.clean") }) {
		t.Error("Env has no override for a filter in a file included under a condition that does not hold")
	}
}

// TestEnvKeepsWhatIsThePersons is the variables Env leaves or drops: config a
// parent process set counts as the person's, and a GIT_DIR or GIT_INDEX_FILE
// from a parent's hook does not point git at another repository.
func TestEnvKeepsWhatIsThePersons(t *testing.T) {
	p := newProbes(t)
	isolate(t, p.dir, "")
	root := filepath.Join(p.dir, "repo")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	mustPlain(t, root, "init", "-q")
	for _, name := range strings.Fields(gitenv.RepositoryVars) {
		t.Setenv(name, filepath.Join(p.dir, "other"))
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "from the parent")
	if got := runGit(t, root, "config", "user.name"); got != "from the parent" {
		t.Errorf("user.name = %q, want the one the parent's GIT_CONFIG_KEY_0 sets", got)
	}
	if got := runGit(t, root, "config", "core.fsmonitor"); got != "false" {
		t.Errorf("core.fsmonitor = %q, want the fixed setting after the parent's", got)
	}
	if got, want := runGit(t, root, "rev-parse", "--show-toplevel"), root; got != want {
		t.Errorf("toplevel = %q, want %q: the parent's GIT_DIR was left in", got, want)
	}
	if key, err := repoFilter(context.Background(), root); err != nil || key != "" {
		t.Errorf("repoFilter = %q, %v, want no filter in the selected repository", key, err)
	}
}

// TestGuardArgs is where the flags go: after the subcommand, whatever precedes
// it.
func TestGuardArgs(t *testing.T) {
	drivers := []string{"--no-ext-diff", "--no-textconv"}
	for _, c := range []struct {
		in, want []string
	}{
		{[]string{"rev-parse", "HEAD"}, []string{"rev-parse", "HEAD"}},
		{[]string{"-c", "core.quotePath=true", "diff", "x"}, slices.Concat([]string{"-c", "core.quotePath=true", "diff"}, drivers, []string{"--ignore-submodules=dirty", "x"})},
		{[]string{"--literal-pathspecs", "status"}, []string{"--literal-pathspecs", "status", "--ignore-submodules=dirty"}},
		{[]string{"log", "-1"}, slices.Concat([]string{"log"}, drivers, []string{"-1"})},
		{[]string{"diff-tree", "-r"}, slices.Concat([]string{"diff-tree"}, drivers, []string{"-r"})},
	} {
		if got := guardArgs(c.in); !slices.Equal(got, c.want) {
			t.Errorf("guardArgs(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestGitStartsOnlyThroughCommand fails on a git pitwall starts by itself past
// Command, which would run what a repository's config names.
func TestGitStartsOnlyThroughCommand(t *testing.T) {
	raw := regexp.MustCompile(`exec\.Command(Context)?\([^)\n]*"git"`)
	for _, top := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(top, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || strings.Contains(path, "testdata") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if raw.Match(b) && filepath.Base(path) != "guard.go" {
				t.Errorf("%s starts git past gitstat.Command", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
