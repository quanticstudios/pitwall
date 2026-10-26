package daemon

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// A second opinion: another agent reviews a tab's changes, read only, and
// its first reply goes to the tab's agent as a message.

// readOnly are the flags that start each reviewer without write access.
var readOnly = map[string][]string{
	"claude": {"--permission-mode", "plan"},
	"codex":  {"-s", "read-only"},
}

// reviewNote is the line above the reviewer's reply in the message to the
// tab's agent.
const reviewNote = "Another agent reviewed your changes. Verify each blocker and major against the code before changing anything, and say which findings you dropped and why."

// reviewPrompt is the reviewer's instructions; %[1]s is the base ref.
// Adapted from forge (MIT): prompts/review.md
const reviewPrompt = `# Second opinion

Another coding agent made the changes in this worktree and keeps working on
them. Review them. pitwall sends your final reply to that agent.

Do not modify code: no edits, no commits, no commands that change files or
git state. Do the review yourself; start no other agents.

## What to review

The branch against its base, plus uncommitted changes:

    git log --oneline %[1]s..HEAD
    git diff --merge-base %[1]s
    git status --short

Untracked files in the status are new files too; read them.

## What to check

1. Correctness: logic errors, edge cases, error handling, concurrency.
2. Tests: the change has them where it needs them, and they pass. Run the
   checks yourself where that is safe and changes nothing.
3. Risk: secrets, destructive operations, public interfaces, migrations.
4. Fit: the repository's conventions; no needless abstraction, dead code or
   scope creep.

Give every finding a severity (blocker, major, minor or nit), a location
(path:line), the problem and a concrete fix. Only blocker and major
findings justify changes. Do not invent problems to look thorough.

## Your final reply

End your turn with a final summary as your reply, not as a plan or a
question: one line with your verdict, then the findings, most severe first,
one per line:

    - [major] path/to/file.go:42: the problem. Fix: the fix.

Write "No findings." when you have none. In plan mode, answer with that
reply; do not leave plan mode.
`

// secondOpinion opens m.Agent beside m.Pane, in the pane's folder, on a
// file of review instructions, and keeps which pane its first reply goes to.
func (d *Daemon) secondOpinion(m proto.SecondOpinion) error {
	name := strings.TrimSuffix(filepath.Base(m.Agent), ".exe")
	flags, ok := readOnly[name]
	if !ok {
		return fmt.Errorf("pick claude or codex to review, not %q", m.Agent)
	}
	path, err := exec.LookPath(m.Agent)
	if err != nil {
		return fmt.Errorf("%s is not installed: no %s on PATH", name, name)
	}
	if _, busy, err := d.ready(m.Pane); err != nil && !busy {
		return err
	}
	d.mu.Lock()
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == m.Pane })
	if i < 0 { // closed since ready
		d.mu.Unlock()
		return fmt.Errorf("no pane %s", m.Pane)
	}
	sp := d.st.Panes[i]
	stats, git := d.st.Stats[sp.WorkspaceID]
	d.mu.Unlock()
	if !git {
		return errors.New("the tab's folder is no git repository, so it has no changes to review")
	}
	dir := filepath.Join(config.StateDir(), "reviews")
	file := filepath.Join(dir, m.Pane+".md")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(file, []byte(fmt.Sprintf(reviewPrompt, cmp.Or(stats.Base, "origin/HEAD"))), 0o600); err != nil {
		return err
	}
	pointer := "Review the changes in this worktree. Your full instructions are in " + file + ": read the whole file now and follow it exactly."
	cmd := append(append([]string{path}, flags...), pointer)

	d.mu.Lock()
	defer d.mu.Unlock()
	w := d.workspace(sp.WorkspaceID)
	if w == nil {
		return fmt.Errorf("no tab for pane %s", m.Pane)
	}
	t := slices.IndexFunc(w.Tabs, func(t model.Tab) bool { return slices.Contains(layout.Panes(t.Layout), m.Pane) })
	if t < 0 {
		return fmt.Errorf("pane %s is in no tab", m.Pane)
	}
	id := newID()
	if err := d.start(id, cmd, cmp.Or(sp.Cwd, w.Path), w.Ports); err != nil {
		return err
	}
	d.st.Panes = append(d.st.Panes, model.Pane{ID: id, WorkspaceID: w.ID, Cmd: cmd, Cwd: cmp.Or(sp.Cwd, w.Path)})
	w.Tabs[t].Layout = d.o.Split(w.Tabs[t].Layout, m.Pane, id, layout.Horizontal)
	if d.reviews == nil {
		d.reviews = map[string]string{}
	}
	d.reviews[id] = m.Pane
	d.changed()
	return nil
}

// relayReview queues the reply just kept for pane, a reviewer's, for the
// agent it reviews, once: the reviewer stays open, and its later turns go
// nowhere. Callers hold d.mu.
func (d *Daemon) relayReview(pane string) {
	to, ok := d.reviews[pane]
	if !ok {
		return
	}
	delete(d.reviews, pane)
	if d.panes[to] == nil {
		return // the reviewed pane closed
	}
	d.mail.queue[to] = append(d.mail.queue[to], letter{from: pane, text: reviewNote + "\n\n" + d.replies[pane]})
	d.changed()
	d.wake(to, 0)
}
