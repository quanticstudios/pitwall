package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// pruneOrphans is Options.Orphans over git.
func pruneOrphans(ctx context.Context, root string, used func(string) bool) ([]model.Orphan, error) {
	if err := gitstat.Prune(ctx, root); err != nil {
		return nil, err
	}
	return gitstat.Orphans(ctx, root, used)
}

// orphans runs Options.Orphans in every git group's repo, against the
// folders of every tab.
func (d *Daemon) orphans(ctx context.Context) ([]model.Orphan, error) {
	if d.o.Orphans == nil {
		return nil, nil
	}
	d.mu.Lock()
	var roots []string
	for _, p := range d.st.Projects {
		if p.Kind == model.ProjectGit && !slices.Contains(roots, p.Root) {
			roots = append(roots, p.Root)
		}
	}
	used := map[string]bool{}
	for _, w := range d.st.Workspaces {
		used[filepath.Clean(w.Path)] = true
	}
	d.mu.Unlock()
	var out []model.Orphan
	var errs []error
	for _, root := range roots {
		o, err := d.o.Orphans(ctx, root, func(p string) bool { return used[filepath.Clean(p)] })
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", root, err))
		}
		out = append(out, o...)
	}
	return out, errors.Join(errs...)
}

// noteOrphans prunes at start, and shows a notice when worktrees pitwall
// made have no tab, unless one already shows.
func (d *Daemon) noteOrphans(ctx context.Context) {
	o, err := d.orphans(ctx)
	if err != nil {
		log.Printf("worktree prune: %q", err)
	}
	if len(o) == 0 {
		return
	}
	log.Printf("%d worktrees have no tab", len(o))
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.st.Notice == "" && !d.closing {
		d.st.Notice = orphanNotice(len(o))
		d.changed()
	}
}

func orphanNotice(n int) string {
	what := fmt.Sprintf("%d worktrees under .worktrees have", n)
	if n == 1 {
		what = "A worktree under .worktrees has"
	}
	return what + " no tab. Clean up worktrees, in the command palette, opens or deletes them."
}

// worktreeQuery answers q; see proto.WorktreeQuery.
func (d *Daemon) worktreeQuery(ctx context.Context, q proto.WorktreeQuery) proto.WorktreeInfo {
	r := proto.WorktreeInfo{Query: q}
	var err error
	switch {
	case q.ProjectID != "":
		d.mu.Lock()
		i := slices.IndexFunc(d.st.Projects, func(p model.Project) bool { return p.ID == q.ProjectID && p.Kind == model.ProjectGit })
		var root string
		if i >= 0 {
			root = d.st.Projects[i].Root
		}
		d.mu.Unlock()
		if i < 0 {
			err = fmt.Errorf("no git group %s", q.ProjectID)
			break
		}
		var refs gitstat.Refs
		refs, err = gitstat.ListRefs(ctx, root)
		r.Default, r.Local, r.Remote, r.GitHub = refs.Default, refs.Local, refs.Remote, refs.GitHub
	case q.WorkspaceID != "":
		d.mu.Lock()
		var ws model.Workspace
		w := d.workspace(q.WorkspaceID)
		if w != nil {
			ws = *w
		}
		d.mu.Unlock()
		if w == nil {
			err = fmt.Errorf("no workspace %s", q.WorkspaceID)
			break
		}
		if ws.WorktreeRoot == "" {
			break // deleting it leaves the folder alone
		}
		if r.Changed, err = gitstat.Status(ctx, ws.Path); err != nil {
			break
		}
		if branch, _ := d.o.Branch(ctx, ws.Path); branch != "" {
			var merged bool
			merged, err = gitstat.Merged(ctx, ws.WorktreeRoot, branch)
			r.Unmerged = !merged
		}
	case q.Orphans:
		r.Orphans, err = d.orphans(ctx)
	}
	if err != nil {
		r.Err = err.Error()
	}
	return r
}

// deleteWorktree removes an orphan: a worktree under a git group's
// .worktrees that no tab uses.
func (d *Daemon) deleteWorktree(ctx context.Context, m proto.DeleteWorktree) error {
	path := filepath.Clean(m.Path)
	d.mu.Lock()
	known := slices.ContainsFunc(d.st.Projects, func(p model.Project) bool { return p.Kind == model.ProjectGit && p.Root == m.Root })
	used := slices.ContainsFunc(d.st.Workspaces, func(w model.Workspace) bool { return filepath.Clean(w.Path) == path })
	d.mu.Unlock()
	switch {
	case !known:
		return fmt.Errorf("no git group at %s", m.Root)
	case filepath.Dir(path) != filepath.Join(m.Root, ".worktrees"):
		return fmt.Errorf("%s is not under %s", path, filepath.Join(m.Root, ".worktrees"))
	case used:
		return fmt.Errorf("a tab uses %s", path)
	}
	return d.o.RemoveWorktree(ctx, m.Root, path, false, m.Force)
}
