package mobile

import (
	"context"
	"errors"
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

// restartAPI counts Restarts and answers err.
type restartAPI struct {
	chatAPI
	n   int
	err error
}

func (f *restartAPI) Restart(ctx context.Context, pane string) error { f.n++; return f.err }

// Restart goes once until the list shows the tab running, and its error
// shows on the exited band.
func TestRestartTab(t *testing.T) {
	api := &restartAPI{}
	var later func()
	env := chatEnv{Open: true, Online: true, CanRestart: true, Tab: remote.Tab{Item: remote.Item{Pane: "p1"}, Exited: true},
		Do: func(call func(context.Context, chatAPI) error, done func(error)) {
			err := call(context.Background(), api)
			later = func() { done(err) }
		}}
	c := newChat("p1")
	c.restartTab(env)
	c.restartTab(env) // a second tap while the first goes
	later()
	if api.n != 1 || !c.restarting {
		t.Fatalf("restarts %d, restarting %t", api.n, c.restarting)
	}
	c.restarting, api.err = false, errors.New("that pane is still running")
	c.restartTab(env)
	later()
	if c.restarting || c.cmp.note != "That pane is still running" {
		t.Fatalf("after an error: restarting %t, note %q", c.restarting, c.cmp.note)
	}
}

// A folder's path shows ~ for home and keeps its end when it is cut.
func TestShortDir(t *testing.T) {
	for _, tc := range []struct {
		path, home string
		n          int
		want       string
	}{
		{"/home/u/Work/pitwall", "/home/u", 40, "~/Work/pitwall"},
		{"/home/u", "/home/u", 40, "~"},
		{"/home/user2/x", "/home/u", 40, "/home/user2/x"},
		{"/home/u/Work/pitwall/.worktrees/phone-polish", "/home/u", 22, "~/Work/…s/phone-polish"},
		{"/home/u/Work/pitwall/.worktrees/phone-new", "/home/u", 22, "~/Work/…rees/phone-new"},
		{`C:\Users\u\src\app`, `C:\Users\u`, 40, `~\src\app`},
		{"/srv/a", "", 3, "/srv/a"},
	} {
		if got := shortDir(tc.path, tc.home, tc.n); got != tc.want {
			t.Errorf("shortDir(%q, %q, %d) = %q, want %q", tc.path, tc.home, tc.n, got, tc.want)
		}
		if got := []rune(shortDir(tc.path, tc.home, tc.n)); tc.n >= 8 && len(got) > tc.n {
			t.Errorf("shortDir(%q) is %d runes, over %d", tc.path, len(got), tc.n)
		}
	}
}
