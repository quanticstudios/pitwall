package daemon

import (
	"cmp"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// fakeSSH runs the command git asks for on this machine, as host would.
// Host down refuses the connection; host readonly refuses every push, as
// a read-only deploy key does.
const fakeSSH = `#!/bin/sh
while [ $# -gt 0 ]; do
	case "$1" in -o|-p|-l) shift 2 ;; -*) shift ;; *) break ;; esac
done
host=$1; shift
case "$host" in
envonly) [ "$WORKER_KEY" = worker ] || { echo 'wrong worker credentials' >&2; exit 1; } ;;
custom) [ "$CUSTOM_SSH" = 1 ] || { echo 'custom ssh was lost' >&2; exit 1; } ;;
esac
case "$host" in down) echo "ssh: connect to host down port 22: Connection refused" >&2; exit 255 ;; esac
case "$host:$*" in readonly:git-receive-pack*) echo "ERROR: The key you are authenticating with has been marked as read only." >&2; exit 1 ;; esac
exec sh -c "$*"
`

func TestCheckStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent and ssh are shell scripts")
	}
	home, bin := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	for _, k := range []string{"CLAUDE_CONFIG_DIR", "PITWALL_GUARD", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "GIT_SSH_COMMAND", "GIT_SSH"} {
		t.Setenv(k, "")
	}
	for name, script := range map[string]string{"claude": "#!/bin/sh\nexit 0\n", "ssh": fakeSSH} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) {
		t.Helper()
		args = append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %q: %v: %s", args, err, out)
		}
	}
	origin, repo := filepath.Join(home, "origin.git"), filepath.Join(home, "app")
	git(home, "init", "--quiet", "--bare", origin)
	git(home, "init", "--quiet", repo)
	git(repo, "commit", "--quiet", "--allow-empty", "-m", "init")
	git(repo, "remote", "add", "origin", "ok:"+origin)

	empty, hooked := t.TempDir(), hookedDir(t)
	if err := os.WriteFile(filepath.Join(hooked, ".credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		check  proto.HostCheck
		origin string   // the repository's origin host, ok when ""
		want   []string // each problem, "warn: " for a warning
	}{
		{name: "not on PATH", check: proto.HostCheck{Cmd: []string{"codex"}}, want: []string{"codex is not on the daemon's PATH"}},
		{name: "a profile's dir without hooks or login", check: proto.HostCheck{Cmd: []string{"claude"}, Env: []string{"CLAUDE_CONFIG_DIR=" + empty}},
			want: []string{"warn: pitwall's hooks are missing", "warn: no claude login in " + empty}},
		{name: "guarded without hooks", check: proto.HostCheck{Cmd: []string{"claude"}, Env: []string{"CLAUDE_CONFIG_DIR=" + empty, "PITWALL_GUARD=1"}},
			want: []string{"guarded tab not started: no settings.json", "warn: no claude login"}},
		{name: "a guarded shell", check: proto.HostCheck{Cmd: []string{"sh"}, Env: []string{"PITWALL_GUARD=1"}}, want: []string{"guarded tab not started: the guard covers"}},
		{name: "a profile's dir with hooks and login", check: proto.HostCheck{Cmd: []string{"claude"}, Env: []string{"CLAUDE_CONFIG_DIR=" + hooked, "PITWALL_GUARD=1"}}},
		{name: "an API key instead of a login", check: proto.HostCheck{Cmd: []string{"claude"}, Env: []string{"CLAUDE_CONFIG_DIR=" + empty, "ANTHROPIC_API_KEY=k"}},
			want: []string{"warn: pitwall's hooks are missing"}},
		{name: "a repository git can read and push", check: proto.HostCheck{Repo: "~/app"}, want: []string{"warn: push permission not checked"}},
		{name: "worker credentials", check: proto.HostCheck{Repo: "~/app", Env: []string{"WORKER_KEY=worker"}}, origin: "envonly", want: []string{"warn: push permission not checked"}},
		{name: "worker ssh command", check: proto.HostCheck{Repo: "~/app", Env: []string{"GIT_SSH_COMMAND=env CUSTOM_SSH=1 ssh"}}, origin: "custom", want: []string{"warn: push permission not checked"}},
		{name: "a read-only origin", check: proto.HostCheck{Repo: "~/app"}, origin: "readonly", want: []string{"git cannot push to origin: ERROR: The key you are authenticating with has been marked as read only."}},
		{name: "an unreachable origin", check: proto.HostCheck{Repo: "~/app"}, origin: "down", want: []string{"git cannot read origin: ssh: connect to host down"}},
		{name: "no repository", check: proto.HostCheck{Cmd: []string{"sh"}, Repo: "~/nowhere", Clone: "https://example.com/app.git"},
			want: []string{"no git repository at ~/nowhere"}},
		{name: "bare repository is not a work tree", check: proto.HostCheck{Repo: origin}, want: []string{"no git repository at " + origin}},
		{name: "files supplied by a future worktree", check: proto.HostCheck{Repo: "~/app", Cwd: "~/app/.worktrees/new", Cmd: []string{"./claude"}, Env: []string{"CLAUDE_CONFIG_DIR=.claude", "PITWALL_GUARD=1"}}, want: []string{"warn: agent preflight deferred", "warn: push permission not checked"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			git(repo, "remote", "set-url", "origin", cmp.Or(c.origin, "ok")+":"+origin)
			got := CheckStart(context.Background(), c.check)
			if len(got) != len(c.want) {
				t.Fatalf("problems %+v, want %q", got, c.want)
			}
			for i, p := range got {
				what := p.What
				if p.Warn {
					what = "warn: " + what
				}
				if !strings.HasPrefix(what, c.want[i]) {
					t.Errorf("problem %d: %q, want %q", i, what, c.want[i])
				}
				if p.Fix == "" && !strings.HasPrefix(p.What, "guarded tab not started") {
					t.Errorf("problem %q has no fix", p.What)
				}
			}
		})
	}
	// The worker's own git config keeps its ssh transport; the repository's never runs.
	global, marker := filepath.Join(home, "worker.gitconfig"), filepath.Join(home, "repo-ssh-ran")
	if err := os.WriteFile(global, []byte("[core]\n\tsshCommand = env CUSTOM_SSH=1 ssh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(repo, "config", "core.sshCommand", "touch "+marker+"; ssh")
	git(repo, "remote", "set-url", "origin", "custom:"+origin)
	transport := CheckStart(context.Background(), proto.HostCheck{Repo: "~/app", Env: []string{"GIT_CONFIG_GLOBAL=" + global}})
	if len(transport) != 1 || !strings.HasPrefix(transport[0].What, "push permission not checked") {
		t.Fatalf("worker's core.sshCommand: %+v", transport)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's core.sshCommand ran")
	}
	git(repo, "config", "--unset", "core.sshCommand")
	// The clone command is the asking machine's origin, cloned to Repo.
	got := CheckStart(context.Background(), proto.HostCheck{Repo: "~/nowhere", Clone: "https://example.com/app.git"})
	if len(got) != 1 || !strings.HasPrefix(got[0].Fix, "git clone https://example.com/app.git ~/nowhere") {
		t.Fatalf("missing repository: %+v", got)
	}
}

func TestCheckStartDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake agent is a shell script")
	}
	home, launch := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PITWALL_GUARD", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	// Hooks exist only relative to the requested launch directory.
	hooked := hookedDir(t)
	data, err := os.ReadFile(filepath.Join(hooked, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(launch, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for file, content := range map[string][]byte{"settings.json": data, ".credentials.json": []byte("{}")} {
		if err := os.WriteFile(filepath.Join(configDir, file), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(launch, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, cwd := range []string{launch, "~/../" + filepath.Base(launch)} {
		got := CheckStart(context.Background(), proto.HostCheck{Cwd: cwd, Cmd: []string{"./claude"}, Env: []string{"CLAUDE_CONFIG_DIR=config", "PITWALL_GUARD=1"}})
		if len(got) != 0 {
			t.Fatalf("launch directory %s: %+v", cwd, got)
		}
	}
}
