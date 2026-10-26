package guard

import (
	"encoding/json"
	"testing"
)

func input(t *testing.T, cmd any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]any{"command": cmd})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCheck(t *testing.T) {
	for _, c := range []struct {
		cmd   any
		extra []string
		want  string
	}{
		{cmd: "git push", want: "git push"},
		{cmd: "git push origin main --force", want: "git push origin main --force"},
		{cmd: "git -C ../repo push", want: "git -C ../repo push"},
		{cmd: "gh pr merge 3 --squash", want: "gh pr merge 3 --squash"},
		{cmd: "gh -R o/r pr merge 3", want: "gh -R o/r pr merge 3"},
		{cmd: "terraform -chdir=infra apply", want: "terraform -chdir=infra apply"},
		{cmd: "kubectl --context prod delete pod x", want: "kubectl --context prod delete pod x"},
		{cmd: "go test ./... && git push origin x", want: "git push origin x"},
		{cmd: "git status; kubectl  delete pod x", want: "kubectl delete pod x"},
		{cmd: "FOO=1 BAR=2 tofu apply", want: "tofu apply"},
		{cmd: "echo hi | gh pr merge 3", want: "gh pr merge 3"},
		{cmd: "make build\ngit push", want: "git push"},
		{cmd: "true || git push", want: "git push"},
		{cmd: "sleep 1 & git push", want: "git push"},
		// Codex can send an argv; `bash -lc <script>` is judged by its script.
		{cmd: []string{"bash", "-lc", "cd wt && git push"}, want: "git push"},
		{cmd: []string{"git", "push"}, want: "git push"},

		{cmd: "git stash push -m wip", want: ""},
		{cmd: "git fetch && git rebase origin/main", want: ""},
		{cmd: "gh pr view 3", want: ""},
		{cmd: "terraform plan", want: ""},
		{cmd: "echo git push", want: ""},

		// Additions deny more; they never lift the built-in list.
		{cmd: "npm publish --tag next", extra: []string{"npm publish *"}, want: "npm publish --tag next"},
		{cmd: "git push", extra: []string{"npm publish *"}, want: "git push"},

		// Known bypasses, documented in docs/usage.md: no shell parser.
		{cmd: "echo $(git push)", want: ""},
		{cmd: "eval 'git push'", want: ""},
		{cmd: "sh -c 'git push'", want: ""},
		{cmd: "./push.sh", want: ""},
	} {
		if got := Check(c.extra, input(t, c.cmd)); got != c.want {
			t.Errorf("%q extra %q: Check = %q, want %q", c.cmd, c.extra, got, c.want)
		}
	}
	for _, raw := range []string{`{}`, `{"command":1}`, `not json`, `{"file_path":"x"}`} {
		if got := Check(nil, json.RawMessage(raw)); got != "" {
			t.Errorf("%s: Check = %q", raw, got)
		}
	}
	if got := Check(nil, json.RawMessage(`{"cmd":"git push"}`)); got != "git push" {
		t.Errorf("cmd key: Check = %q", got)
	}
}

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		pattern, value string
		want           bool
	}{
		{"git push *", "git push", true},
		{"git push *", "git push origin", true},
		{"git push *", "git pushx", false},
		{"git ?ush", "git push", true},
		{"rm -rf (x)", "rm -rf (x)", true},
		{"rm -rf (x)", "rm -rf x", false},
	} {
		if got := Match(c.pattern, c.value); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.pattern, c.value, got)
		}
	}
}
