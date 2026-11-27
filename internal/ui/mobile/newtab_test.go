package mobile

import (
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/remote"
)

func TestNewFormRequest(t *testing.T) {
	opts := remote.NewOptions{
		Agents:  []remote.Agent{{ID: "claude", Name: "Claude Code"}, {ID: remote.Terminal, Name: "Terminal"}},
		Folders: []remote.Folder{{Path: "/src/shop", Name: "shop", Git: true}, {Path: "/tmp/notes", Name: "notes"}},
	}
	form := func() *newForm {
		f := &newForm{session: "main"}
		f.setOptions(opts)
		return f
	}

	f := form()
	f.brief.SetText("  fix the race  ")
	if r, err := f.request(); err != nil || r.Session != "main" || r.Dir != "/src/shop" || r.Agent != "claude" || r.Brief != "fix the race" || r.Worktree {
		t.Fatalf("plain: %+v %v", r, err)
	}

	f.worktree = true
	if _, err := f.request(); err == nil || !strings.Contains(err.Error(), "branch") {
		t.Fatalf("worktree without a branch: %v", err)
	}
	f.branch.SetText(" fix/race ")
	if r, err := f.request(); err != nil || !r.Worktree || r.Branch != "fix/race" {
		t.Fatalf("worktree: %+v %v", r, err)
	}

	f.folder = 1 // not a repo: no worktree, whatever was picked
	if !f.worktree || f.canWorktree() {
		t.Fatal("notes takes a worktree")
	}
	if r, err := f.request(); err != nil || r.Worktree || r.Branch != "" || r.Dir != "/tmp/notes" {
		t.Fatalf("worktree on a plain folder: %+v %v", r, err)
	}

	f.agent = 1 // a terminal takes no first message
	if r, err := f.request(); err != nil || r.Agent != remote.Terminal || r.Brief != "" || !f.shell() {
		t.Fatalf("terminal: %+v %v", r, err)
	}

	f = form()
	f.folder = len(opts.Folders)
	if _, err := f.request(); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("empty typed path: %v", err)
	}
	f.path.SetText(" ~/elsewhere ")
	if r, err := f.request(); err != nil || r.Dir != "~/elsewhere" || !f.canWorktree() {
		t.Fatalf("typed path: %+v %v", r, err)
	}

	f = &newForm{}
	f.setOptions(remote.NewOptions{})
	if !f.typed() {
		t.Fatal("a machine with no folders does not ask for a path")
	}
	f.path.SetText("/x")
	if _, err := f.request(); err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("no agents: %v", err)
	}
}

func TestTabActions(t *testing.T) {
	env := chatEnv{Open: true, Online: true, CanClose: true, CanRestart: true}
	if r, c := tabActions(env); r || !c {
		t.Errorf("running tab: restart %t close %t", r, c)
	}
	env.Tab.Exited = true
	if r, c := tabActions(env); !r || !c {
		t.Errorf("exited tab: restart %t close %t", r, c)
	}
	env.CanRestart, env.CanClose = false, false // an older daemon
	if r, c := tabActions(env); r || c {
		t.Errorf("older daemon: restart %t close %t", r, c)
	}
	env = chatEnv{Open: true, CanClose: true, CanRestart: true}
	if r, c := tabActions(env); r || c {
		t.Errorf("offline: restart %t close %t", r, c)
	}
}
