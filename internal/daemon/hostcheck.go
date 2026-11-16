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
	"github.com/quanticstudios/pitwall/internal/childproc"
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
	return checkStart(ctx, m, true)
}

// diagnostics is false for JSON: transport stderr can contain arbitrary
// credential values or argument lists that pattern redaction cannot identify.
func checkStart(ctx context.Context, m proto.HostCheck, diagnostics bool) []proto.HostProblem {
	var out []proto.HostProblem
	add := func(warn bool, fix, format string, args ...any) {
		out = append(out, proto.HostProblem{What: fmt.Sprintf(format, args...), Fix: fix, Warn: warn})
	}
	home, _ := os.UserHomeDir()
	repo := m.Repo
	if rest, ok := strings.CutPrefix(repo, "~/"); ok {
		repo = filepath.Join(home, rest) // as newWorkspace expands NewWorkspace.Root
	}
	cwd := cmp.Or(m.Cwd, repo, home)
	if rest, ok := strings.CutPrefix(cwd, "~/"); ok || cwd == "~" {
		cwd = filepath.Join(home, rest)
	}
	get := envGet(m.Env)
	_, cwdErr := os.Stat(cwd)
	pending := repo != "" && errors.Is(cwdErr, os.ErrNotExist)
	if len(m.Cmd) > 0 {
		provider := agent.Identify("", m.Cmd[0])
		key, dir := agentDir(provider, get, cwd)
		// why: panes start with exec.Command, which looks cmd[0] up on the daemon's own PATH.
		program := m.Cmd[0]
		relativeProgram := !filepath.IsAbs(program) && strings.ContainsAny(program, `/\`)
		if relativeProgram {
			program = filepath.Join(cwd, program)
		}
		_, pathErr := exec.LookPath(program)
		if pathErr != nil && !(pending && relativeProgram) {
			add(false, "install it, or add its folder to the PATH pitwall's daemon starts with (docs/remote.md)", "%s is not on the daemon's PATH", m.Cmd[0])
		} else if rel, err := filepath.Rel(cwd, dir); pending && (relativeProgram || err == nil && filepath.IsLocal(rel)) {
			// Checkout and worktree links can supply these files. startVT
			// still checks the final hooks before any pane can run.
			add(true, "ensure the command and hooks exist in the prepared worktree; the start-time guard still applies", "agent preflight deferred until the worktree exists")
		} else {
			if err := guardGateContext(ctx, m.Cmd, m.Env, cwd); err != nil {
				add(false, "", "%v: %v", errUnguarded, err)
			} else if err := agent.GuardHookedContext(ctx, provider, dir); err != nil && key != "" {
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
	env := append(os.Environ(), m.Env...)
	check := gitstat.CommandEnv(ctx, repo, env, "rev-parse", "--is-inside-work-tree")
	childproc.Bound(check)
	if result, err := check.Output(); err != nil || strings.TrimSpace(string(result)) != "true" {
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
		cmd := gitstat.CommandEnv(ctx, repo, env, args...)
		childproc.Bound(cmd)
		// What a worker's git push uses, failing instead of asking for a password.
		ssh := get("GIT_SSH_COMMAND")
		if ssh == "" {
			// The person's own transport. The guard replaces a value from
			// .git/config, but one written after its read keeps local scope.
			config := gitstat.CommandEnv(ctx, repo, env, "config", "--show-scope", "--get", "core.sshCommand")
			childproc.Bound(config)
			out, _ := config.Output()
			if scope, value, _ := strings.Cut(strings.TrimSpace(string(out)), "\t"); scope == "system" || scope == "global" || scope == "command" {
				ssh = value
			}
		}
		if ssh == "" && get("GIT_SSH") != "" {
			ssh = "'" + strings.ReplaceAll(get("GIT_SSH"), "'", `'\''`) + "'"
		}
		ssh = cmp.Or(ssh, "ssh") + " -o BatchMode=yes"
		cmd.Env = append(cmd.Env, "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND="+ssh)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		if errors.Is(err, exec.ErrWaitDelay) {
			err = nil // git succeeded; a helper it started kept stderr open
		}
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
		what := "git cannot read origin"
		if diagnostics {
			what += ": " + msg
		}
		add(false, login, "%s", what)
		return out // push and GitHub would fail the same way
	}
	// A dry run asks origin to accept the push without sending it.
	if code, msg := remote("push", "--dry-run", "--no-verify", "--quiet", "origin", "HEAD:refs/heads/pitwall-check-"+strings.ToLower(rand.Text()[:8])); code != 0 {
		what := "git cannot push to origin"
		if diagnostics {
			what += ": " + msg
		}
		add(false, login+", with write access", "%s", what)
	} else {
		// A server can accept receive-pack but reject the actual update.
		// Without an actual write, a successful dry run does not prove permission.
		add(true, "confirm this worker's git credentials can push to origin", "push permission not checked: git's dry run succeeded, but origin may still refuse a write")
	}
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	if gh, err := forge.PushAccessEnv(ctx, repo, env); err != nil {
		what := "gh cannot verify push access to " + gh
		if diagnostics {
			what = err.Error()
		}
		add(false, "gh auth login there, with an account that can push to "+gh, "%s", what)
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
