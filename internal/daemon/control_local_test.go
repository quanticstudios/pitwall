//go:build linux

package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// The JSON check must not forward credential diagnostics from git's transport.
func TestControlHostCheckSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := gitRepo(t)
	gitIn(t, repo, "remote", "add", "origin", "box:repo")
	ssh := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\necho 'fixture-credential-value' >&2\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", ssh)
	_, _, sock := controlDaemon(t, func(*Options) {})
	c := dialJSON(t, sock)
	c.hello(t)
	r := c.call(t, fmt.Sprintf(`{"type":"hostCheck","id":"1","repo":%q}`, repo))
	if r["ok"] != true || len(dataOf(r)["problems"].([]any)) == 0 || strings.Contains(fmt.Sprint(r), "fixture-credential-value") {
		t.Fatalf("hostCheck exposed a credential diagnostic: %v", r)
	}
	// Fix strings also contain caller-supplied clone URLs.
	r = c.call(t, fmt.Sprintf(`{"type":"hostCheck","id":"2","repo":%q,"clone":"https://user:fixture-password@example.com/repo"}`, filepath.Join(repo, "absent")))
	if strings.Contains(fmt.Sprint(r), "fixture-password") {
		t.Fatalf("hostCheck exposed a clone credential: %v", r)
	}
}

// Both checks after ls-remote must omit raw diagnostics too.
func TestControlHostCheckWriteSecrets(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := gitRepo(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, repo, "init", "--bare", origin)
	gitIn(t, repo, "remote", "add", "origin", "box:"+origin)
	bin := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHECK_REAL_GIT", git)
	for name, script := range map[string]string{
		"ssh": "#!/bin/sh\ncase \"$CHECK_FAIL:$*\" in push:*git-receive-pack*) echo fixture-credential-value >&2; exit 1;; esac\n" + fakeSSH,
		"git": "#!/bin/sh\ncase \"$CHECK_FAIL:$*\" in gh:*'config --get remote.origin.url') echo https://github.com/example/app.git; exit 0;; esac\nexec \"$CHECK_REAL_GIT\" \"$@\"\n",
		"gh":  "#!/bin/sh\necho fixture-credential-value >&2\nexit 1\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_SSH_COMMAND", filepath.Join(bin, "ssh"))
	_, _, sock := controlDaemon(t, func(*Options) {})
	c := dialJSON(t, sock)
	c.hello(t)
	for _, mode := range []string{"push", "gh"} {
		t.Setenv("CHECK_FAIL", mode)
		r := c.call(t, fmt.Sprintf(`{"type":"hostCheck","id":"1","repo":%q}`, repo))
		if r["ok"] != true || strings.Contains(fmt.Sprint(r), "fixture-credential-value") || !strings.Contains(fmt.Sprint(r), map[string]string{"push": "git cannot push to origin", "gh": "gh cannot verify push access"}[mode]) {
			t.Fatalf("hostCheck %s diagnostic: %v", mode, r)
		}
	}
}

// Even git's preliminary config reads must finish under the check deadline.
func TestControlHostCheckDeadline(t *testing.T) {
	limit := hostCheckTimeout
	t.Cleanup(func() { hostCheckTimeout = limit })
	hostCheckTimeout = 50 * time.Millisecond
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nsleep 2 &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := t.Context() // no caller deadline: the check supplies its own
	start := time.Now()
	problems := (ctlHub{}).CheckStart(ctx, proto.HostCheck{Repo: t.TempDir()})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("hostCheck took %s after its deadline: %+v", elapsed, problems)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].What, context.DeadlineExceeded.Error()) {
		t.Fatalf("hostCheck did not report its deadline: %+v", problems)
	}
}

// Guard and version probes share the whole check's deadline and cancel
// the fixture's child process too.
func TestControlHostCheckProbeDeadline(t *testing.T) {
	limit := hostCheckTimeout
	t.Cleanup(func() { hostCheckTimeout = limit })
	hostCheckTimeout = 50 * time.Millisecond
	bin := t.TempDir()
	slow := filepath.Join(bin, "slow")
	for _, name := range []string{"slow", "claude", "opencode"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nsleep 2 &\nwait\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(bin, "version-checked")
	t.Setenv("CHECK_VERSION_MARKER", marker)
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!/bin/sh\n: > \"$CHECK_VERSION_MARKER\"\nsleep 2 &\nwait\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	denies := filepath.Join(bin, "denies")
	if err := os.WriteFile(denies, []byte("#!/bin/sh\nexit 2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"hooks":`+string(agent.ClaudeHooks(slow))+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	opencode := t.TempDir()
	if err := os.Mkdir(filepath.Join(opencode, "plugins"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opencode, "plugins", "pitwall.js"), agent.OpenCodePlugin(denies), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_CONFIG_DIR", opencode)
	for _, check := range []proto.HostCheck{
		{Cmd: []string{"claude"}},
		{Cmd: []string{"claude"}, Env: []string{"PITWALL_GUARD=1"}},
		{Cmd: []string{"opencode"}, Env: []string{"PITWALL_GUARD=1"}},
	} {
		start := time.Now()
		problems := (ctlHub{}).CheckStart(t.Context(), check)
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("hostCheck %s took %s after its deadline: %+v", check.Cmd[0], elapsed, problems)
		}
		if len(problems) != 1 || !strings.Contains(problems[0].What, context.DeadlineExceeded.Error()) {
			t.Fatalf("hostCheck %s did not report its deadline: %+v", check.Cmd[0], problems)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the OpenCode version probe was not reached: %v", err)
	}
}

// A refusal removes a real worktree and its fresh branch, not only the tab.
func TestControlWorktreeRefusalCleanup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	repo := gitRepo(t)
	d, _, sock := controlDaemon(t, func(o *Options) {
		o.AddWorktree, o.RemoveWorktree = gitstat.AddWorktree, gitstat.RemoveWorktree
		o.Fleet = func() config.Settings { return config.Settings{GuardWorkers: true} }
	})
	c := dialJSON(t, sock)
	c.hello(t)
	before := len(d.state().Workspaces)
	r := c.call(t, fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"refused","parentPane":"p1","cmd":["claude"]}`, repo))
	if r["code"] != "refused" || len(d.state().Workspaces) != before {
		t.Fatalf("guard refusal left a tab: %v", r)
	}
	if _, err := os.Stat(filepath.Join(repo, ".worktrees", "refused")); !os.IsNotExist(err) {
		t.Fatalf("guard refusal left a worktree: %v", err)
	}
	if refs := gitIn(t, repo, "branch", "--list", "refused"); refs != "" {
		t.Fatalf("guard refusal left a branch: %s", refs)
	}
}

// gitRepo is a repository with one commit in a folder named repo, which
// the fakes' RepoRoot takes for a git repository.
func gitRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	for _, args := range [][]string{{"init", "--initial-branch=main", repo}, {"-C", repo, "commit", "--allow-empty", "-m", "init"}} {
		args = append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %q: %v: %s", args, err, out)
		}
	}
	return repo
}

// TestControlLocalOnDaemon: worktreeNew, the queue commands, hostCheck
// and adopt change the daemon as pitwall worktree new, queue and adopt
// do, with the guard on the worktree's agent.
func TestControlLocalOnDaemon(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // no hooks
	settings := config.Settings{GuardWorkers: true}
	d, _, sock := controlDaemon(t, func(o *Options) { o.Fleet = func() config.Settings { return settings } })
	repo := gitRepo(t)
	c := dialJSON(t, sock)
	c.hello(t)
	guarded := func(pane string) bool {
		st := d.state()
		i := slices.IndexFunc(st.Panes, func(p model.Pane) bool { return p.ID == pane })
		return i >= 0 && slices.ContainsFunc(st.Panes[i].Env, guard.EnvOn)
	}

	tabs := len(d.state().Workspaces)
	if r := c.call(t, fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"g","cmd":["claude"],"parentPane":"p1"}`, repo)); r["code"] != "refused" {
		t.Fatalf("a worker's worktree without hooks: %v", r)
	}
	if n := len(d.state().Workspaces); n != tabs {
		t.Fatalf("a refused worktree left %d tabs, want %d", n, tabs)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", hookedDir(t))
	r := c.call(t, fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"fix","cmd":["claude"],"parentPane":"p1"}`, filepath.Join(repo, ".")))
	tab, _ := dataOf(r)["tab"].(string)
	st := d.state()
	w := st.Workspace(tab)
	if w == nil || w.Parent != "w1" || w.Path != filepath.Join(repo, ".worktrees", "fix") || !guarded(fmt.Sprint(dataOf(r)["pane"])) {
		t.Fatalf("worktreeNew %v: %+v", r, w)
	}
	r = c.call(t, fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"plain"}`, repo))
	st = d.state()
	if w := st.Workspace(fmt.Sprint(dataOf(r)["tab"])); w == nil || w.Parent != "" || w.ProjectID == "" {
		t.Fatalf("worktreeNew from outside a pane %v: %+v", r, w)
	}

	at := time.Now().Add(2 * time.Hour).Format("15:04") // stays queued
	if r := c.call(t, fmt.Sprintf(`{"type":"queueAdd","id":"1","dir":%q,"cmd":["sleep","9"],"at":%q}`, t.TempDir(), at)); r["ok"] != true {
		t.Fatalf("queueAdd %v", r)
	}
	r = c.call(t, `{"type":"queueList","id":"1"}`)
	rows, _ := dataOf(r)["tasks"].([]any)
	queued := control.Queued(d.state(), "s") // what pitwall queue ls lists
	if len(rows) != 1 || len(queued) != 1 || rows[0].(map[string]any)["id"] != queued[0].ID || rows[0].(map[string]any)["title"] != "9" {
		t.Fatalf("queueList %v, queue ls %+v", r, queued)
	}
	if r := c.call(t, `{"type":"queueDrop","id":"1","task":"1"}`); r["ok"] != true || len(d.state().Tasks) != 0 {
		t.Fatalf("queueDrop %v", r)
	}

	r = c.call(t, `{"type":"hostCheck","id":"1","cmd":["pitwall-test-no-such-program"]}`)
	if p, _ := dataOf(r)["problems"].([]any); len(p) == 0 || !strings.Contains(fmt.Sprint(p[0]), "not on the daemon's PATH") {
		t.Fatalf("hostCheck %v", r)
	}

	r = c.call(t, fmt.Sprintf(`{"type":"newTab","id":"1","dir":%q,"parentPane":"p2","startId":"00000000000000c1"}`, t.TempDir()))
	if r["ok"] != true {
		t.Fatalf("newTab %v", r)
	}
	if r := c.call(t, `{"type":"killTab","id":"1","tab":"@w2"}`); r["ok"] != true {
		t.Fatalf("killTab %v", r)
	}
	if r := c.call(t, `{"type":"adopt","id":"1","tab":"#1","ids":["@r9"]}`); r["code"] != "remote" {
		t.Fatalf("adopt of a worker on box: %v", r)
	}
	if r := c.call(t, `{"type":"adopt","id":"1","tab":"#1","ids":["@00000000000000c1"]}`); r["ok"] != true || !has(d, "00000000000000c1", func(w model.Workspace) bool { return w.Parent == "w1" }) {
		t.Fatalf("adopt %v", r)
	}
}

// TestControlLocalTwoDaemons: two daemons, as this machine and box, each
// answering on its own socket. This machine changes nothing for its
// worker on box, and starts nothing there for a lane on box; box's own
// socket adopts box's orphans and checks a start there.
func TestControlLocalTwoDaemons(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	settings := config.Settings{
		Presets: []config.AgentPreset{{Name: "safe", Agent: "claude"}},
		Fleet:   config.FleetSettings{On: true, Lanes: []config.LaneSettings{{Name: "small", Preset: "safe", Host: "box"}}},
	}
	here, _, hereSock := controlDaemon(t, func(o *Options) { o.Fleet = func() config.Settings { return settings } })
	box, _, boxSock := controlDaemon(t, func(*Options) {})
	h := dialJSON(t, hereSock)
	h.hello(t)
	b := dialJSON(t, boxSock)
	b.hello(t)

	if r := h.call(t, `{"type":"killTab","id":"1","tab":"@w1"}`); r["ok"] != true {
		t.Fatalf("killTab %v", r)
	}
	before := here.state()
	if i := slices.IndexFunc(before.Sessions[0].Workers, func(w model.RemoteWorker) bool { return w.ID == "r9" && w.Lost == "" }); i < 0 {
		t.Fatalf("the record of r9 on box is gone: %+v", before.Sessions[0].Workers)
	}
	for _, req := range []string{
		`{"type":"adopt","id":"1","tab":"#1","all":true}`,
		`{"type":"adopt","id":"1","tab":"#1","ids":["r9"]}`,
		fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"x","lane":"small","cmd":["go"]}`, gitRepo(t)),
	} {
		if r := h.call(t, req); r["code"] != "remote" || !strings.Contains(fmt.Sprint(r["message"]), "box") {
			t.Errorf("%s: %v", req, r)
		}
	}
	after := here.state()
	if len(after.Workspaces) != len(before.Workspaces) || fmt.Sprint(after.Sessions[0].Workers) != fmt.Sprint(before.Sessions[0].Workers) {
		t.Fatalf("a remote answer changed this machine: %+v, then %+v", before, after)
	}

	r := b.call(t, fmt.Sprintf(`{"type":"newTab","id":"1","dir":%q,"parentPane":"p2","startId":"00000000000000d1"}`, t.TempDir()))
	if r["ok"] != true {
		t.Fatalf("newTab on box %v", r)
	}
	if r := b.call(t, `{"type":"killTab","id":"1","tab":"@w2"}`); r["ok"] != true {
		t.Fatalf("killTab on box %v", r)
	}
	if r := b.call(t, `{"type":"adopt","id":"1","tab":"@w1","all":true}`); fmt.Sprint(dataOf(r)["adopted"]) != "[00000000000000d1]" || !has(box, "00000000000000d1", func(w model.Workspace) bool { return w.Parent == "w1" }) {
		t.Fatalf("adopt all on box %v", r)
	}
	if r := b.call(t, `{"type":"hostCheck","id":"1","cmd":["sh"]}`); r["ok"] != true {
		t.Fatalf("hostCheck on box %v", r)
	}
}
