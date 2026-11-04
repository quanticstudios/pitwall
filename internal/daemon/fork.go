package daemon

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
)

func (d *Daemon) tryForkPane(ctx context.Context, m proto.ForkPane) error {
	err := d.forkPane(ctx, m)
	if err != nil {
		log.Printf("fork: %q", err)
		d.mu.Lock()
		d.st.Notice = "Fork not started: " + err.Error()
		d.changed()
		d.mu.Unlock()
	} else {
		d.mu.Lock()
		if strings.HasPrefix(d.st.Notice, "Fork not started: ") {
			d.st.Notice = ""
			d.changed()
		}
		d.mu.Unlock()
	}
	return err
}

func (d *Daemon) forkPane(ctx context.Context, m proto.ForkPane) error {
	d.mu.Lock()
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == m.Pane })
	if i < 0 {
		d.mu.Unlock()
		return fmt.Errorf("no pane %s", m.Pane)
	}
	stored := d.st.Panes[i]
	p := d.liveCwd(stored)
	sourceTab := d.workspace(p.WorkspaceID)
	if sourceTab == nil {
		d.mu.Unlock()
		return fmt.Errorf("pane %s has no tab", p.ID)
	}
	w := *sourceTab
	parent, after, session := "", w.ID, w.SessionID
	if caller := d.paneTab(m.ParentPane); caller != nil {
		parent, after, session = caller.ID, caller.ID, caller.SessionID
	}
	if m.Name != "" && d.nameTaken(session, m.Name, "") {
		d.mu.Unlock()
		return fmt.Errorf("a tab is already named %s", m.Name)
	}
	d.mu.Unlock()
	cmd, err := store.ForkCmd(p)
	if err != nil {
		return err
	}
	dir := cmp.Or(p.Cwd, w.Path)
	selector := store.CodexCwd(p)
	relative := selector != "" && !filepath.IsAbs(selector)
	if cwd := selector; cwd != "" {
		if !filepath.IsAbs(cwd) {
			cwd = filepath.Join(stored.Cwd, cwd)
		}
		dir = cwd
	}
	fallback := !isDir(dir)
	if fallback {
		dir = w.Path
	}
	var root string
	var wt config.WorktreeSettings
	var problems []error
	if m.Worktree != "" {
		source, ok := d.o.RepoRoot(ctx, dir)
		if !ok {
			return fmt.Errorf("%s is not in a git repository", dir)
		}
		trees, err := gitstat.ListWorktrees(ctx, source)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(trees, func(t gitstat.Worktree) bool { return filepath.Clean(t.Path) == filepath.Clean(source) })
		if i < 0 {
			return fmt.Errorf("no worktree at %s", source)
		}
		root = trees[0].Path
		if dir, _, wt, problems, err = d.worktree(ctx, root, m.Worktree, model.WorktreeFrom{Ref: trees[i].Head}); err != nil {
			return err
		}
	}
	if p.Provider == model.ProviderCodex && (m.Worktree != "" || fallback || relative || selector == "") {
		cmd, err = store.ForkCmdIn(p, dir)
		if err != nil {
			return err
		}
	}
	typed, err := setupInput(wt)
	if err != nil {
		problems = append(problems, err)
	}
	var id string
	err = d.addSession(ctx, proto.NewSession{Name: m.Name, Cwd: dir, Cmd: cmd, Env: p.Env}, after, nil, func(n *model.Workspace) {
		id, n.Parent, n.Profile = n.ID, parent, w.Profile
		n.Ports = w.Ports
		if root != "" {
			n.WorktreeRoot, n.Ports = root, portBlock(d.st.Workspaces, wt.PortBase, wt.PortStep)
		}
	})
	if err != nil || typed == nil && len(problems) == 0 {
		return err
	}
	return d.worktreeSetup(id, typed, problems)
}
