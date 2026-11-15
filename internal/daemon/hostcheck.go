package daemon

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// remoteTimeout bounds each call a check makes to origin or GitHub.
const remoteTimeout = 10 * time.Second

// CheckStart answers a proto.HostCheck with this process's PATH and
// environment, which are the daemon's own when the daemon runs it. With
// cmd empty it checks only repo; with repo empty, only cmd.
func CheckStart(ctx context.Context, m proto.HostCheck) []proto.HostProblem {
	var out []proto.HostProblem
	add := func(warn bool, fix, format string, args ...any) {
		out = append(out, proto.HostProblem{What: fmt.Sprintf(format, args...), Fix: fix, Warn: warn})
	}
	home, _ := os.UserHomeDir()
	repo := m.Repo
	if rest, ok := strings.CutPrefix(repo, "~/"); ok {
		repo = filepath.Join(home, rest) // as newWorkspace expands NewWorkspace.Root
	}
	if len(m.Cmd) > 0 {
		cwd, get := cmp.Or(repo, home), envGet(m.Env)
		provider := agent.Identify("", m.Cmd[0])
		key, dir := agentDir(provider, get, cwd)
		// why: panes start with exec.Command, which looks cmd[0] up on the daemon's own PATH.
		if _, err := exec.LookPath(m.Cmd[0]); err != nil {
			add(false, "install it, or add its folder to the PATH pitwall's daemon starts with (docs/remote.md)", "%s is not on the daemon's PATH", m.Cmd[0])
		} else {
			if err := guardGate(m.Cmd, m.Env, cwd); err != nil {
				add(false, "", "%v: %v", errUnguarded, err)
			} else if err := agent.GuardHooked(provider, dir); err != nil && key != "" {
				add(true, key+"="+dir+" pitwall hooks install", "pitwall's hooks are missing, so the worker's state will not show: %v", err)
			}
			if err := agent.LoggedIn(provider, dir, get); err != nil {
				add(true, "start "+string(provider)+" there once and log in", "%v; the worker may stop at a login screen", err)
			}
		}
	}
	if repo == "" {
		return out
	}
	if err := gitstat.Command(ctx, repo, "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		fix := "clone the repository there"
		if m.Clone != "" {
			fix = "git clone " + m.Clone + " " + m.Repo
		}
		add(false, fix+", or pass --repo with its path there", "no git repository at %s", m.Repo)
		return out
	}
	remote := func(args ...string) (int, string) {
		ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
		defer cancel()
		cmd := gitstat.Command(ctx, repo, args...)
		// What a worker's git push uses, failing instead of asking for a password.
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		if ctx.Err() != nil {
			return -1, fmt.Sprintf("no answer in %s", remoteTimeout)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode(), firstLine(stderr.String(), err)
		}
		if err != nil {
			return -1, err.Error()
		}
		return 0, ""
	}
	const login = "set up the ssh key or credential helper git uses for origin there, and accept origin's host key"
	// Exit 2 is a readable origin without a HEAD, such as an empty repository.
	if code, msg := remote("ls-remote", "--exit-code", "origin", "HEAD"); code != 0 && code != 2 {
		add(false, login, "git cannot read origin: %s", msg)
		return out // push and GitHub would fail the same way
	}
	// A dry run asks origin to accept the push without sending it.
	if code, msg := remote("push", "--dry-run", "--no-verify", "--quiet", "origin", "HEAD:refs/heads/pitwall-check-"+strings.ToLower(rand.Text()[:8])); code != 0 {
		add(false, login+", with write access", "git cannot push to origin: %s", msg)
	}
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	if gh, err := forge.PushAccess(ctx, repo); err != nil {
		add(false, "gh auth login there, with an account that can push to "+gh, "%v", err)
	}
	return out
}

// firstLine is the first line of a command's stderr, where git puts what
// ssh or the server said, else err.
func firstLine(stderr string, err error) string {
	if line, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n"); line != "" {
		return line
	}
	return err.Error()
}
