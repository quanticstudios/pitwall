package forge

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// PushAccess asks GitHub only about a GitHub origin, and only gh's
// answer "true" lets a worker open a pull request.
func TestPushAccess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a shell script")
	}
	repo, bin := t.TempDir(), t.TempDir()
	if out, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	answer := filepath.Join(bin, "answer")
	script := "#!/bin/sh\n[ \"$*\" = 'api repos/example/app --jq .permissions.push' ] || { echo \"bad call: $*\" >&2; exit 2; }\n. '" + answer + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ origin, answer, want string }{
		{"", "", ""},
		{"https://example.com/app.git", "", ""},
		{"git@github.com:example/app.git", "echo true", ""},
		{"https://github.com/example/app.git", "echo false", "gh's account cannot push to example/app"},
		{"https://github.com/example/app.git", "printf 'To get started with GitHub CLI, please run:  gh auth login\\nAlternatively, populate GH_TOKEN.\\n' >&2; exit 4", "gh cannot read example/app: To get started"},
	} {
		exec.Command("git", "-C", repo, "remote", "remove", "origin").Run()
		if c.origin != "" {
			if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", c.origin).CombinedOutput(); err != nil {
				t.Fatalf("git remote add: %v: %s", err, out)
			}
		}
		if err := os.WriteFile(answer, []byte(c.answer+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := PushAccess(context.Background(), repo)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.HasPrefix(err.Error(), c.want)) {
			t.Errorf("origin %q, gh %q: %v, want %q", c.origin, c.answer, err, c.want)
		}
	}
	// The daemon may have another account. Use the worker's token and HOME,
	// preserving gh's noninteractive flags and git's repository safeguards.
	if err := os.WriteFile(answer, []byte("[ \"$GH_TOKEN\" = worker ] && [ \"$HOME\" = '"+repo+"' ] && [ \"$GH_PROMPT_DISABLED\" = 1 ] || exit 1\necho true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PushAccessEnv(context.Background(), repo, append(os.Environ(), "GH_TOKEN=worker", "HOME="+repo)); err != nil {
		t.Fatalf("worker credentials: %v", err)
	}
}
