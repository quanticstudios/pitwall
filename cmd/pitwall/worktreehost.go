package main

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// newWorktreeOnHost is worktree new --host: a worker in a new worktree of
// the repository at root, in its folder on host (hostRepo). A new branch
// starts from this machine's commit, pushed there first, so commits not
// pushed anywhere reach it too. The tab joins the current session's part
// on host, under the calling agent's tab, as new --host does, and carries
// this machine's timebox and nudge for lane.
func newWorktreeOnHost(ctx context.Context, conn *cliConn, out, errOut io.Writer, m proto.StateMsg, root, host, repo, id, name string, from model.WorktreeFrom, cmd, vars []string, lane string) error {
	session, err := currentSession(m.State, "")
	if err := sharesHere(m, err); err != nil {
		return err
	}
	dir, err := hostRepo(root, repo, host)
	if err != nil {
		return err
	}
	wt := &hostWorktree{msg: proto.NewWorkspace{Root: dir, From: from, Lane: lane}}
	if url, err := gitstat.Command(ctx, root, "remote", "get-url", "origin").Output(); err == nil {
		wt.clone = string(bytes.TrimSpace(url))
	}
	if from.Kind == model.FromNew {
		sha, err := gitstat.Commit(ctx, root, from.Ref)
		if err != nil {
			return err
		}
		wt.msg.From = model.WorktreeFrom{Ref: sha}
		wt.push = func(id string) error { return pushBase(ctx, host, root, dir, sha, id) }
	}
	if lane != "" {
		s, _ := config.Load()
		wt.msg.Timebox, wt.msg.Nudge = s.Fleet.Timebox(lane), s.Fleet.Nudge
	}
	parent := parentHere(m.State, session.ID)
	num, err := startOnHost(conn, m.State.Daemon, &session, host, proto.NewSession{Name: name, Cwd: dir, Cmd: cmd, Env: vars, ID: id, ParentTab: parent}, false, wt)
	if num == "" {
		return err
	}
	if parent != "" && !shownInWindows(host) {
		fmt.Fprintf(errOut, "pitwall: %s is not shown in windows; set window = true under its [[hosts]] entry to see its workers in the sidebar\n", host)
	}
	if _, perr := fmt.Fprintf(out, "#%s\n", num); perr != nil {
		return perr
	}
	return err // the tab is made, but setup had a problem
}

// hostRepo is the folder on host of the repository at root here: repo
// when given, under the home folder there unless it starts with / or ~/;
// else root with this machine's home folder swapped for ~.
func hostRepo(root, repo, host string) (string, error) {
	if repo != "" {
		if strings.HasPrefix(repo, "/") || strings.HasPrefix(repo, "~/") {
			return repo, nil
		}
		return "~/" + repo, nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, root); err == nil && rel != "." && filepath.IsLocal(rel) {
			return "~/" + filepath.ToSlash(rel), nil
		}
	}
	return "", fmt.Errorf("%s is outside your home folder, so pitwall cannot tell where it is on %s: pass --repo <path on %s>", root, host, host)
}

// pushBase pushes commit sha of the repository at root to dir on host, as
// gitstat.BaseRef(id), over the shared ssh connection. Tests replace it.
var pushBase = func(ctx context.Context, host, root, dir, sha, id string) error {
	s, _ := config.Load()
	if err := agentHost(host, s.Hosts); err != nil {
		return err
	}
	r, err := newSSHHost(host, s.Hosts)
	if err != nil {
		return err
	}
	r.batch = os.Getenv("PITWALL_PANE") != ""
	args := pushArgs(r.dest, dir, sha, id)
	cmd := gitstat.Command(ctx, root, args...)
	cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND="+r.gitSSH())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := cmp.Or(lastLine(stderr.String()), err.Error())
		if !strings.Contains(stderr.String(), "does not appear to be a git repository") {
			return fmt.Errorf("push the worktree's commit to %s: %s", host, msg)
		}
		url := "<url>"
		if out, err := gitstat.Command(ctx, root, "remote", "get-url", "origin").Output(); err == nil && len(bytes.TrimSpace(out)) > 0 {
			url = string(bytes.TrimSpace(out))
		}
		return fmt.Errorf("%s has no repository at %s (pass --repo <path on %s> if it is elsewhere). Clone it there with:\n  ssh %s git clone %s %s", host, dir, host, r.dest, url, dir)
	}
	return nil
}

// pushArgs are git's arguments that push sha to dir on dest as tab id's
// base ref, which only that tab uses, so the push overwrites it.
// --no-verify: this machine's pre-push hooks are for its own branches.
func pushArgs(dest, dir, sha, id string) []string {
	return []string{"push", "--no-verify", "--quiet", dest + ":" + dir, "+" + sha + ":" + gitstat.BaseRef(id)}
}
