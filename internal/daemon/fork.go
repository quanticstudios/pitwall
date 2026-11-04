package daemon

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/store"
)

func (d *Daemon) forkPane(ctx context.Context, m proto.ForkPane) error {
	d.mu.Lock()
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == m.Pane })
	if i < 0 {
		d.mu.Unlock()
		return fmt.Errorf("no pane %s", m.Pane)
	}
	p := d.st.Panes[i]
	w := *d.workspace(p.WorkspaceID)
	if m.Name != "" && d.nameTaken(w.SessionID, m.Name, "") {
		d.mu.Unlock()
		return fmt.Errorf("a tab is already named %s", m.Name)
	}
	d.mu.Unlock()
	cmd, err := store.ForkCmd(p)
	if err != nil {
		return err
	}
	dir := cmp.Or(p.Cwd, w.Path)
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
	typed, err := setupInput(wt)
	if err != nil {
		problems = append(problems, err)
	}
	var id string
	err = d.addSession(ctx, proto.NewSession{Name: m.Name, Cwd: dir, Cmd: cmd, Env: p.Env}, w.ID, nil, func(n *model.Workspace) {
		id, n.Parent, n.Profile = n.ID, w.Parent, w.Profile
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
