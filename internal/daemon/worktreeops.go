package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/teardown"
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
	st := model.State{Workspaces: slices.Clone(d.st.Workspaces)}
	d.mu.Unlock()
	var out []model.Orphan
	var errs []error
	for _, root := range roots {
		o, err := d.o.Orphans(ctx, root, func(p string) bool { return len(st.WorktreeUsers(p, "")) > 0 })
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", root, err))
		}
		for i := range o {
			o[i].Teardown = d.repoTeardown(root)
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
		d.mu.Lock()
		r.UsedBy = d.st.WorktreeUsers(ws.Path, ws.ID)
		d.mu.Unlock()
		r.Teardown = d.repoTeardown(ws.WorktreeRoot)
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
	users := d.st.WorktreeUsers(path, "")
	workspaces := slices.Clone(d.st.Workspaces)
	d.mu.Unlock()
	switch {
	case !known:
		return fmt.Errorf("no git group at %s", m.Root)
	case filepath.Dir(path) != filepath.Join(m.Root, ".worktrees"):
		return fmt.Errorf("%s is not under %s", path, filepath.Join(m.Root, ".worktrees"))
	case len(users) > 0:
		err := fmt.Errorf("tabs %s use %s", strings.Join(users, ", "), path)
		d.mu.Lock()
		d.st.Notice = "Worktree not removed: " + err.Error()
		d.changed()
		d.mu.Unlock()
		return err
	}
	ports, err := teardown.PortsFor(path, workspaces)
	if err != nil && !m.SkipTeardown {
		return err
	}
	return d.removeWorktree(ctx, m.Root, path, ports, false, m.Force, m.SkipTeardown, m.Teardown)
}

// repoTeardown is the command the UI must show before it can approve it.
func (d *Daemon) repoTeardown(root string) string {
	if d.o.Worktrees != nil {
		s, _ := d.o.Worktrees(root)
		if s.TeardownFromRepo {
			return s.Teardown
		}
	}
	return ""
}

// removeWorktree runs outside mu, in a removal worker or the requesting
// CLI client's goroutine. Only one removal of a path may run at a time.
func (d *Daemon) removeWorktree(ctx context.Context, root, path string, ports model.PortBlock, branch, force, skip bool, approved string) error {
	d.mu.Lock()
	if d.worktreeDeletes[path] {
		d.mu.Unlock()
		return fmt.Errorf("worktree removal already running: %s", path)
	}
	if d.worktreeDeletes == nil {
		d.worktreeDeletes = map[string]bool{}
	}
	d.worktreeDeletes[path] = true
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.worktreeDeletes, path); d.mu.Unlock() }()

	notice := ""
	if !skip && d.o.Worktrees != nil {
		s, _ := d.o.Worktrees(root)
		if !force && s.Teardown != "" && s.TeardownProblem == "" && (!s.TeardownFromRepo || approved == s.Teardown) {
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				files, err := gitstat.Status(ctx, path)
				if err == nil && len(files) > 0 {
					err = fmt.Errorf("uncommitted files: %s", strings.Join(files, ", "))
				}
				if err != nil {
					err = fmt.Errorf("worktree kept at %s; teardown not run: %w", path, err)
					d.worktreeNotice(ctx, err.Error(), true)
					return err
				}
			}
		}
		var err error
		notice, err = teardown.Run(ctx, path, s, ports, approved)
		if err != nil {
			d.worktreeNotice(ctx, err.Error(), true)
			return err
		}
	}
	err := d.o.RemoveWorktree(ctx, root, path, branch, force)
	if errors.Is(err, gitstat.ErrBranchKept) {
		if notice != "" {
			d.worktreeNotice(ctx, notice, false)
		}
		return fmt.Errorf("worktree removed at %s; %w", path, err)
	}
	if err != nil {
		err = fmt.Errorf("worktree kept at %s: removal failed: %w", path, err)
		if notice != "" {
			err = fmt.Errorf("%w\n%s", err, notice)
			d.worktreeNotice(ctx, err.Error(), true)
		}
		return err
	}
	if notice != "" {
		d.worktreeNotice(ctx, notice, false)
	}
	return nil
}

type worktreeNoticeKey struct{}

func (d *Daemon) worktreeNotice(ctx context.Context, text string, failed bool) {
	if report, ok := ctx.Value(worktreeNoticeKey{}).(func(string, bool)); ok {
		report(text, failed)
		return
	}
	d.setWorktreeNotice(text)
}

func (d *Daemon) setWorktreeNotice(text string) {
	log.Printf("%s", text)
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.Notice = text
	d.changed()
}
