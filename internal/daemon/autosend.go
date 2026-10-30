package daemon

import (
	"context"
	"fmt"
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
// fleet worker's failed checks to its agent before escalation.
const autoSendMax = 2

// autoSendTimeout bounds one fetch of failed checks and their logs.
const autoSendTimeout = 3 * time.Minute

// autoSend is one branch's failed checks or requested changes to send.
type autoSend struct {
	key, dir, pane string
	pr             model.PR
	act            string
	reviews        []string
}

// autoSends chooses at most one send per branch. A pane holds at most one
// message, including while its agent works. Fleet escalation keeps its
// two-send limit; other tabs may receive every new failing head.
// Callers hold d.mu.
func (d *Daemon) autoSends(answered map[string]bool, keys map[string]string, failures, reviews, fleetCap bool) []autoSend {
	var out []autoSend
	for _, w := range d.st.Workspaces {
		k := keys[w.ID]
		pr, ok := d.st.PRs[w.ID]
		s := d.prs.seen[k]
		if !answered[k] || s == nil || s.err != nil || !ok || pr.State != model.PROpen || d.prs.busy[k] ||
			slices.ContainsFunc(out, func(a autoSend) bool { return a.key == k }) {
			continue
		}
		a := autoSend{key: k, dir: d.st.LivePath(w), pr: pr}
		sent := d.prs.sent[k]
		if failures && pr.CI() == model.CheckFail && pr.Head != "" && !slices.Contains(sent, pr.Head) &&
			!(fleetCap && w.Lane.Name != "" && len(sent) >= autoSendMax) {
			a.act = "send_failures"
		} else if reviews && pr.Review == model.ReviewChanges {
			for _, id := range pr.ChangeRequests {
				id = fmt.Sprintf("%d:%s", pr.Number, id)
				if !slices.Contains(d.prs.reviews[k], id) {
					a.reviews = append(a.reviews, id)
				}
			}
			if len(a.reviews) > 0 {
				a.act = "send_review"
			}
		}
		if a.act == "" {
			continue
		}
		for _, ap := range d.st.AgentPanes(w) {
			if ap.Pane.Exited {
				continue
			}
			if len(d.mail.queue[ap.Pane.ID]) == 0 {
				a.pane = ap.Pane.ID
				out = append(out, a)
			}
			break // one target per tab, even while its first agent is busy
		}
	}
	return out
}

// sendAgent uses the same fetch/prompt path as the window's startSend.
// Fetches reserve a branch until they finish. Only a successfully queued
// prompt counts as sent; failed or empty fetches can retry on the next poll.
func (d *Daemon) sendAgent(ctx context.Context, a autoSend) {
	if _, busy, err := d.ready(a.pane); err != nil && !busy {
		return
	}
	if a.act == "send_failures" {
		if head, err := d.o.Head(ctx, a.dir); err != nil || head != a.pr.Head {
			return
		}
	}
	d.mu.Lock()
	s := d.prs.seen[a.key]
	if a.act == "send_review" {
		a.reviews = slices.DeleteFunc(slices.Clone(a.reviews), func(id string) bool { return slices.Contains(d.prs.reviews[a.key], id) })
	}
	if a.act == "send_review" && len(a.reviews) == 0 || s == nil || d.prs.busy[a.key] || len(d.mail.queue[a.pane]) > 0 ||
		a.act == "send_failures" && slices.Contains(d.prs.sent[a.key], a.pr.Head) {
		d.mu.Unlock()
		return
	}
	if d.prs.busy == nil {
		d.prs.busy = map[string]bool{}
	}
	d.prs.busy[a.key] = true
	d.mu.Unlock()
	defer func() { d.mu.Lock(); delete(d.prs.busy, a.key); d.mu.Unlock() }()
	cctx, cancel := context.WithTimeout(ctx, autoSendTimeout)
	text, err := forge.SendPrompt(cctx, a.dir, a.pr.Number, a.act, d.o.Failures, d.o.Reviews)
	cancel()
	if err != nil || text == "" {
		log.Printf("%s: pane %s: nothing to send: %v", a.act, a.pane, err)
		return
	}
	if a.act == "send_failures" {
		if head, err := d.o.Head(ctx, a.dir); err != nil || head != a.pr.Head {
			return
		}
	}
	d.mu.Lock()
	current := d.prs.seen[a.key]
	valid := current == s && current.pr != nil && current.pr.Number == a.pr.Number && current.pr.State == model.PROpen && current.err == nil
	if valid && a.act == "send_failures" {
		valid = current.pr.Head == a.pr.Head && current.pr.CI() == model.CheckFail
	}
	if valid && a.act == "send_review" {
		valid = current.pr.Review == model.ReviewChanges && slices.Equal(current.pr.ChangeRequests, a.pr.ChangeRequests)
	}
	d.mu.Unlock()
	if !valid {
		return
	}
	what := "CI failures"
	if a.act == "send_review" {
		what = "review comments"
	}
	if err := d.sendMessage(proto.Send{Pane: a.pane, Text: text}, "Sent "+what+" to ", true); err != nil {
		log.Printf("%s: pane %s: %q", a.act, a.pane, err)
		return
	}
	d.mu.Lock()
	if a.act == "send_failures" {
		d.prs.sent[a.key] = append(d.prs.sent[a.key], a.pr.Head)
	} else {
		if d.prs.reviews == nil {
			d.prs.reviews = map[string][]string{}
		}
		d.prs.reviews[a.key] = append(d.prs.reviews[a.key], a.reviews...)
	}
	d.mu.Unlock()
}

// gitHead is the commit checked out at dir.
func gitHead(ctx context.Context, dir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "HEAD")
	nowindow.Set(cmd)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
