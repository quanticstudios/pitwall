package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestTaskProjects(t *testing.T) {
	st := NewFakeBackend().State()
	names := func(ps []taskProject) string {
		var out []string
		for _, p := range ps {
			out = append(out, p.name)
		}
		return strings.Join(out, " ")
	}
	ps, pick := taskProjects(&st, "s1", "w4", "")
	// The worktree tab, in the group of tabs billing, counts as web-app,
	// its main checkout, whose own group its task joins. Detached tabs
	// (/tmp) are left out.
	if got := names(ps); got != "acme-api ~ spike web-app notes" || ps[pick].name != "web-app" || ps[pick].group != "g2" || !ps[pick].git {
		t.Fatalf("projects %q, picked %+v", got, ps[pick])
	}
	if _, pick := taskProjects(&st, "s1", "w4", "g2"); ps[pick].dir != fakeHome+"/src/web-app" {
		t.Fatalf("the group's folder not picked: %+v", ps[pick])
	}
	if ps[1].git || ps[0].group != "" {
		t.Fatalf("home %+v, acme-api %+v", ps[1], ps[0])
	}
}

func TestTaskHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"Fix the flaky login test in CI please":             "fix-the-flaky-login-test",
		"\n  Add OAuth2 (Google)\nmore":                     "add-oauth2-google",
		"supercalifragilisticexpialidocious-and-more-words": "supercalifragilisticexpialidocious-and",
		"": "",
	} {
		if got := worktreeName(in); got != want {
			t.Errorf("worktreeName(%q) = %q, want %q", in, got, want)
		}
	}
	for i, want := range map[forge.Issue]string{
		{Number: 12, Title: "Login fails: \"bad token\" on Safari!"}:            "issue-12-login-fails-bad-token-on",
		{Number: 3, Title: "supercalifragilisticexpialidocious-and-more-words"}: "issue-3-supercalifragilisticexpialidocious-and",
		{Number: 7, Title: "???"}: "issue-7",
		{Number: 140, Title: "  Ünïcode Title / with path\nand a second line"}: "issue-140-n-code-title-with-path",
	} {
		if got := issueBranch(i); got != want {
			t.Errorf("issueBranch(%q) = %q, want %q", i.Title, got, want)
		}
	}
	branches := []string{"main", "feature/login", "origin/main", "origin/feature/login", "fix-main-crash"}
	if got := refMatches(branches, "ma"); !slices.Equal(got, []string{"main", "origin/main", "fix-main-crash"}) {
		t.Errorf("refMatches(ma) = %q", got)
	}
	if got := refMatches(branches, "MAIN"); got != nil {
		t.Errorf("an exact branch still offers %q", got)
	}
	for _, c := range []struct {
		agent string
		mode  int
		want  []string
	}{
		{"claude", 2, []string{"/bin/claude", "--permission-mode", "plan", "go"}},
		{"codex", 0, []string{"/bin/codex", "go"}},
		{"codex", 1, []string{"/bin/codex", "-s", "read-only", "go"}},
		{"gemini", 0, []string{"/bin/gemini", "-i", "go"}},
		{"pi", 3, []string{"/bin/pi", "go"}},
	} {
		if got := taskCmd("/bin/"+c.agent, c.agent, c.mode, "go"); !slices.Equal(got, c.want) {
			t.Errorf("taskCmd(%s, %d) = %q, want %q", c.agent, c.mode, got, c.want)
		}
	}
}

// The new_task action opens the dialog on the open tab's repo; the prompt
// names the worktree, and Start and Queue send the task.
func TestTaskDialog(t *testing.T) {
	bin := t.TempDir()
	for _, a := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, exeName(a)), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	b := NewFakeBackend()
	u := &ui{b: b}
	st := b.State()
	u.nav.sync(&st)
	u.nav.selectWorkspace(&st, "w6", "")
	u.gui.TaskAgent = "codex"
	u.runAction(&st, config.Action{Name: "new_task", Group: "Agents"})
	u.openRequested(&st, time.Now())
	d := &u.task
	codex := slices.IndexFunc(d.agents, func(a foundAgent) bool { return a.cmd == "codex" })
	if u.modal.kind != modalTask || d.projects[d.project].name != "web-app" || d.pickedCount() != 1 || !d.picked[codex] {
		t.Fatalf("dialog %v on %+v with %+v", u.modal.kind, d.projects[d.project], d.agents)
	}
	u.submitTask(&st, false)
	if d.err == "" || u.modal.kind != modalTask {
		t.Fatal("started without a prompt")
	}
	d.where = whereWorktree
	d.prompt.SetText("Fix the login redirect\nand add a test")
	d.modes[codex] = 1
	d.syncName()
	if d.name.Text() != "fix-the-login-redirect" {
		t.Fatalf("worktree name %q", d.name.Text())
	}
	d.base.SetText("release/1.4")
	u.submitTask(&st, true)
	want := proto.NewTask{Queue: true, FromPane: u.nav.focused(), Task: model.Task{SessionID: "s1", GroupID: "g2", Dir: fakeHome + "/src/web-app",
		Worktree: "fix-the-login-redirect", Base: "release/1.4", Cmd: []string{filepath.Join(bin, exeName("codex")), "-s", "read-only", "Fix the login redirect\nand add a test"}}}
	sent := b.Sent()
	if got := sent[len(sent)-1]; !reflect.DeepEqual(got, want) || u.modal.kind != modalNone {
		t.Fatalf("sent %#v\nwant %#v", got, want)
	}
}

// Picking an issue fills the prompt and names the worktree after it; two
// agents make two tasks, each in its own worktree with its own mode, sent
// in order.
func TestTaskIssueAgents(t *testing.T) {
	bin := t.TempDir()
	for _, a := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, exeName(a)), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	issues := []forge.Issue{
		{Number: 57, Title: "Login redirect loops on Safari", URL: "https://github.com/example/widgets/issues/57", Body: "It loops."},
		{Number: 49, Title: "Add a dark theme", URL: "https://github.com/example/widgets/issues/49"},
	}
	defer func(f func(context.Context, string) ([]forge.Issue, error)) { listIssues = f }(listIssues)
	listIssues = func(context.Context, string) ([]forge.Issue, error) { return issues, nil }
	b := NewFakeBackend()
	u := &ui{b: b}
	st := b.State()
	u.nav.sync(&st)
	u.nav.selectWorkspace(&st, "w6", "")
	u.gui.TaskAgent = "claude"
	u.openTask(&st, "")
	d := &u.task
	for deadline := time.Now().Add(5 * time.Second); len(d.issueList()) == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("issues never fetched")
		}
	}
	if got := d.matches(&d.issue); !slices.Equal(got, []string{"#57 Login redirect loops on Safari", "#49 Add a dark theme"}) {
		t.Fatalf("issue matches %q", got)
	}
	d.issue.SetText("dark")
	ms := d.matches(&d.issue)
	if len(ms) != 1 {
		t.Fatalf("matches for dark %q", ms)
	}
	d.take(&d.issue, ms[0])
	d.syncName()
	if d.prompt.Text() != issues[1].Prompt() || d.name.Text() != "issue-49-add-a-dark-theme" || d.matches(&d.issue) != nil {
		t.Fatalf("prompt %q, name %q", d.prompt.Text(), d.name.Text())
	}
	claude := slices.IndexFunc(d.agents, func(a foundAgent) bool { return a.cmd == "claude" })
	codex := slices.IndexFunc(d.agents, func(a foundAgent) bool { return a.cmd == "codex" })
	d.where = whereHere
	d.pickAgent(codex, true)
	if d.pickedCount() != 2 || d.where != whereWorktree {
		t.Fatalf("picked %v, where %d", d.picked, d.where)
	}
	d.modes[claude] = 2
	d.where = whereBranch
	if _, err := d.tasks(); err == nil {
		t.Fatal("two agents on one branch")
	}
	d.where = whereWorktree
	d.base.SetText("main")
	u.submitTask(&st, true)
	prompt := issues[1].Prompt()
	task := model.Task{SessionID: "s1", GroupID: "g2", Dir: fakeHome + "/src/web-app", Base: "main"}
	var want []any
	for _, a := range d.agents { // in the agents' order
		at := task
		at.Worktree = "issue-49-add-a-dark-theme-" + a.cmd
		at.Cmd = []string{filepath.Join(bin, exeName(a.cmd)), prompt}
		if a.cmd == "claude" {
			at.Cmd = slices.Insert(at.Cmd, 1, "--permission-mode", "plan")
		}
		want = append(want, proto.NewTask{Queue: true, FromPane: u.nav.focused(), Task: at})
	}
	sent := b.Sent()
	if got := sent[len(sent)-2:]; !reflect.DeepEqual(got, want) || u.modal.kind != modalNone {
		t.Fatalf("sent %#v\nwant %#v", got, want)
	}
	d.pickAgent(codex, true)
	d.pickAgent(claude, true) // the last one stays
	if !d.picked[claude] || d.pickedCount() != 1 {
		t.Fatalf("picked %v", d.picked)
	}
}
