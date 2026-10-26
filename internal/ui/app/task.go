package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// taskDialog is the New task dialog: agents and their prompt, each
// started in a tab of its own now or queued (proto.NewTask). The drawing
// is in taskdraw.go.
type taskDialog struct {
	projects []taskProject
	project  int
	where    int // whereHere, whereWorktree or whereBranch
	agents   []foundAgent
	picked   []bool // per agent: whether it gets a task
	modes    []int  // per agent: into taskModes of it
	err      string
	preset   string // the prompt the next open starts with

	prompt, name, base, branch, issue widget.Editor
	autoName                          string             // the name the prompt last filled in; once the user changes it, the prompt stops
	refs                              proto.WorktreeInfo // the project's branches, for the base and branch fields
	issues                            taskIssues
	focus                             bool // move key focus to the prompt this frame

	projectBtn, agentBtn []widget.Clickable
	modeBtn              [][]widget.Clickable // per agent
	whereBtn             [3]widget.Clickable
	matchBtn, issueBtn   [maxPicks]widget.Clickable
	cancel, queue, start widget.Clickable
}

// taskIssues is the open issues of the dialog's project, which a goroutine
// fetches.
type taskIssues struct {
	mu   sync.Mutex
	gen  int // bumped by each fetch, so a stale one drops its answer
	list []forge.Issue
}

// listIssues is forge.Issues; tests replace it.
var listIssues = forge.Issues

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
	t.picked, t.modes = make([]bool, len(t.agents)), make([]int, len(t.agents))
	if len(t.agents) > 0 {
		t.picked[max(0, slices.IndexFunc(t.agents, func(a foundAgent) bool { return a.cmd == u.gui.TaskAgent }))] = true
	}
	t.err, t.focus = "", true
	t.prompt.Submit, t.name.SingleLine, t.base.SingleLine, t.branch.SingleLine, t.issue.SingleLine = false, true, true, true, true
	t.name.Submit, t.base.Submit, t.branch.Submit, t.issue.Submit = true, true, true, true
	t.prompt.SetText(t.preset)
	t.preset = ""
	t.name.SetText("")
	t.branch.SetText("")
	t.issue.SetText("")
	t.autoName = ""
	t.pickProject(t.project, u.invalidate)
	u.modal.open(modalTask, "")
}

// pickProject selects project i: its branches, its default branch as the
// base, and its open issues, fetched in the background with invalidate
// called once they are in. Outside a repo only "This folder" is left.
func (t *taskDialog) pickProject(i int, invalidate func()) {
	t.project = i
	p := t.projects[i]
	t.base.SetText(p.base)
	t.refs = proto.WorktreeInfo{}
	is := &t.issues
	is.mu.Lock()
	is.gen++
	gen := is.gen
	is.list = nil
	is.mu.Unlock()
	if !p.git {
		t.where = whereHere
		return
	}
	if Host == "" { // the repo is on the host otherwise
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		r, _ := gitstat.ListRefs(ctx, p.dir)
		cancel()
		t.refs.Local, t.refs.Remote = r.Local, r.Remote
		fetch := listIssues
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			list, _ := fetch(ctx, p.dir) // no gh, no GitHub remote or no issues: the field stays hidden
			cancel()
			is.mu.Lock()
			if is.gen == gen {
				is.list = list
			}
			is.mu.Unlock()
			if invalidate != nil {
				invalidate()
			}
		}()
	}
}

// issueList is the project's open issues, none until they are fetched.
func (t *taskDialog) issueList() []forge.Issue {
	t.issues.mu.Lock()
	defer t.issues.mu.Unlock()
	return t.issues.list
}

// issueLabel is how the issue field lists i.
func issueLabel(i forge.Issue) string {
	return fmt.Sprintf("#%d %s", i.Number, i.Title)
}

// issueBranch is the branch a task on issue i gets: issue-<n>- and the
// first words of its title.
func issueBranch(i forge.Issue) string {
	return strings.TrimSuffix(fmt.Sprintf("issue-%d-%s", i.Number, worktreeName(i.Title)), "-")
}

// take puts the match s in field e. For the issue field, s's issue fills
// in the prompt and names the worktree; both stay editable.
func (t *taskDialog) take(e *widget.Editor, s string) {
	setText(e, s)
	if e != &t.issue {
		return
	}
	for _, i := range t.issueList() {
		if issueLabel(i) == s {
			setText(&t.prompt, i.Prompt())
			// The caret stays at the start, where issue-<n> shows, and the
			// prompt no longer names the worktree.
			t.name.SetText(issueBranch(i))
			t.autoName = ""
		}
	}
}

// pickAgent picks agent i alone, or with add adds it to the picked ones or
// drops it from them. Several agents each need a new worktree, so picking
// a second one in a repo switches Where to it.
func (t *taskDialog) pickAgent(i int, add bool) {
	n := t.pickedCount()
	switch {
	case !add:
		clear(t.picked)
	case t.picked[i] && n > 1:
		t.picked[i] = false
		return
	}
	t.picked[i] = true
	if t.pickedCount() > 1 && t.projects[t.project].git {
		t.where = whereWorktree
	}
}

func (t *taskDialog) pickedCount() int {
	n := 0
	for _, p := range t.picked {
		if p {
			n++
		}
	}
	return n
}

// target says where the task runs, for the line under Where.
func (t *taskDialog) target() string {
	dir := model.ShortPath(t.projects[t.project].dir)
	switch t.where {
	case whereWorktree:
		if t.pickedCount() > 1 {
			return "A new worktree per agent in " + dir + "/.worktrees, each on a new branch off " + cmp.Or(strings.TrimSpace(t.base.Text()), "the default branch") + "."
		}
		return "A new worktree in " + dir + "/.worktrees, on a new branch off " + cmp.Or(strings.TrimSpace(t.base.Text()), "the default branch") + "."
	case whereBranch:
		return "A new worktree in " + dir + "/.worktrees, on the branch as it is."
	}
	return "Runs in " + dir + "."
}

// tasks is the dialog's tasks, one per picked agent, or why they cannot
// start. With several agents each gets its own worktree, on a branch named
// after the agent too.
func (t *taskDialog) tasks() ([]model.Task, error) {
	p := t.projects[t.project]
	prompt := strings.TrimSpace(t.prompt.Text())
	if prompt == "" {
		return nil, errors.New("Write a prompt for the agent.")
	}
	if len(t.agents) == 0 {
		return nil, errors.New("No agent CLI is on PATH.")
	}
	several := t.pickedCount() > 1
	if several && t.where != whereWorktree {
		return nil, errors.New("Several agents need a new worktree each: pick New worktree under Where.")
	}
	task := model.Task{GroupID: p.group, Dir: p.dir}
	switch t.where {
	case whereWorktree:
		if task.Worktree = strings.TrimSpace(t.name.Text()); worktreeName(task.Worktree) == "" {
			return nil, errors.New("Name the worktree with a letter or digit.")
		}
		task.Base = strings.TrimSpace(t.base.Text())
	case whereBranch:
		if task.Branch = strings.TrimSpace(t.branch.Text()); task.Branch == "" {
			return nil, errors.New("Pick the branch to check out.")
		}
	}
	var out []model.Task
	for i, a := range t.agents {
		if !t.picked[i] {
			continue
		}
		path := a.cmd
		if Host == "" {
			// why: the daemon runs it with its own PATH, as pitwall new does.
			if found, err := exec.LookPath(a.cmd); err == nil {
				path, _ = filepath.Abs(found)
			}
		}
		at := task
		at.Cmd = taskCmd(path, a.cmd, t.modes[i], prompt)
		if several {
			at.Worktree += "-" + a.cmd
		}
		out = append(out, at)
	}
	return out, nil
}

// submitTask sends the dialog's tasks, queued or started, and closes it.
// Started together, their tabs open next to each other in this order.
func (u *ui) submitTask(st *model.State, queue bool) {
	t := &u.task
	tasks, err := t.tasks()
	if err != nil {
		t.err = err.Error()
		return
	}
	if !queue {
		u.nav.expectSession(st) // the new tab shows once it opens
	}
	for _, task := range tasks {
		task.SessionID = u.nav.session
		if !u.send(proto.NewTask{Task: task, Queue: queue, FromPane: u.nav.focused()}) {
			u.nav.sessions = nil
			t.err = "The pitwall daemon running is older than this window and cannot start tasks. They work once it restarts."
			return
		}
	}
	// The first picked agent is the one the dialog picks next time.
	if a := t.agents[slices.Index(t.picked, true)].cmd; a != u.gui.TaskAgent && u.report != nil { // a real window, not a test
		u.gui.TaskAgent = a
		saveGUIState(u.gui)
	}
	u.modal.close()
}
