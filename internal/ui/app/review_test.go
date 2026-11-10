package app

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/gitenv"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// gitEnv keeps git in tests off the user's config, so no pager, hook or
// signing key of theirs runs.
func gitEnv(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Review Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "review@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Review Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "review@example.invalid")
}

func otherGitEnv(t *testing.T) []string {
	t.Helper()
	dir := t.TempDir()
	var env []string
	for _, name := range strings.Fields(gitenv.RepositoryVars) {
		env = append(env, name+"="+dir)
	}
	return env
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

// run runs argv as a pane would and returns what it printed.
func run(t *testing.T, argv []string, env ...string) string {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

// TestDiffPagerIgnoresRepoConfig: the diff pane's script evals the pager it
// finds, so core.pager from the repository's own config must not reach it, nor
// its fsmonitor run. The one from the person's config does.
func TestDiffPagerIgnoresRepoConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the probes are shell scripts")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	probe := func(name string) string {
		script := filepath.Join(dir, name)
		if err := os.WriteFile(script, []byte("#!/bin/sh\necho "+name+" >> '"+marker+"'\ncat >/dev/null\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return script
	}
	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(global, []byte("[user]\n\tname = t\n\temail = t@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_PAGER", "")
	git := func(args ...string) {
		t.Helper()
		run(t, append([]string{"git", "-C", repo}, args...))
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "a"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "a")
	git("commit", "-qm", "first")
	git("config", "core.pager", probe("repo-pager"))
	git("config", "core.fsmonitor", probe("fsmonitor"))
	run(t, diffCmd(repo, "refs/heads/main", nil))
	if b, _ := os.ReadFile(marker); len(b) > 0 {
		t.Errorf("the diff pane ran the repository's programs: %s", b)
	}
	run(t, []string{"git", "config", "--file", global, "core.pager", probe("my-pager")})
	run(t, diffCmd(repo, "refs/heads/main", nil))
	if b, _ := os.ReadFile(marker); !strings.Contains(string(b), "my-pager") || strings.Contains(string(b), "repo-pager") {
		t.Errorf("want the global pager and only it, ran: %q", b)
	}
}

// TestDiffCmd runs the diff panes' commands in a real repository, from a
// subdirectory, with cat for a pager: the base is the merge base with the
// default branch gitstat finds, so a commit only on main shows nowhere,
// and committed and uncommitted work both show.
func TestDiffCmd(t *testing.T) {
	gitEnv(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "--initial-branch=main")
	write(t, filepath.Join(dir, "a.txt"), "one\n")
	write(t, filepath.Join(dir, "sub", "b.txt"), "two\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "base")
	gitIn(t, dir, "checkout", "-b", "feature")
	write(t, filepath.Join(dir, "a.txt"), "one\ncommitted\n")
	gitIn(t, dir, "commit", "-am", "feature")
	gitIn(t, dir, "checkout", "main")
	write(t, filepath.Join(dir, "main.txt"), "main only\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "main")
	gitIn(t, dir, "checkout", "feature")
	write(t, filepath.Join(dir, "sub", "b.txt"), "two\nuncommitted\n")
	write(t, filepath.Join(dir, "new.txt"), "untracked\n")

	stats, err := gitstat.Stats(context.Background(), dir)
	if err != nil || stats.Base != "refs/heads/main" {
		t.Fatalf("Stats = %+v, %v; want Base refs/heads/main", stats, err)
	}
	sub := filepath.Join(dir, "sub")
	for _, c := range []struct {
		name      string
		file      *gitstat.FileStat
		want, not []string
	}{
		{"branch", nil, []string{"+committed", "+uncommitted"}, []string{"main only", "untracked"}},
		{"file", &gitstat.FileStat{Path: "sub/b.txt", Status: 'M'}, []string{"+uncommitted"}, []string{"+committed", "main only"}},
		{"untracked", &gitstat.FileStat{Path: "new.txt", Status: '?'}, []string{"+untracked"}, []string{"uncommitted"}},
	} {
		out := run(t, diffCmd(sub, stats.Base, c.file), append(otherGitEnv(t), "GIT_PAGER=cat")...)
		if !strings.Contains(out, "\x1b[") {
			t.Errorf("%s: no color in %q", c.name, out)
		}
		out = regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(out, "")
		for _, w := range c.want {
			if !strings.Contains(out, w) {
				t.Errorf("%s: no %q in\n%s", c.name, w, out)
			}
		}
		for _, n := range c.not {
			if strings.Contains(out, n) {
				t.Errorf("%s: %q in\n%s", c.name, n, out)
			}
		}
	}
}

// TestStageScript answers stageScript's git add -p and git reset -p from
// a subfolder: both hunks go in, then git reset -p takes the first back
// and keeps the second.
func TestStageScript(t *testing.T) {
	gitEnv(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "--initial-branch=main")
	write(t, filepath.Join(dir, "sub", "a.txt"), "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "base")
	write(t, filepath.Join(dir, "sub", "a.txt"), "one\n2\n3\n4\n5\n6\n7\n8\n9\nten\n")
	cmd := exec.Command("sh", "-c", stageScript, "sh", filepath.Join(dir, "sub"))
	cmd.Env = append(os.Environ(), otherGitEnv(t)...)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// why: git add -p reads stdin buffered, so git reset -p's answers go in
	// only once both hunks are staged.
	io.WriteString(in, "y\ny\n")
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(gitIn(t, dir, "diff", "--cached"), "+ten") {
			break
		}
	}
	io.WriteString(in, "y\nn\n")
	in.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	staged := gitIn(t, dir, "diff", "--cached")
	if strings.Contains(staged, "+one") || !strings.Contains(staged, "+ten") {
		t.Errorf("staged:\n%s", staged)
	}
}

// TestPRCmd runs the pull request tab's command against a local origin
// with a stub gh: a branch without an upstream is pushed first, one with
// an upstream is not, and gh gets pr create --fill either way.
func TestPRCmd(t *testing.T) {
	gitEnv(t)
	root := t.TempDir()
	origin, dir, bin := filepath.Join(root, "origin.git"), filepath.Join(root, "work"), filepath.Join(root, "bin")
	gitIn(t, root, "init", "--bare", "--initial-branch=main", origin)
	gitIn(t, root, "clone", origin, dir)
	write(t, filepath.Join(dir, "a.txt"), "one\n")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-m", "base")
	gitIn(t, dir, "push", "origin", "HEAD:main")
	gitIn(t, dir, "checkout", "-b", "feature")
	write(t, filepath.Join(dir, "a.txt"), "two\n")
	gitIn(t, dir, "commit", "-am", "feature")
	log := filepath.Join(root, "gh.log")
	write(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho \"$@\" >> '"+log+"'\necho https://example.invalid/pull/1\n")
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")

	out := run(t, prCmd(dir, ""), append(otherGitEnv(t), path)...)
	if !strings.Contains(out, "$ git push -u origin HEAD") || !strings.Contains(out, "https://example.invalid/pull/1") {
		t.Fatalf("no upstream: printed\n%s", out)
	}
	if up := gitIn(t, dir, "rev-parse", "--abbrev-ref", "@{upstream}"); up != "origin/feature\n" {
		t.Fatalf("upstream after the push = %q", up)
	}
	write(t, filepath.Join(dir, "a.txt"), "three\n")
	gitIn(t, dir, "commit", "-am", "more")
	out = run(t, prCmd(dir, "refs/remotes/origin/main"), append(otherGitEnv(t), path)...)
	if strings.Contains(out, "git push") {
		t.Fatalf("with an upstream: printed\n%s", out)
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "pr create --fill\npr create --fill --base main\n" {
		t.Fatalf("gh called with %q, %v", calls, err)
	}
}

// TestReview: diff_pager and stage_terminal split the open tab, create_pr opens a tab in the
// tab's group, and a blocked action sends nothing.
func TestReview(t *testing.T) {
	old := ghInstalled
	t.Cleanup(func() { ghInstalled = old })
	ghInstalled = func() bool { return true }
	st := model.State{
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", ProjectID: "g", Branch: "feature", Path: "/repo"}},
		Stats:      map[string]model.BranchStats{"w": {Ahead: 1, MergeStatus: model.MergeClean, Base: "refs/remotes/origin/main"}},
	}
	n := nav{workspace: "w"}
	if got, want := n.review(&st, "w", "diff_pager"), (proto.OpenPane{WorkspaceID: "w", Dir: layout.Horizontal, Cmd: diffCmd("/repo", "refs/remotes/origin/main", nil)}); !reflect.DeepEqual(got, want) {
		t.Errorf("diff_pager = %+v, want %+v", got, want)
	}
	if got, want := n.review(&st, "w", "stage_terminal"), (proto.OpenPane{WorkspaceID: "w", Dir: layout.Horizontal, Cmd: []string{"sh", "-c", stageScript, "sh", "/repo"}}); !reflect.DeepEqual(got, want) {
		t.Errorf("stage_terminal = %+v, want %+v", got, want)
	}
	if got, want := n.review(&st, "w", "create_pr"), (fromTab{"w", proto.NewSession{Name: "PR feature", Cwd: "/repo", GroupID: "g", SessionID: "s", Cmd: prCmd("/repo", "")}}); !reflect.DeepEqual(got, want) {
		t.Errorf("create_pr = %+v, want %+v", got, want)
	}
	// A base picked in the review view: the pager diffs from it and the
	// pull request goes into it.
	n.reviewBase = map[string]reviewPick{"w": {Ref: "refs/remotes/origin/feature", Branch: "feature"}}
	if got, want := n.review(&st, "w", "diff_pager"), (proto.OpenPane{WorkspaceID: "w", Dir: layout.Horizontal, Cmd: diffCmd("/repo", "refs/remotes/origin/feature", nil)}); !reflect.DeepEqual(got, want) {
		t.Errorf("diff_pager with a base = %+v, want %+v", got, want)
	}
	if got, want := n.review(&st, "w", "create_pr"), (fromTab{"w", proto.NewSession{Name: "PR feature", Cwd: "/repo", GroupID: "g", SessionID: "s", Cmd: prCmd("/repo", "refs/remotes/origin/feature")}}); !reflect.DeepEqual(got, want) || !strings.HasSuffix(strings.Join(want.ns.Cmd, " "), "--base feature") {
		t.Errorf("create_pr with a base = %+v", got)
	}
	// A pick made on another branch of the tab no longer applies.
	n.reviewBase["w"] = reviewPick{Ref: "refs/heads/feature", Branch: "stacked"}
	if got := n.baseOf(&st, "w"); got != "refs/remotes/origin/main" {
		t.Errorf("baseOf after the branch changed = %q", got)
	}
	if got, ok := n.review(&st, "w", "create_pr").(fromTab); !ok || strings.Contains(strings.Join(got.ns.Cmd, " "), "--base") {
		t.Errorf("create_pr after the branch changed = %+v", got)
	}
	n.reviewBase = nil
	ghInstalled = func() bool { return false }
	if got := n.review(&st, "w", "create_pr"); got != nil {
		t.Errorf("create_pr without gh = %+v", got)
	}
	if got := n.reviewBlocked(&st, "create_pr"); got != "no gh CLI" {
		t.Errorf("palette reason %q", got)
	}
}
