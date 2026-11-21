package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// A rewind of the files puts a tab's work tree back to the checkpoint
// pitwall took when the turn's prompt was sent (gitstat.Checkpoint), for a
// chat tab and an agent pane alike: Claude Code's own file history holds
// only what its edit tools wrote, and Codex and Antigravity keep none a
// rewind can use. It refuses while another agent works in the folder,
// whose edits it would undo, and while files changed after the agent's
// last turn, unless they are stashed first; it keeps the files in a
// backup commit before it writes one (gitstat.RewindTurn).

// turnEnd takes tab ws's turn end, once an agent there ended a turn,
// waiting for it at most turnWait.
func (d *Daemon) turnEnd(ws string) {
	if d.o.EndTurn == nil {
		return
	}
	dir := d.tabDir(ws)
	if dir == "" {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.turnMu.Lock() // after the turn's checkpoint, never beside it
		defer d.turnMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, ok := gitstat.RepoRoot(ctx, dir); !ok {
			return
		}
		if _, err := d.o.EndTurn(ctx, dir, ws); err != nil {
			log.Printf("tab %s: turn end: %q", ws, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(turnWait):
	}
}

// sharers is the other agents in the repository at dir, by name: agent
// panes and chats other than self, whose folder is in it.
func (d *Daemon) sharers(ctx context.Context, dir, self string) []string {
	root, ok := gitstat.RepoRoot(ctx, dir)
	if !ok {
		root = dir
	}
	in := func(p string) bool {
		rel, err := filepath.Rel(root, p)
		return p != "" && err == nil && filepath.IsLocal(rel)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	term := "" // self's own TUI, when it is a chat tab showing it
	if i := slices.IndexFunc(d.st.Chats, func(c model.Chat) bool { return c.ID == self }); i >= 0 {
		term = d.st.Chats[i].Terminal
	}
	var names []string
	for _, p := range d.st.Panes {
		if p.ID == self || p.ID == term || p.Provider == "" || p.Exited || d.isParked(p.ID) {
			continue
		}
		live := ""
		if w := d.workspace(p.WorkspaceID); w != nil {
			live = d.st.LivePath(*w)
		}
		if in(d.turnCwd[p.ID]) || in(p.Cwd) || in(live) {
			names = append(names, fmt.Sprintf("%s in tab %q", providerName(p.Provider), d.tabName(p.WorkspaceID, p.WorkspaceID)))
		}
	}
	for _, c := range d.st.Chats {
		if c.ID == self {
			continue
		}
		if w := d.workspace(c.WorkspaceID); w != nil && in(d.st.LivePath(*w)) {
			names = append(names, fmt.Sprintf("the %s chat %q", providerName(c.Provider), d.tabName(c.WorkspaceID, c.Title)))
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// planFiles is what putting tab ws's files back to checkpoint rev would
// do, for chat self, refused as rewindFiles refuses, short of unsaved
// files, which it lists.
func (d *Daemon) planFiles(ctx context.Context, self, ws, rev string) (gitstat.RewindPlan, error) {
	dir := d.tabDir(ws)
	switch {
	case dir == "":
		return gitstat.RewindPlan{}, chatError{"not-found", "the chat's tab is closed"}
	case rev == "":
		return gitstat.RewindPlan{}, chatError{"unsupported", "pitwall took no snapshot of the files before this message, so they cannot go back"}
	}
	if others := d.sharers(ctx, dir, self); len(others) > 0 {
		return gitstat.RewindPlan{}, chatError{"conflict", fmt.Sprintf("%s also %s in this folder, and its edits would go back too; close it, or keep the changes",
			strings.Join(others, " and "), plural(len(others), "works", "work"))}
	}
	p, err := gitstat.PlanRewind(ctx, dir, ws, rev)
	if err != nil {
		return p, chatError{"conflict", "the files cannot go back: " + err.Error()}
	}
	return p, nil
}

// rewindFiles puts tab ws's files back to checkpoint rev for chat self,
// as planFiles plans it. Files changed after the agent's last turn refuse
// it unless stash. It returns the backup commit of the files before, ""
// when they already matched.
func (d *Daemon) rewindFiles(ctx context.Context, self, ws, rev string, stash bool) (string, error) {
	if _, err := d.planFiles(ctx, self, ws, rev); err != nil {
		return "", err
	}
	backup, err := gitstat.RewindTurn(ctx, d.tabDir(ws), ws, rev, stash)
	var u *gitstat.UnsavedError
	switch {
	case errors.As(err, &u):
		return "", chatError{"conflict", u.Error() + "; stash them first, or keep the changes"}
	case err != nil:
		return "", chatError{"conflict", "the files were not put back, so nothing was rewound: " + err.Error()}
	}
	return backup, nil
}

// backupNote says where the files before a rewind are and how to get
// them back; "" for no backup.
func backupNote(backup string) string {
	if backup == "" {
		return ""
	}
	short := backup[:min(len(backup), 12)]
	return fmt.Sprintf(". The files from before are saved in commit %s; to get them back, run git restore --source=%s --worktree -- :/", short, short)
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// chatRewindCheck answers m: what a rewind of chat m.Chat's files to
// before turn m.Turn would do.
func (d *Daemon) chatRewindCheck(ctx context.Context, m proto.ChatRewindCheck) proto.ChatRewindFiles {
	out := proto.ChatRewindFiles{Chat: m.Chat, Turn: m.Turn}
	ws, rev, err := d.rewindPoint(ctx, m.Chat, m.Turn)
	var p gitstat.RewindPlan
	if err == nil {
		p, err = d.planFiles(ctx, m.Chat, ws, rev)
	}
	if err != nil {
		out.Refused = err.Error()
		return out
	}
	out.Files, out.Unsaved = rewindFileList(p.Files), rewindFileList(p.Unsaved)
	return out
}

func rewindFileList(cs []gitstat.Change) []proto.RewindFile {
	var out []proto.RewindFile
	for _, c := range cs {
		out = append(out, proto.RewindFile{Path: c.Path, From: c.From, Status: c.Status})
	}
	return out
}

// rewindPoint is the tab and the checkpoint a rewind of chat id to before
// turn goes back to: a chat tab's turn names its checkpoint; an agent
// pane's is the one its prompt's hook took, found by the prompt's time.
func (d *Daemon) rewindPoint(ctx context.Context, id, turn string) (ws, rev string, err error) {
	if pc, l, ok := d.paneChatOf(id); ok {
		if pc.prov != model.ProviderClaude {
			return "", "", chatError{"unsupported", "only a Claude Code pane rewinds; rewind " + providerName(pc.prov) + " in its terminal"}
		}
		if busy(l.act.State) {
			return "", "", session.ErrBusy
		}
		pc.mu.Lock()
		k := rewindTarget(pc.snap.Content, turn)
		var at string
		if k >= 0 {
			at = pc.snap.Content[k].CreatedAt
		}
		pc.mu.Unlock()
		if k < 0 {
			return "", "", invalid("no prompt starts turn %s", turn)
		}
		d.mu.Lock()
		if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id }); i >= 0 {
			ws = d.st.Panes[i].WorkspaceID
		}
		d.mu.Unlock()
		t, perr := time.Parse(time.RFC3339Nano, at)
		if dir := d.tabDir(ws); perr == nil && dir != "" {
			rev, err = gitstat.CheckpointNear(ctx, dir, ws, t)
		}
		return ws, rev, err
	}
	c, err := d.chat(id)
	if err != nil {
		return "", "", err
	}
	if !c.do(func() {
		var i int
		if i, err = turnIndex(c.snap, turn); err != nil {
			return
		}
		if c.snap.Busy() {
			err = session.ErrBusy
			return
		}
		ws, rev = c.cfg.Workspace, c.snap.Turns[i].CheckpointID
	}) {
		err = chatError{"not-found", "chat " + id + " is closed"}
	}
	return ws, rev, err
}
