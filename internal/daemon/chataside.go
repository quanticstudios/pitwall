package daemon

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agentenv"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// asideWait is how long a side question may take before it stops.
const asideWait = 3 * time.Minute

// asidePrompt frames a side question for the one-off agent.
const asidePrompt = "The user asks a side question about this conversation while it goes on elsewhere. " +
	"Answer it from the conversation so far, briefly and directly. Change nothing and run nothing."

// asideRun is where a side question runs: the session it forks, its
// folder, its model and the environment it adds.
type asideRun struct {
	session, dir, model string
	env                 []string
}

// chatAside answers m with a one-off claude -p that resumes a fork of
// the chat's session that is never saved, with no tools: the chat's own
// agent neither stops nor sees the question.
func (d *Daemon) chatAside(ctx context.Context, m proto.ChatAside) proto.ChatAsideResult {
	out := proto.ChatAsideResult{Chat: m.Chat, ID: m.ID}
	run, err := d.asideOf(m.Chat)
	if err == nil && strings.TrimSpace(m.Question) == "" {
		err = errors.New("the question is empty")
	}
	if err != nil {
		out.Err = err.Error()
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, asideWait)
	defer cancel()
	c := exec.CommandContext(ctx, cmp.Or(os.Getenv(claude.CommandEnv), "claude"), asideArgs(run)...)
	c.Dir = run.dir
	c.Env = append(agentenv.Clean(os.Environ()), run.env...)
	c.Stdin = strings.NewReader(m.Question)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	c.WaitDelay = time.Second
	switch err := c.Run(); {
	case ctx.Err() == context.DeadlineExceeded:
		out.Err = "Claude took longer than " + asideWait.String() + " to answer, so pitwall stopped it"
	case err != nil:
		out.Err = cmp.Or(lastLine(stderr.String()), err.Error())
	default:
		out.Answer = strings.TrimSpace(stdout.String())
	}
	return out
}

// asideArgs is claude's command line for run, the question on stdin.
func asideArgs(run asideRun) []string {
	a := []string{"-p", "--resume", run.session, "--fork-session", "--no-session-persistence",
		"--tools", "", "--output-format", "text", "--append-system-prompt", asidePrompt}
	if run.model != "" {
		a = append(a, "--model", run.model)
	}
	return a
}

// asideOf is where a side question about chat id runs: a chat tab's
// session, or the session a pane chat's transcript is of.
func (d *Daemon) asideOf(id string) (asideRun, error) {
	d.mu.Lock()
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	var p model.Pane
	if i >= 0 {
		p = d.st.Panes[i]
	}
	d.mu.Unlock()
	if i >= 0 && model.PaneChat(p) {
		sid := strings.TrimSuffix(filepath.Base(p.Transcript), ".jsonl")
		if p.Transcript == "" || sid == "" {
			return asideRun{}, errors.New("Claude has not written this conversation down yet; ask after its first reply")
		}
		return asideRun{session: sid, dir: p.Cwd, env: p.Env}, nil
	}
	c, err := d.chat(id)
	if err != nil {
		return asideRun{}, err
	}
	var run asideRun
	if !c.do(func() {
		switch {
		case c.cfg.Provider != model.ProviderClaude:
			err = chatError{"unsupported", "side questions need a Claude chat"}
		case c.cfg.Resume == "":
			err = errors.New("the chat has no conversation yet; ask after its first reply")
		}
		run = asideRun{session: c.cfg.Resume, dir: c.cfg.Cwd, model: c.cfg.Model, env: slices.Clone(c.cfg.Env)}
	}) {
		err = chatError{"not-found", "chat " + id + " is closed"}
	}
	return run, err
}

// lastLine is s's last line that is not blank.
func lastLine(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(ls[len(ls)-1])
}
