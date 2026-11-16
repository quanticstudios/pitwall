package main

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/teardown"
)

const worktreeUsage = `usage:
  pitwall worktree ls [--all]   this repo's worktrees: tab, branch, changes, and commits
                                ahead of and behind the default branch (--all: every
                                repo pitwall has a group for)
  pitwall worktree new <name> [--from <ref> | --branch <branch> | --pr <n>]
                       [--preset <name> | --lane <lane>] [--guard]
                       [--host <name> [--repo <path>] [--id <id>]] [-- cmd args...]
                                open a tab in a new worktree, <repo>/.worktrees/<name>:
                                a new branch <name> off ref (default: the default
                                branch), an existing local or remote branch, or GitHub
                                pull request n as branch pr-<n>; cmd runs in it, else
                                the tab opens empty. --preset runs a [[agents.preset]]
                                with cmd's args after its own; --guard denies the agent
                                pushes and merges. --lane (fleet mode) runs the lane's
                                preset with the words after -- as one brief, the worker
                                contract before it, on the lane's host if it has one.
                                --host starts it on that [[hosts]] machine, in the repo
                                at the same path under its home folder or at --repo,
                                from this machine's commit, and prints #<host>:N;
                                --host local keeps a lane's worker here. --id finishes
                                a start that lost its connection. Prints the tab's #
  pitwall worktree lanes        the fleet lanes: small, normal and big, each with its
                                preset and timebox
  pitwall worktree rm <name> [--force] [--teardown]
                                remove the worktree and close its tab; --force when it
                                has changes, skipping teardown. --teardown approves the
                                repo teardown command this time. Its branch stays
  pitwall worktree prune        forget deleted worktrees and list the ones under
                                .worktrees that no tab uses`

func runWorktree(args []string, in *os.File, out, errOut io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := worktreeCommand(ctx, args, in, out, errOut); err != nil {
		fmt.Fprintln(errOut, "pitwall:", err)
		return 1
	}
	return 0
}

func worktreeCommand(ctx context.Context, args []string, in *os.File, out, errOut io.Writer) error {
	if len(args) == 0 {
		return errors.New(worktreeUsage)
	}
	command, args := args[0], args[1:]
	var cmd []string
	dashes := false
	if i := slices.Index(args, "--"); i >= 0 && command == "new" {
		args, cmd, dashes = args[:i], args[i+1:], true
	}
	flags := flag.NewFlagSet("worktree "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var all, force, guarded, approve bool
	var from, branch, preset, lane, host, tabID, repo string
	var pr int
	switch command {
	case "ls":
		flags.BoolVar(&all, "all", false, "")
	case "new":
		flags.StringVar(&from, "from", "", "")
		flags.StringVar(&branch, "branch", "", "")
		flags.IntVar(&pr, "pr", 0, "")
		flags.StringVar(&preset, "preset", "", "")
		flags.StringVar(&lane, "lane", "", "")
		flags.BoolVar(&guarded, "guard", false, "")
		flags.StringVar(&host, "host", "", "")
		flags.StringVar(&tabID, "id", "", "")
		flags.StringVar(&repo, "repo", "", "")
	case "rm":
		flags.BoolVar(&force, "force", false, "")
		flags.BoolVar(&approve, "teardown", false, "")
	case "prune":
	case "lanes":
		if len(args) > 0 {
			return errors.New(worktreeUsage)
		}
		return listLanes(out)
	default:
		return fmt.Errorf("unknown command %q\n%s", command, worktreeUsage)
	}
	args, err := parseFlags(flags, args)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, worktreeUsage)
	}
	if (command == "ls" || command == "prune") != (len(args) == 0) || len(args) > 1 || dashes && len(cmd) == 0 && preset == "" {
		return errors.New(worktreeUsage)
	}
	if lane != "" {
		var laneHost string
		s, _ := config.Load()
		if preset, cmd, laneHost, err = control.Lane(s, lane, preset, cmd); err != nil {
			return err
		}
		host = cmp.Or(host, laneHost)
	}
	if host == "local" { // this machine, over a lane's host
		host = ""
	}
	if host == "" && (tabID != "" || repo != "") {
		return errors.New("--id and --repo name a worker on another host and go with --host")
	}
	if host != "" && branch != "" {
		return errors.New("--branch cannot be used with --host: the branch may differ on the two machines. Use --from <branch> for a new branch off it")
	}
	// An agent's worker gets the guard when [agents] guard_workers says so.
	if !guarded {
		s, _ := config.Load()
		guarded = control.WorkerGuard(s, envCaller(""), cmd, preset)
	}
	var vars []string
	if cmd, vars, err = launch(preset, guarded, cmd, host == ""); err != nil {
		return err
	}
	var root string
	if !all {
		if root, err = control.MainRoot(ctx, "."); err != nil {
			return err
		}
	}
	// The daemon knows the tabs; without it every worktree has none.
	conn, err := dialCLI()
	var state model.State
	level := 0
	switch {
	case errors.Is(err, errNotRunning) && command != "new" && !all:
	case err != nil:
		return err
	default:
		defer conn.Close()
		cancelSocket := context.AfterFunc(ctx, func() { conn.Close() })
		defer cancelSocket()
		m, err := syncMsg(conn)
		if err != nil {
			return err
		}
		state, level = m.State, m.Level
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
		f, err := control.WorktreeFrom(ctx, root, from, branch, pr)
		if err != nil {
			return err
		}
		if err := varsLevel(vars, level); err != nil {
			return err
		}
		if host != "" {
			m := proto.StateMsg{State: state, Level: level}
			return newWorktreeOnHost(ctx, conn, out, errOut, m, root, host, repo, tabID, args[0], f, cmd, vars, lane)
		}
		if lane != "" && level < proto.LaneLevel {
			return errors.New("the pitwall daemon running is older than this pitwall and would not keep the lane's timebox; it works once the daemon restarts")
		}
		return newWorktree(conn, out, state, level, root, args[0], f, cmd, vars, lane)
	case "rm":
		return removeWorktree(ctx, conn, in, errOut, state, root, args[0], force, approve, level)
	}
	if err := gitstat.Prune(ctx, root); err != nil {
		return err
	}
	orphans, err := gitstat.Orphans(ctx, root, func(p string) bool { return len(state.WorktreeUsers(p, "")) > 0 })
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

// listLanes prints the fleet lanes as config.toml sets them.
func listLanes(out io.Writer) error {
	s, _ := config.Load()
	if !s.Fleet.On {
		_, err := fmt.Fprintln(out, "Fleet mode is off: turn it on in Settings, Fleet, or set enabled = true under [fleet] in config.toml.")
		return err
	}
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "LANE\tPRESET\tTIMEBOX\tFOR")
	for i, l := range s.Fleet.Lanes {
		preset, tb := l.Preset, "off"
		if preset == "" {
			preset = "(none: lane off)"
		}
		if l.Timebox > 0 {
			tb = fmt.Sprintf("%gm", l.Timebox.Minutes())
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", l.Name, preset, tb, config.LaneDescs[i])
	}
	return w.Flush()
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
// Run from an agent's pane, the tab joins the agent's group instead, and
// no group is made. With no session the daemon makes one, as pitwall new
// does.
func newWorktree(conn *cliConn, out io.Writer, state model.State, level int, root, name string, from model.WorktreeFrom, cmd, vars []string, lane string) error {
	session, _ := currentSession(state, "")
	msg := proto.NewWorkspace{Name: name, From: from, Cmd: cmd, Env: vars, ParentPane: os.Getenv("PITWALL_PANE"), Lane: lane}
	if parent, err := resolveTab(state, "", ""); err == nil && level >= proto.WorkspaceRootLevel {
		session.ID, msg.Root = parent.SessionID, root
	} else {
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
		session.ID, msg.ProjectID = p.SessionID, p.ID
	}
	after, err := syncCLI(conn, msg)
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
func removeWorktree(ctx context.Context, conn *cliConn, in *os.File, errOut io.Writer, state model.State, root, name string, force, approve bool, level int) error {
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
	except := ""
	if w := tabAt(state, path); w != nil {
		except = w.ID
	}
	if users := state.WorktreeUsers(path, except); len(users) > 0 {
		message := "worktree folder is also used by tabs " + strings.Join(users, ", ") + "; removing it leaves those tabs without their folder"
		if !force {
			return fmt.Errorf("%s; use --force to remove it", message)
		}
		fmt.Fprintln(errOut, "Warning: "+message+". Teardown is skipped with --force.")
	}
	_, pathErr := os.Stat(path)
	if !force && !errors.Is(pathErr, os.ErrNotExist) {
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
	var settings config.WorktreeSettings
	approved := ""
	if !force {
		settings, _ = config.LoadWorktrees(config.Path(), root)
		if settings.TeardownFromRepo {
			if approve {
				approved = settings.Teardown
			} else if teardownTerminal(in, state) {
				if _, err := fmt.Fprintf(errOut, "Repo teardown for %s:\n%q\nRun this command? [y/N] ", path, settings.Teardown); err != nil {
					return err
				}
				type response struct {
					answer string
					err    error
				}
				replies := make(chan response, 1)
				go func() { answer, err := bufio.NewReader(in).ReadString('\n'); replies <- response{answer, err} }()
				var answer string
				select {
				case <-ctx.Done():
					return ctx.Err()
				case reply := <-replies:
					if reply.err != nil {
						return reply.err
					}
					answer = reply.answer
				}
				if answer = strings.ToLower(strings.TrimSpace(answer)); answer == "y" || answer == "yes" {
					approved = settings.Teardown
				}
			}
		}
	}
	if w := tabAt(state, path); w != nil && w.WorktreeRoot != "" {
		if !force && level < proto.TeardownLevel && settings.Teardown != "" && settings.TeardownProblem == "" {
			return errors.New("this tab's teardown needs a daemon with teardown support; restart the daemon first")
		}
		if !force && level < proto.TeardownLevel && settings.TeardownProblem != "" {
			notice, _ := teardown.Run(ctx, path, settings, model.PortBlock{}, "")
			fmt.Fprintln(errOut, notice)
		}
		after, err := syncCLI(conn, proto.DeleteWorkspace{WorkspaceID: w.ID, Force: force, SkipTeardown: force, Teardown: approved})
		if err == nil && after.Notice != state.Notice && after.Notice != "" {
			fmt.Fprintln(errOut, after.Notice)
		}
		return err
	}
	notice := ""
	if !force {
		ports, err := teardown.PortsFor(path, state.Workspaces)
		if err != nil {
			return err
		}
		notice, err = teardown.Run(ctx, path, settings, ports, approved)
		if err != nil {
			return err
		}
		if notice != "" && !errors.Is(pathErr, os.ErrNotExist) {
			files, err := gitstat.Status(ctx, path)
			if err == nil && len(files) > 0 {
				err = fmt.Errorf("uncommitted files: %s", strings.Join(files, ", "))
			}
			if err != nil {
				return fmt.Errorf("worktree kept at %s: removal failed: %w\n%s", path, err, notice)
			}
		}
	}
	if w := tabAt(state, path); w != nil {
		if _, err := syncCLI(conn, proto.KillSession{WorkspaceID: w.ID}); err != nil {
			return err
		}
	}
	if err := gitstat.RemoveWorktree(ctx, root, path, false, force); err != nil {
		if notice != "" {
			return fmt.Errorf("worktree kept at %s: removal failed: %w\n%s", path, err, notice)
		}
		return err
	}
	if notice != "" {
		fmt.Fprintln(errOut, notice)
	}
	return nil
}

// teardownTerminal excludes agent panes and PTYs without the caller's foreground terminal.
func teardownTerminal(in *os.File, state model.State) bool {
	for _, p := range state.Panes {
		if p.ID == os.Getenv("PITWALL_PANE") && p.Provider != "" && p.Provider != model.ProviderTerminal {
			return false
		}
	}
	return foregroundTerminal(in)
}
