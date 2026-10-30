package app

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestProfilePicker(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, cfg: config.Settings{Profiles: map[string]config.Profile{"work": {Label: "Work plan"}, "personal": {}}}}
	st := b.State()
	for _, msg := range []any{proto.NewTab{WorkspaceID: "w1"}, proto.NewSession{Cwd: "/repo", GroupID: "g1"}} {
		b.sent = nil
		if !u.send(msg) || u.modal.kind != modalProfile || len(b.sent) != 0 {
			t.Fatalf("profile picker did not defer %T", msg)
		}
		if !reflect.DeepEqual(u.profile.names, []string{"", "personal", "work"}) || !reflect.DeepEqual(u.profile.labels, []string{"Default", "personal", "Work plan"}) {
			t.Fatalf("choices %+v", u.profile)
		}
		u.profile.picked = 2
		u.confirmModal(&st)
		if len(b.sent) != 1 || u.modal.kind != modalNone {
			t.Fatalf("profile picker sent %v", b.sent)
		}
		switch got := b.sent[0].(type) {
		case proto.NewTab:
			if got.Profile != "work" || got.WorkspaceID != "w1" {
				t.Fatal(got)
			}
		case proto.NewSession:
			if got.Profile != "work" || got.Cwd != "/repo" || got.GroupID != "g1" {
				t.Fatal(got)
			}
		}
	}
	b.sent = nil
	u.send(proto.NewTab{WorkspaceID: "w1"})
	u.confirmProfileTab()
	if len(b.sent) != 1 || b.sent[0].(proto.NewTab).Profile != "" {
		t.Fatal("default profile did not send original new tab")
	}
	u.cfg.Profiles = nil
	b.sent = nil
	u.send(proto.NewTab{WorkspaceID: "w1"})
	if len(b.sent) != 1 || u.modal.kind != modalNone {
		t.Fatal("no profiles changed the existing path")
	}
}

func TestTaskProfile(t *testing.T) {
	task := taskDialog{projects: []taskProject{{dir: t.TempDir()}}, agents: []foundAgent{{cmd: "claude"}, {cmd: "codex"}}, picked: []bool{true, true}, modes: []int{0, 0}}
	task.profile.reset(config.Settings{Profiles: map[string]config.Profile{"work": {}}})
	task.profile.picked = 1
	task.prompt.SetText("Fix it")
	task.where = whereWorktree
	task.name.SetText("fix-it")
	tasks, err := task.tasks()
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks %v, %v", tasks, err)
	}
	for _, v := range tasks {
		if v.Profile != "work" {
			t.Fatalf("task lost profile: %+v", v)
		}
	}
	task.models = make([]taskModel, 2)
	task.models[0].editor.SetText("sonnet")
	task.models[1].editor.SetText("gpt-custom")
	tasks, err = task.tasks()
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks with models %v, %v", tasks, err)
	}
	for i, model := range []string{"--model sonnet", "-m gpt-custom"} {
		if tasks[i].Profile != "work" || !strings.Contains(strings.Join(tasks[i].Cmd, " "), model) {
			t.Fatalf("task lost profile or model: %+v", tasks[i])
		}
	}
}

func TestProfileLimitNotices(t *testing.T) {
	now := time.Now()
	w := flow.Window{Minutes: 300, Used: 95, Resets: now.Add(time.Hour)}
	base := flow.Limit{Provider: model.ProviderClaude, Windows: []flow.Window{w}}
	work := base
	work.Profile = "work"
	work.ProfileLabel = "Work plan"
	if limitKey(base, w) == limitKey(work, w) {
		t.Fatal("notice keys share accounts")
	}
	if !strings.Contains(limitNotice(work, w, now), "Work plan") {
		t.Fatal("notice omits profile")
	}
}

func TestProfilePickerOldDaemon(t *testing.T) {
	b := &linked{FakeBackend: NewFakeBackend(), link: Link{Level: proto.ProfileLevel - 1}}
	u := &ui{b: b, cfg: config.Settings{Profiles: map[string]config.Profile{"work": {}}}}
	u.send(proto.NewTab{WorkspaceID: "w1"})
	u.profile.picked = 1
	u.confirmProfileTab()
	if len(b.sent) != 0 || u.modal.pathErr == "" || u.modal.kind != modalProfile {
		t.Fatal("old daemon could drop the profile")
	}
	u.profile.picked = 0
	u.confirmProfileTab()
	if len(b.sent) != 1 || u.modal.kind != modalNone {
		t.Fatal("old daemon cannot start the default profile")
	}
}
