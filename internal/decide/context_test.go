package decide

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	git(t, root, "init", "-q")
	return root
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func paths(es []Excerpt) []string {
	out := []string{}
	for _, e := range es {
		out = append(out, e.Path)
	}
	return out
}

// TestRetrieveVersionsAreNotFiles: version numbers and URL hosts are not
// required files; a named file that is missing fails the turn.
func TestRetrieveVersionsAreNotFiles(t *testing.T) {
	root := gitRepo(t)
	write(t, filepath.Join(root, "README.md"), "# Models\n")
	ctx := context.Background()
	c, err := RetrieveContext(ctx, root, "The list shows outdated Fable, there is 5.1 now. Update README.md for v0.0.2 and 0.0.3-beta.1.")
	if err != nil || !slices.Equal(paths(c.Mandatory), []string{"README.md"}) {
		t.Fatalf("mandatory %v, %v", paths(c.Mandatory), err)
	}
	write(t, filepath.Join(root, "5.1"), "A file with a numeric name\n")
	if c, err := RetrieveContext(ctx, root, "Inspect ./5.1"); err != nil || !slices.Equal(paths(c.Mandatory), []string{"5.1"}) {
		t.Fatalf("./5.1: %v, %v", paths(c.Mandatory), err)
	}
	if _, err := RetrieveContext(ctx, root, "Inspect missing.ts"); err == nil || err.Error() != `Required context file "missing.ts" is unavailable.` {
		t.Fatalf("missing.ts: %v", err)
	}
	if c, err := RetrieveContext(ctx, root, "https://github.com/Graphify-Labs/graphify run graphify here so that we can speed up our process"); err != nil || len(c.Mandatory) != 0 {
		t.Fatalf("URL: %v, %v", paths(c.Mandatory), err)
	}
}

// TestRetrieveSafeFiles: instruction files and named files are mandatory;
// secrets, binaries, ignored files, odd names and a symlink out of the
// workspace never are; a changed or swapped file fails validation; a big
// named file gives the lines around the one named.
func TestRetrieveSafeFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs symlinks and tabs in file names")
	}
	root := gitRepo(t)
	outside := t.TempDir()
	write(t, filepath.Join(root, "AGENTS.md"), "Use the local conventions.\n")
	write(t, filepath.Join(root, "src", "CLAUDE.md"), "Test the parser.\n")
	write(t, filepath.Join(root, "src", "parser.ts"), "export const parseTask = () => true\n")
	write(t, filepath.Join(root, ".env"), "API_KEY=hidden\n")
	write(t, filepath.Join(root, "ignored.ts"), "export const ignored = true\n")
	write(t, filepath.Join(root, ".gitignore"), "ignored*\n")
	if err := os.WriteFile(filepath.Join(root, "binary.dat"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "ignored\ttracked.ts"), "export const ignoredName = true\n")
	for i := range 520 {
		write(t, filepath.Join(root, fmt.Sprintf("candidate-%03d.ts", i)), "export const candidate = true\n")
	}
	write(t, filepath.Join(outside, "outside.ts"), "export const outside = true\n")
	if err := os.Symlink(filepath.Join(outside, "outside.ts"), filepath.Join(root, "src", "escape.ts")); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "AGENTS.md", "src", ".env", "binary.dat", ".gitignore", "--force", "ignored.ts", "ignored\ttracked.ts")
	git(t, root, "commit", "-q", "-m", "fixture")

	ctx := context.Background()
	c, err := RetrieveContext(ctx, root, "Fix src/parser.ts and use parser behavior.")
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(c.Mandatory); !slices.Equal(got, []string{"AGENTS.md", "src/CLAUDE.md", "src/parser.ts"}) {
		t.Fatalf("mandatory %v", got)
	}
	for _, p := range []string{".env", "binary.dat", "ignored.ts", "src/escape.ts", "ignored\ttracked.ts"} {
		if slices.Contains(paths(c.Candidates), p) {
			t.Errorf("candidate %q", p)
		}
	}
	if len(c.Candidates) != 16 {
		t.Errorf("%d candidates, want 16", len(c.Candidates))
	}
	if m := c.Mandatory[2]; len(m.Revision) != 64 || m.StartLine != 1 || m.EndLine != 1 {
		t.Errorf("parser excerpt %+v", m)
	}

	write(t, filepath.Join(root, "src", "parser.ts"), "export const parseTask = () => false\n")
	changed, err := RetrieveContext(ctx, root, "Fix src/parser.ts")
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(changed.Mandatory, func(e Excerpt) bool { return e.Path == "src/parser.ts" }); i < 0 || changed.Mandatory[i].Revision == c.Mandatory[2].Revision {
		t.Error("the revision did not change with the file")
	}
	if err := ValidateRevisions(ctx, root, c.Mandatory); err == nil || !strings.Contains(err.Error(), "changed after retrieval") {
		t.Errorf("changed file: %v", err)
	}
	os.Remove(filepath.Join(root, "src", "parser.ts"))
	if err := os.Symlink(filepath.Join(outside, "outside.ts"), filepath.Join(root, "src", "parser.ts")); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRevisions(ctx, root, changed.Mandatory); err == nil || !strings.Contains(err.Error(), "is unavailable") {
		t.Errorf("swapped file: %v", err)
	}

	var big strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&big, "export const value%d = %d\n", i+1, i+1)
	}
	write(t, filepath.Join(root, "src", "large.ts"), big.String())
	large, err := RetrieveContext(ctx, root, "Fix src/large.ts:1500")
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(large.Mandatory, func(e Excerpt) bool { return e.Path == "src/large.ts" })
	if i < 0 || large.Mandatory[i].StartLine != 1498 || large.Mandatory[i].EndLine < 1500 {
		t.Fatalf("large excerpt %v", large.Mandatory)
	}
	if !slices.ContainsFunc(large.Omitted, func(o Omission) bool {
		return o.Path == "src/large.ts" && strings.Contains(o.Reason, "full file exceeds the excerpt budget")
	}) {
		t.Errorf("omitted %v", large.Omitted)
	}
}

// TestRetrieveNotGit: outside Git only the root's instruction files and
// named files inside the folder count, and a cancelled context fails.
func TestRetrieveNotGit(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write(t, filepath.Join(root, "AGENTS.md"), "Stay in the workspace.\n")
	write(t, filepath.Join(outside, "secret.ts"), "export const secret = true\n")
	rel, _ := filepath.Rel(root, filepath.Join(outside, "secret.ts"))
	c, err := RetrieveContext(context.Background(), root, "Inspect "+filepath.ToSlash(rel))
	if err != nil || !slices.Equal(paths(c.Mandatory), []string{"AGENTS.md"}) || len(c.Candidates) != 0 {
		t.Fatalf("%v %v, %v", paths(c.Mandatory), paths(c.Candidates), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RetrieveContext(ctx, root, "Inspect AGENTS.md"); !errors.Is(err, errRetrievalCancelled) {
		t.Errorf("cancelled: %v", err)
	}
}
