package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

const worktreeUsage = `usage:
  pitwall worktree ls [--all]   this repo's worktrees: tab, branch, changes, and commits
                                ahead of and behind the default branch (--all: every
                                repo pitwall has a group for)
  pitwall worktree new <name> [--from <ref> | --branch <branch> | --pr <n>] [-- cmd args...]
                                open a tab in a new worktree, <repo>/.worktrees/<name>:
                                a new branch <name> off ref (default: the default
                                branch), an existing local or remote branch, or GitHub
                                pull request n as branch pr-<n>; cmd runs in it, else
                                the tab opens empty. Prints the tab's #
  pitwall worktree rm <name> [--force]
                                remove the worktree and close its tab; --force when it
                                has changes. Its branch stays
  pitwall worktree prune        forget deleted worktrees and list the ones under
                                .worktrees that no tab uses`

func runWorktree(args []string, out, errOut io.Writer) int {
	if err := worktreeCommand(context.Background(), args, out, errOut); err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}
	return 0
}

func worktreeCommand(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New(worktreeUsage)
	}
	command, args := args[0], args[1:]
	var cmd []string
	if i := slices.Index(args, "--"); i >= 0 && command == "new" {
		if args, cmd = args[:i], args[i+1:]; len(cmd) == 0 {
			return errors.New(worktreeUsage)
		}
		if err := absCmd(cmd); err != nil {
			return err
		}
	}
	flags := flag.NewFlagSet("worktree "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var all, force bool
	var from, branch string
	var pr int
	switch command {
	case "ls":
		flags.BoolVar(&all, "all", false, "")
	case "new":
		flags.StringVar(&from, "from", "", "")
		flags.StringVar(&branch, "branch", "", "")
		flags.IntVar(&pr, "pr", 0, "")
	case "rm":
		flags.BoolVar(&force, "force", false, "")
	case "prune":
	default:
		return fmt.Errorf("unknown command %q\n%s", command, worktreeUsage)
	}
	args, err := parseFlags(flags, args)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, worktreeUsage)
	}
	if (command == "ls" || command == "prune") != (len(args) == 0) || len(args) > 1 {
		return errors.New(worktreeUsage)
	}
	var root string
	if !all {
		if root, err = mainRoot(ctx, "."); err != nil {
			return err
		}
	}
	// The daemon knows the tabs; without it every worktree has none.
	conn, err := dialCLI()
	var state model.State
	switch {
	case errors.Is(err, errNotRunning) && command != "new" && !all:
	case err != nil:
		return err
	default:
		defer conn.Close()
		if state, err = syncCLI(conn); err != nil {
			return err
		}
	}
	switch command {
	case "ls":
		roots := []string{root}
		if all {
			roots = nil
			for _, p := range state.Projects {
				if p.Kind == model.ProjectGit && !slices.Contains(roots, p.Root) {
					roots = append(roots, p.Root)
				}
			}
		}
		return listWorktrees(ctx, out, state, roots)
	case "new":
		f, err := worktreeFrom(ctx, root, from, branch, pr)
		if err != nil {
			return err
		}
		return newWorktree(conn, out, state, root, args[0], f, cmd)
	case "rm":
		return removeWorktree(ctx, conn, errOut, state, root, args[0], force)
	}
	if err := gitstat.Prune(ctx, root); err != nil {
		return err
	}
	orphans, err := gitstat.Orphans(ctx, root, func(p string) bool { return tabAt(state, p) != nil })
	if err != nil {
		return err
	}
	if len(orphans) == 0 {
		_, err = fmt.Fprintln(out, "Every worktree under .worktrees has a tab.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tBRANCH\tCHANGES\tLAST COMMIT")
	for _, o := range orphans {
		changes, last := "clean", "-"
		if o.Dirty {
			changes = "changed"
		}
		if !o.Committed.IsZero() {
			last = ago(time.Since(o.Committed))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", filepath.Base(o.Path), o.Branch, changes, last)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, "No tab uses these. pitwall worktree rm <name> deletes one.")
	return err
}

// mainRoot is the main worktree of the repo at dir, also from inside a
// linked worktree.
func mainRoot(ctx context.Context, dir string) (string, error) {
	trees, err := gitstat.ListWorktrees(ctx, dir)
	if err != nil || len(trees) == 0 {
		return "", errors.New("not in a git repository")
	}
	return trees[0].Path, nil
}

// worktreeFrom turns new's flags into what the worktree starts from. A
// --branch with no local branch of that name may name a remote one.
func worktreeFrom(ctx context.Context, root, from, branch string, pr int) (model.WorktreeFrom, error) {
	set := 0
	for _, on := range []bool{from != "", branch != "", pr != 0} {
		if on {
			set++
		}
	}
	if set > 1 {
		return model.WorktreeFrom{}, errors.New("use one of --from, --branch and --pr")
	}
	switch {
	case pr != 0:
		return model.WorktreeFrom{Kind: model.FromPR, PR: pr}, nil
	case branch != "":
		refs, err := gitstat.ListRefs(ctx, root)
		if err != nil {
			return model.WorktreeFrom{}, err
		}
		if slices.Contains(refs.Local, branch) {
			return model.WorktreeFrom{Kind: model.FromBranch, Ref: branch}, nil
		}
		if slices.Contains(refs.Remote, branch) {
			return model.WorktreeFrom{Kind: model.FromRemote, Ref: branch}, nil
		}
		return model.WorktreeFrom{}, fmt.Errorf("no local or remote branch %q", branch)
	}
	return model.WorktreeFrom{Ref: from}, nil
}

// tabAt is the tab started in path, if any.
func tabAt(state model.State, path string) *model.Workspace {
	path = filepath.Clean(path)
	for i, w := range state.Workspaces {
		if filepath.Clean(w.Path) == path {
			return &state.Workspaces[i]
		}
	}
	return nil
}

// listWorktrees prints a row per worktree of each repo in roots.
func listWorktrees(ctx context.Context, out io.Writer, state model.State, roots []string) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "TAB\tBRANCH\tCHANGES\tAHEAD\tBEHIND\tPATH")
	home, _ := os.UserHomeDir()
	for _, root := range roots {
		trees, err := gitstat.ListWorktrees(ctx, root)
		if err != nil {
			return err
		}
		for _, t := range trees {
			tab, branch, changes, ahead, behind := "-", t.Branch, "clean", "-", "-"
			if ws := tabAt(state, t.Path); ws != nil {
				tab = tabTitle(*ws)
			}
			if branch == "" {
				branch = "(detached)"
			}
			if files, err := gitstat.Status(ctx, t.Path); err != nil {
				changes = "?"
			} else if len(files) > 0 {
				changes = plural(len(files), "file")
			}
			if s, err := gitstat.Stats(ctx, t.Path); err == nil && s.Base != "" {
				ahead, behind = strconv.Itoa(s.Ahead), strconv.Itoa(s.Behind)
			}
			path := t.Path
			if home != "" && strings.HasPrefix(path, home+string(os.PathSeparator)) {
				path = "~" + strings.TrimPrefix(path, home)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", tab, branch, changes, ahead, behind, path)
		}
	}
	return w.Flush()
}

// newWorktree opens a tab in a new worktree of root, in the repo's group
// of the current session, which it makes when missing, and prints its #.
// With no session the daemon makes one, as pitwall new does.
func newWorktree(conn *cliConn, out io.Writer, state model.State, root, name string, from model.WorktreeFrom, cmd []string) error {
	session, _ := currentSession(state, "")
	group := func(st model.State) *model.Project {
		for i, p := range st.Projects {
			if (session.ID == "" || p.SessionID == session.ID) && p.Kind == model.ProjectGit && p.Root == root {
				return &st.Projects[i]
			}
		}
		return nil
	}
	p := group(state)
	if p == nil {
		var err error
		if state, err = syncCLI(conn, proto.AddProject{Path: root, SessionID: session.ID}); err != nil {
			return err
		}
		if p = group(state); p == nil {
			return fmt.Errorf("pitwall did not group %s as a git repository", root)
		}
	}
	pid := p.ID
	session.ID = p.SessionID
	after, err := syncCLI(conn, proto.NewWorkspace{ProjectID: pid, Name: name, From: from, Cmd: cmd})
	var added *model.Workspace
	for i, w := range after.Workspaces {
		if !slices.ContainsFunc(state.Workspaces, func(old model.Workspace) bool { return old.ID == w.ID }) {
			added = &after.Workspaces[i]
		}
	}
	if added == nil {
		return cmp.Or(err, errors.New("cannot identify the new tab from daemon state"))
	}
	n := slices.IndexFunc(numbered(after, session.ID), func(w model.Workspace) bool { return w.ID == added.ID })
	if _, perr := fmt.Fprintf(out, "#%d\n", n+1); perr != nil {
		return perr
	}
	return err // the tab is made, but setup had a problem
}

// removeWorktree removes the worktree name of root: through the daemon
// when a tab pitwall made it for uses it, else here, after closing any
// other tab in it. Without force it lists the changes and stops.
func removeWorktree(ctx context.Context, conn *cliConn, errOut io.Writer, state model.State, root, name string, force bool) error {
	trees, err := gitstat.ListWorktrees(ctx, root)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, ".worktrees")
	i := slices.IndexFunc(trees, func(t gitstat.Worktree) bool {
		return filepath.Dir(t.Path) == dir && (filepath.Base(t.Path) == gitstat.Slug(name) || t.Branch == name)
	})
	if i < 0 {
		return fmt.Errorf("no worktree %q under %s (see pitwall worktree ls)", name, dir)
	}
	path := trees[i].Path
	if !force {
		files, err := gitstat.Status(ctx, path)
		if err != nil {
			return err
		}
		if len(files) > 0 {
			for _, f := range files {
				fmt.Fprintln(errOut, "  "+f)
			}
			return fmt.Errorf("%s has changes, listed above; --force removes them", path)
		}
	}
	if w := tabAt(state, path); w != nil {
		if w.WorktreeRoot != "" {
			_, err := syncCLI(conn, proto.DeleteWorkspace{WorkspaceID: w.ID, Force: force})
			return err
		}
		if _, err := syncCLI(conn, proto.KillSession{WorkspaceID: w.ID}); err != nil {
			return err
		}
	}
	return gitstat.RemoveWorktree(ctx, root, path, false, force)
}
