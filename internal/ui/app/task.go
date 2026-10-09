package app

import (
	"cmp"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// taskDialog is the New task dialog: an agent and its prompt, started in a
// tab of its own now or queued (proto.NewTask). The drawing is in
// taskdraw.go.
type taskDialog struct {
	projects []taskProject
	project  int
	where    int // whereHere, whereWorktree or whereBranch
	agents   []foundAgent
	agent    int
	mode     int // into taskModes of the agent
	err      string

	prompt, name, base, branch widget.Editor
	autoName                   string             // the name the prompt last filled in; once the user changes it, the prompt stops
	refs                       proto.WorktreeInfo // the project's branches, for the base and branch fields
	focus                      bool               // move key focus to the prompt this frame

	projectBtn, agentBtn, modeBtn []widget.Clickable
	whereBtn                      [3]widget.Clickable
	matchBtn                      [maxPicks]widget.Clickable
	cancel, queue, start          widget.Clickable
}

const (
	whereHere = iota
	whereWorktree
	whereBranch
)

// taskProject is a folder the dialog can start a task in: a group's root
// or a tab's repo.
type taskProject struct {
	name, dir string
	group     string // the group a tab there joins, "" for the folder's own
	git       bool
	base      string // the default branch's name, "" when unknown
}

// taskMode is a permission or plan mode an agent takes at launch.
type taskMode struct {
	name string
	args []string
}

// taskModes are the launch modes of the agents that have them, the first
// leaving the agent's own default. They are the flags a resumed agent
// keeps (store.RestoreCmd).
var taskModes = map[string][]taskMode{
	"claude": {{"Default", nil}, {"Accept edits", []string{"--permission-mode", "acceptEdits"}}, {"Plan", []string{"--permission-mode", "plan"}}},
	"codex":  {{"Default", nil}, {"Read only", []string{"-s", "read-only"}}, {"Auto", []string{"-s", "workspace-write", "-a", "on-request"}}},
}

// promptFlags are the flags before the prompt of agents that do not take
// it as their first argument and stay open.
var promptFlags = map[string][]string{"gemini": {"-i"}, "opencode": {"--prompt"}}

// taskCmd is the argv that starts agent, the program at path, in mode on
// prompt.
func taskCmd(path, agent string, mode int, prompt string) []string {
	cmd := []string{path}
	if ms := taskModes[agent]; mode > 0 && mode < len(ms) {
		cmd = append(cmd, ms[mode].args...)
	}
	return append(append(cmd, promptFlags[agent]...), prompt)
}

// taskProjects lists the folders of session's groups and tabs, in sidebar
// order, and picks group's, else the tab ws's.
func taskProjects(st *model.State, session, ws, group string) ([]taskProject, int) {
	var out []taskProject
	add := func(p taskProject) int {
		if i := slices.IndexFunc(out, func(o taskProject) bool { return o.dir == p.dir }); i >= 0 {
			out[i].git = out[i].git || p.git
			out[i].base = cmp.Or(out[i].base, p.base)
			out[i].group = cmp.Or(out[i].group, p.group)
			return i
		}
		out = append(out, p)
		return len(out) - 1
	}
	home, _ := os.UserHomeDir()
	pick := -1
	groups := map[string]model.Project{}
	for _, p := range st.Projects {
		if p.SessionID == session {
			groups[p.ID] = p
		}
	}
	for _, w := range st.Ordered(session) {
		if w.Detached {
			continue
		}
		// A worktree pitwall made counts as its main checkout, which new
		// worktrees come from.
		dir := cmp.Or(w.WorktreeRoot, w.RepoRoot, w.Path)
		g := groups[w.ProjectID].ID
		if p := groups[w.ProjectID]; p.Root != "" && p.Root != dir {
			g = "" // a tab of another folder in a folder's group
		}
		bs, git := st.Stats[w.ID]
		name := filepath.Base(dir)
		if dir == home {
			name = "~"
		}
		i := add(taskProject{name: name, dir: dir, group: g, git: git || w.Branch != "", base: gitstat.BranchName(bs.Base)})
		if w.ID == ws && group == "" || g == group && group != "" && pick < 0 {
			pick = i
		}
	}
	for _, p := range st.Projects {
		if p.SessionID == session && p.Root != "" {
			i := add(taskProject{name: p.Name, dir: p.Root, git: p.Kind == model.ProjectGit})
			out[i].group = p.ID // the folder's own group over a group of tabs
			if p.ID == group {
				pick = i
			}
		}
	}
	return out, max(pick, 0)
}

// worktreeName is the worktree name a prompt suggests: the first few words
// of its first line, as a branch name.
func worktreeName(prompt string) string {
	var words []string
	n := 0
	for _, w := range strings.FieldsFunc(strings.ToLower(model.Task{Cmd: []string{"", prompt}}.Title()), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	}) {
		if len(words) == 5 || n+len(w) > 40 {
			break
		}
		words, n = append(words, w), n+len(w)+1
	}
	return strings.Join(words, "-")
}

// openTask opens the New task dialog on group's folder, else the open tab's.
func (u *ui) openTask(st *model.State, group string) {
	t := &u.task
	t.projects, t.project = taskProjects(st, u.nav.session, u.nav.workspace, group)
	if len(t.projects) == 0 {
		return
	}
	if Host == "" {
		home, _ := os.UserHomeDir()
		t.agents = findAgents(home)
	} else { // the agents on the host are not on this PATH
		t.agents = nil
		for _, a := range welcomeAgents {
			t.agents = append(t.agents, foundAgent{cmd: a.cmd, name: a.name})
		}
	}
	t.agent = max(0, slices.IndexFunc(t.agents, func(a foundAgent) bool { return a.cmd == u.gui.TaskAgent }))
	t.mode, t.err, t.focus = 0, "", true
	t.prompt.Submit, t.name.SingleLine, t.base.SingleLine, t.branch.SingleLine = false, true, true, true
	t.name.Submit, t.base.Submit, t.branch.Submit = true, true, true
	t.prompt.SetText("")
	t.name.SetText("")
	t.branch.SetText("")
	t.autoName = ""
	t.pickProject(t.project)
	u.modal.open(modalTask, "")
}

// pickProject selects project i: its branches, and its default branch as
// the base. Outside a repo only "This folder" is left.
func (t *taskDialog) pickProject(i int) {
	t.project = i
	p := t.projects[i]
	t.base.SetText(p.base)
	t.refs = proto.WorktreeInfo{}
	if !p.git {
		t.where = whereHere
		return
	}
	if Host == "" { // the repo is on the host otherwise
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		r, _ := gitstat.ListRefs(ctx, p.dir)
		cancel()
		t.refs.Local, t.refs.Remote = r.Local, r.Remote
	}
}

// target says where the task runs, for the line under Where.
func (t *taskDialog) target() string {
	dir := model.ShortPath(t.projects[t.project].dir)
	switch t.where {
	case whereWorktree:
		return "A new worktree in " + dir + "/.worktrees, on a new branch off " + cmp.Or(strings.TrimSpace(t.base.Text()), "the default branch") + "."
	case whereBranch:
		return "A new worktree in " + dir + "/.worktrees, on the branch as it is."
	}
	return "Runs in " + dir + "."
}

// task is the dialog's task, or why it cannot start.
func (t *taskDialog) task() (model.Task, error) {
	p := t.projects[t.project]
	prompt := strings.TrimSpace(t.prompt.Text())
	if prompt == "" {
		return model.Task{}, errors.New("Write a prompt for the agent.")
	}
	if len(t.agents) == 0 {
		return model.Task{}, errors.New("No agent CLI is on PATH.")
	}
	a := t.agents[t.agent]
	path := a.cmd
	if Host == "" {
		// why: the daemon runs it with its own PATH, as pitwall new does.
		if found, err := exec.LookPath(a.cmd); err == nil {
			path, _ = filepath.Abs(found)
		}
	}
	task := model.Task{GroupID: p.group, Dir: p.dir, Cmd: taskCmd(path, a.cmd, t.mode, prompt)}
	switch t.where {
	case whereWorktree:
		if task.Worktree = strings.TrimSpace(t.name.Text()); worktreeName(task.Worktree) == "" {
			return model.Task{}, errors.New("Name the worktree with a letter or digit.")
		}
		task.Base = strings.TrimSpace(t.base.Text())
	case whereBranch:
		if task.Branch = strings.TrimSpace(t.branch.Text()); task.Branch == "" {
			return model.Task{}, errors.New("Pick the branch to check out.")
		}
	}
	return task, nil
}

// submitTask sends the dialog's task, queued or started, and closes it.
func (u *ui) submitTask(st *model.State, queue bool) {
	t := &u.task
	task, err := t.task()
	if err != nil {
		t.err = err.Error()
		return
	}
	task.SessionID = u.nav.session
	if !queue {
		u.nav.expectSession(st) // the new tab shows once it opens
	}
	if !u.send(proto.NewTask{Task: task, Queue: queue, FromPane: u.nav.focused()}) {
		u.nav.sessions = nil
		t.err = "The pitwall daemon running is older than this window and cannot start tasks. They work once it restarts."
		return
	}
	if a := t.agents[t.agent].cmd; a != u.gui.TaskAgent && u.report != nil { // a real window, not a test
		u.gui.TaskAgent = a
		saveGUIState(u.gui)
	}
	u.modal.close()
}
