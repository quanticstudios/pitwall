package daemon

import (
	"context"
	"log"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/nowindow"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// autoSendMax is how many times [git] auto_send_failures sends one
// branch's failed checks to its agent.
const autoSendMax = 2

// autoSendTimeout bounds one fetch of failed checks and their logs.
const autoSendTimeout = 3 * time.Minute

// autoSend is a branch whose failed checks may go to its tab's agent.
type autoSend struct {
	key, dir, pane string
	pr             model.PR
}

// autoSends lists, for [git] auto_send_failures, the branches gh just
// answered for whose open pull request has a failed check on a head not
// sent yet, fewer than autoSendMax sent, and an agent with no message
// waiting. Callers hold d.mu.
func (d *Daemon) autoSends(answered map[string]bool, keys map[string]string) []autoSend {
	var out []autoSend
	for _, w := range d.st.Workspaces {
		k := keys[w.ID]
		pr, ok := d.st.PRs[w.ID]
		sent := d.prs.sent[k]
		if !answered[k] || !ok || pr.State != model.PROpen || pr.CI() != model.CheckFail || pr.Head == "" ||
			slices.Contains(sent, pr.Head) || len(sent) >= autoSendMax || slices.ContainsFunc(out, func(a autoSend) bool { return a.key == k }) {
			continue
		}
		for _, ap := range d.st.AgentPanes(w) {
			if !ap.Pane.Exited && len(d.mail.queue[ap.Pane.ID]) == 0 {
				out = append(out, autoSend{k, d.st.LivePath(w), ap.Pane.ID, pr})
				break
			}
		}
	}
	return out
}

// sendFailures queues the failed checks of a's pull request for its agent,
// as Send failures to agent words them, and counts its head as sent. It
// skips an agent that is busy or cannot take a prompt, to try again at the
// next poll, and a branch whose local head is not the one GitHub reports:
// its checks are of an older commit, or of one an agent has not pushed.
func (d *Daemon) sendFailures(ctx context.Context, a autoSend) {
	if _, _, err := d.ready(a.pane); err != nil {
		return
	}
	if head, err := d.o.Head(ctx, a.dir); err != nil || head != a.pr.Head {
		return
	}
	d.mu.Lock()
	if d.prs.seen[a.key] == nil || slices.Contains(d.prs.sent[a.key], a.pr.Head) { // its tabs went, or a tab's twin sent it
		d.mu.Unlock()
		return
	}
	d.prs.sent[a.key] = append(d.prs.sent[a.key], a.pr.Head)
	d.mu.Unlock()
	// ponytail: fetched in the poll loop, which waits for it; the cap keeps
	// that rare.
	cctx, cancel := context.WithTimeout(ctx, autoSendTimeout)
	checks, err := d.o.Failures(cctx, a.dir)
	cancel()
	if err != nil || len(checks) == 0 {
		log.Printf("auto_send_failures: pane %s: no failed checks to send: %v", a.pane, err)
		return
	}
	if err := d.send(proto.Send{Pane: a.pane, Text: forge.FailuresPrompt(a.pr.Number, checks)}); err != nil {
		log.Printf("auto_send_failures: pane %s: %q", a.pane, err)
	}
}

// gitHead is the commit checked out at dir.
func gitHead(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	nowindow.Set(cmd)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
