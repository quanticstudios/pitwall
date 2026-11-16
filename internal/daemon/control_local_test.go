//go:build linux

package daemon

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
)

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
