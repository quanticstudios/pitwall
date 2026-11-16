package web

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// askLocal sends req and checks its result code and the requests the
// daemon got, in order; want nil for none.
func askLocal(t *testing.T, f *fakeControl, c *lineClient, req, code string, want ...any) map[string]any {
	t.Helper()
	f.edit(func(f *fakeControl) { f.reqs = nil })
	r := ask(t, c, req)
	if got, _ := r["code"].(string); got != code {
		t.Errorf("%s: %v, want code %q", req, r, code)
	}
	f.edit(func(f *fakeControl) {
		if len(want) == 0 && len(f.reqs) == 0 {
			return
		}
		if !reflect.DeepEqual(f.reqs, want) {
			t.Errorf("%s: asked %#v, want %#v", req, f.reqs, want)
		}
	})
	return r
}

// TestControlQueue: queueList numbers a session's tasks as pitwall queue
// ls does, queueDrop drops the task that number names there, as queue rm
// does (cmd/pitwall TestCLIQueue's state and answers), and queueAdd asks
// for the task queue add asks for.
func TestControlQueue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixtures use Unix paths; the daemon serves control on Linux only")
	}
	f, c := controlServer(t)
	f.edit(func(f *fakeControl) {
		f.settings.Presets = []config.AgentPreset{{Name: "safe", Agent: "claude", Env: []string{"A=1"}, Guard: true}}
	})
	f.set(func(st *model.State) {
		st.Projects = []model.Project{{ID: "g", SessionID: "m1", Name: "agents"}}
		st.Tasks = []model.Task{
			{ID: "t1", SessionID: "m1", GroupID: "g", Dir: "/work/a", Worktree: "flaky", Cmd: []string{"/bin/codex", "fix the flaky test\nthen push"}, Env: []string{"SECRET=1"}},
			{ID: "t2", SessionID: "s1", Dir: "/work/z", Cmd: []string{"/bin/claude", "elsewhere"}},
			{ID: "t3", SessionID: "m1", Dir: "/work/c", Cmd: []string{"/bin/pi"}},
		}
	})
	r := askLocal(t, f, c, `{"type":"queueList","id":"1","session":"main"}`, "")
	var got []string
	for _, row := range data(r)["tasks"].([]any) {
		m := row.(map[string]any)
		got = append(got, fmt.Sprint(m["n"], " ", m["id"], " ", m["title"], " ", m["dir"], " ", m["worktree"], " ", m["group"], " ", m["nextStart"]))
	}
	if want := "1 t1 fix the flaky test /work/a flaky agents <nil>|2 t3 pi /work/c   <nil>"; strings.Join(got, "|") != want {
		t.Errorf("queueList %q, want %q", strings.Join(got, "|"), want)
	}
	askLocal(t, f, c, `{"type":"queueDrop","id":"1","session":"main","task":"2"}`, "", proto.DropTask{ID: "t3"})
	askLocal(t, f, c, `{"type":"queueDrop","id":"1","session":"main","task":"#1"}`, "", proto.DropTask{ID: "t1"})
	askLocal(t, f, c, `{"type":"queueDrop","id":"1","session":"main","task":"3"}`, "not-found")
	askLocal(t, f, c, `{"type":"queueDrop","id":"1","task":"1"}`, "invalid") // two sessions
	askLocal(t, f, c, `{"type":"queueDrop","id":"1","task":"t2"}`, "", proto.DropTask{ID: "t2"})

	askLocal(t, f, c, `{"type":"queueAdd","id":"1","session":"main","dir":"/src","cmd":["claude","fix it"]}`, "",
		proto.NewTask{Task: model.Task{SessionID: "m1", Dir: "/src", Cmd: []string{"claude", "fix it"}}, Queue: true})
	askLocal(t, f, c, `{"type":"queueAdd","id":"1","session":"side","dir":"/src","preset":"safe","cmd":["go"],"worktree":"night"}`, "",
		proto.NewTask{Task: model.Task{SessionID: "s1", Dir: "/src", Cmd: []string{"claude", "go"}, Env: []string{"A=1", "PITWALL_GUARD=1"}, Worktree: "night", FreshWorktree: true}, Queue: true})
	for _, req := range []string{
		`{"type":"queueAdd","id":"1","dir":"src","cmd":["claude"]}`,
		`{"type":"queueAdd","id":"1","dir":"/src"}`,
		`{"type":"queueAdd","id":"1","dir":"/src","cmd":["claude"],"at":"25:00"}`,
		`{"type":"queueAdd","id":"1","dir":"/src","cmd":["claude"],"guard":true}`,
		`{"type":"queueAdd","id":"1","dir":"/src","cmd":["claude"],"env":["A=1"]}`,
	} {
		askLocal(t, f, c, req, "invalid")
	}
}

// TestControlHostCheck: hostCheck asks the daemon's own check with the
// preset's variables and the guard, as pitwall new --host asks a host.
func TestControlHostCheck(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixtures use Unix paths; the daemon serves control on Linux only")
	}
	f, c := controlServer(t)
	r := askLocal(t, f, c, `{"type":"hostCheck","id":"1","cmd":["missing"],"guard":true,"dir":"~/src","repo":"~/src","clone":"git@example.com:o/r.git"}`, "",
		proto.HostCheck{Cmd: []string{"missing"}, Env: []string{"PITWALL_GUARD=1"}, Repo: "~/src", Clone: "git@example.com:o/r.git", Cwd: "~/src"})
	if p := data(r)["problems"].([]any); len(p) != 1 || p[0].(map[string]any)["what"] != "missing is not on the daemon's PATH" {
		t.Errorf("hostCheck %v", r)
	}
	if r := askLocal(t, f, c, `{"type":"hostCheck","id":"1","cmd":["sh"]}`, "", proto.HostCheck{Cmd: []string{"sh"}}); len(data(r)["problems"].([]any)) != 0 {
		t.Errorf("hostCheck %v", r)
	}
	for _, req := range []string{
		`{"type":"hostCheck","id":"1","cmd":["sh"],"dir":"src"}`,
		`{"type":"hostCheck","id":"1"}`,
		`{"type":"hostCheck","id":"1","guard":true,"repo":"/src"}`,
	} {
		askLocal(t, f, c, req, "invalid")
	}
}

// TestControlAdopt: adopt reparents this machine's orphans as pitwall
// adopt does, and changes nothing for a worker on another host.
func TestControlAdopt(t *testing.T) {
	f, c := controlServer(t)
	f.set(func(st *model.State) {
		st.Workspaces[0].Parent = "gone"   // build, the new orchestrator, is an orphan too
		st.Workspaces[1].Parent = "gone"   // tests
		st.Workspaces[2].Parent = "absent" // docs, in session side
	})
	askLocal(t, f, c, `{"type":"adopt","id":"1","session":"main","tab":"#1","ids":["@w2"]}`, "", proto.Reparent{WorkspaceID: "w2", ParentTab: "w1"})
	askLocal(t, f, c, `{"type":"adopt","id":"1","session":"main","tab":"#1","ids":["w1"]}`, "invalid")
	askLocal(t, f, c, `{"type":"adopt","id":"1","session":"main","tab":"#1","ids":["w3"]}`, "not-found")
	askLocal(t, f, c, `{"type":"adopt","id":"1","session":"main","tab":"#1","ids":["@r9"]}`, "remote")
	askLocal(t, f, c, `{"type":"adopt","id":"1","session":"main","tab":"#1"}`, "invalid")
	askLocal(t, f, c, `{"type":"adopt","id":"1","session":"main","tab":"#1","ids":["w2"],"all":true}`, "invalid")
	askLocal(t, f, c, `{"type":"adopt","id":"1","tab":"@w3","all":true}`, "not-found") // nothing else in side
	// all skips the tab itself.
	r := askLocal(t, f, c, `{"type":"adopt","id":"1","session":"main","tab":"#1","all":true}`, "", proto.Reparent{WorkspaceID: "w2", ParentTab: "w1"})
	if fmt.Sprint(data(r)["adopted"]) != "[w2]" {
		t.Errorf("adopt all %v", r)
	}
	// A worker on box whose parent is gone: all changes nothing.
	f.set(func(st *model.State) { st.Sessions[0].Workers[0].Parent = "gone" })
	if r := askLocal(t, f, c, `{"type":"adopt","id":"1","session":"main","tab":"#1","all":true}`, "remote"); !strings.Contains(fmt.Sprint(r["message"]), "box") {
		t.Errorf("adopt all with a worker on box: %v", r)
	}
}

// TestControlWorktreeNew: worktreeNew asks what pitwall worktree new run
// in the repo asks (cmd/pitwall TestWorktreeNew and TestWorktreeNewFirst),
// and starts nothing for a lane on another host.
func TestControlWorktreeNew(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the daemon serves control on Linux only")
	}
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"commit", "--allow-empty", "-m", "init"}, {"branch", "side"}} {
		args = append([]string{"-C", repo, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %q: %v: %s", args, err, out)
		}
	}
	f, c := controlServer(t)
	f.edit(func(f *fakeControl) {
		f.settings.GuardWorkers = true
		f.settings.Fleet = config.FleetSettings{On: true, Lanes: []config.LaneSettings{{Name: "small", Preset: "safe", Host: "box"}, {Name: "big", Preset: "safe"}}}
		f.settings.Presets = []config.AgentPreset{{Name: "safe", Agent: "claude"}}
	})
	// The worktree's tab id is the daemon's to pick.
	id := func(want proto.NewWorkspace) func() []any {
		return func() []any {
			var got []any
			f.edit(func(f *fakeControl) { got = f.reqs })
			if m, isNew := got[len(got)-1].(proto.NewWorkspace); isNew {
				want.ID = m.ID
			}
			return append(got[:len(got)-1:len(got)-1], want)
		}
	}
	for _, tc := range []struct {
		req  string
		want func() []any
	}{
		{fmt.Sprintf(`{"type":"worktreeNew","id":"1","session":"main","repo":%q,"name":"fix","branch":"side"}`, filepath.Join(repo, ".")),
			id(proto.NewWorkspace{ProjectID: "g" + repo, Name: "fix", From: model.WorktreeFrom{Kind: model.FromBranch, Ref: "side"}})},
		{fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"fix","parentPane":"p1","cmd":["claude"]}`, repo),
			id(proto.NewWorkspace{Name: "fix", Cmd: []string{"claude"}, Env: []string{"PITWALL_GUARD=1"}, ParentPane: "p1", Root: repo})},
		{fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"fix","parentPane":"p1","lane":"big","cmd":["fix","it"]}`, repo),
			id(proto.NewWorkspace{Name: "fix", Cmd: []string{"claude", "fix it"}, Env: []string{"PITWALL_GUARD=1"}, ParentPane: "p1", Root: repo, Lane: "big"})},
	} {
		f.edit(func(f *fakeControl) { f.reqs = nil })
		r := ask(t, c, tc.req)
		if d := data(r); d["tab"] == nil || d["pane"] != "n"+d["tab"].(string) {
			t.Errorf("%s: %v", tc.req, r)
			continue
		}
		var got []any
		f.edit(func(f *fakeControl) { got = f.reqs })
		if want := tc.want(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: asked %#v, want %#v", tc.req, got, want)
		}
	}
	// The first request grouped the repo; a second one in main reuses it.
	askLocal(t, f, c, fmt.Sprintf(`{"type":"worktreeNew","id":"1","session":"main","repo":%q,"name":"x","lane":"small","cmd":["go"]}`, repo), "remote")
	for _, req := range []string{
		`{"type":"worktreeNew","id":"1","repo":"src","name":"x"}`,
		fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":" "}`, repo),
		fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"x","from":"main","pr":3}`, repo),
		fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"x","branch":"nope"}`, repo),
		fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"x","host":"box"}`, repo),
		fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"x","guard":true}`, t.TempDir()),
	} {
		askLocal(t, f, c, req, "invalid")
	}
	askLocal(t, f, c, fmt.Sprintf(`{"type":"worktreeNew","id":"1","repo":%q,"name":"x","parentPane":"p9"}`, repo), "not-found")
	f.edit(func(f *fakeControl) { f.fail = ControlError("refused", "the guard") })
	if r := ask(t, c, fmt.Sprintf(`{"type":"worktreeNew","id":"1","session":"main","repo":%q,"name":"x","cmd":["claude"],"guard":true}`, repo)); r["code"] != "refused" {
		t.Errorf("a refused worktree: %v", r) // the daemon's gate refuses; the daemon test checks it starts nothing
	}
}
