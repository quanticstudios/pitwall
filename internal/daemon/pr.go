package daemon

import (
	"context"
	"errors"
	"log"
	"reflect"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// prTick is how often prLoop looks for a branch whose PR is due; tests
// call pollPRs instead.
const prTick = 10 * time.Second

// prTimeout bounds one gh call.
const prTimeout = 30 * time.Second

// prPoll is what the PR poller remembers, guarded by Daemon.mu: gh's last
// answer per branch (prKey), the heads whose failed checks went to its
// agent, and a rate limit pause for all of them.
type prPoll struct {
	seen    map[string]*prSeen
	reviews map[string][]string // branch: review IDs already sent
	busy    map[string]bool     // branch: a send fetch is running
	sent    map[string][]string
	pause   time.Time
	wake    chan struct{} // a poke: run pollPRs now
}

// prSeen is gh's last answer for one branch.
type prSeen struct {
	at    time.Time // zero: due now
	pr    *model.PR // the last PR gh reported, kept through errors
	err   error
	fails int // errors in a row, for forge.Next
}

// prKey names a branch checked out at dir; tabs in one folder share it.
func prKey(dir, branch string) string { return dir + "\x00" + branch }

// prLoop keeps State.PRs current while the daemon runs, when o.PR is set.
func (d *Daemon) prLoop(ctx context.Context) {
	if d.o.PR == nil {
		return
	}
	t := time.NewTicker(prTick)
	defer t.Stop()
	for {
		d.pollPRs(ctx, time.Now())
		d.checkFleet(time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.prs.wake:
		}
	}
}

// pokePR makes the PR of tab id's branch due now, for a gh command that
// ended in one of its panes, such as a merge. Callers hold d.mu.
func (d *Daemon) pokePR(id string) {
	w := d.workspace(id)
	if w == nil || w.Branch == "" || d.o.PR == nil {
		return
	}
	if s := d.prs.seen[prKey(d.st.LivePath(*w), w.Branch)]; s != nil {
		s.at = time.Time{}
	}
	select {
	case d.prs.wake <- struct{}{}:
	default:
	}
}

// hidden reports whether no GUI window has focus. Callers hold d.mu.
func (d *Daemon) hidden() bool {
	for c := range d.clients {
		if c.focus != "" {
			return false
		}
	}
	return true
}

// pollPRs asks gh, one branch at a time, about every branch a tab is on
// whose answer forge.Next says is due at now, then sets State.PRs. A tab
// whose PR it sees go from open to merged is archived, as the delete dialog
// does with its branch box ticked, when [git] archive_on_merge is on and
// the tab is a worktree pitwall made. With [git] auto_send_failures on,
// a branch's failed checks go to its tab's agent; see sendAgent.
// send_review_on_request sends reviews through the same path. In
// fleet mode, a lane worker past those sends is escalated; see escalate.
func (d *Daemon) pollPRs(ctx context.Context, now time.Time) {
	type job struct{ key, dir string }
	var jobs []job
	keys := map[string]string{} // tab: its branch's key
	live := map[string]bool{}   // keys some tab has
	d.mu.Lock()
	hidden := d.hidden()
	for _, w := range d.st.Workspaces {
		if w.Branch == "" {
			continue
		}
		dir := d.st.LivePath(w)
		k := prKey(dir, w.Branch)
		keys[w.ID] = k
		if live[k] {
			continue
		}
		live[k] = true
		s := d.prs.seen[k]
		if s != nil && now.Before(s.at.Add(forge.Next(s.pr, s.err, s.fails, hidden))) || now.Before(d.prs.pause) {
			continue
		}
		jobs = append(jobs, job{k, dir})
		if s == nil {
			d.prs.seen[k] = &prSeen{at: now} // not due again before it answers
		}
	}
	for k := range d.prs.seen {
		if !live[k] {
			delete(d.prs.seen, k) // no tab is on that branch now
			delete(d.prs.sent, k)
			delete(d.prs.reviews, k)
		}
	}
	d.mu.Unlock()

	type got struct {
		pr  *model.PR
		err error
	}
	answers := map[string]got{}
	for _, j := range jobs {
		cctx, cancel := context.WithTimeout(ctx, prTimeout)
		pr, err := d.o.PR(cctx, j.dir)
		cancel()
		if ctx.Err() != nil {
			return
		}
		answers[j.key] = got{pr, err}
		if errors.Is(err, forge.ErrRateLimited) {
			break
		}
	}

	failures := len(answers) > 0 && d.o.AutoSendFailures != nil && d.o.Head != nil && d.o.Failures != nil && d.o.AutoSendFailures()
	var fleet config.Settings
	fleetOn := false
	reviews := len(answers) > 0 && d.o.SendReviewOnRequest != nil && d.o.Reviews != nil && d.o.SendReviewOnRequest()
	if failures {
		fleet, fleetOn = d.fleetOn()
	}
	d.mu.Lock()
	for k, a := range answers {
		s := d.prs.seen[k]
		if s == nil {
			continue // its tabs went while gh ran
		}
		s.at, s.err = now, a.err
		switch {
		case a.err == nil:
			s.pr, s.fails = a.pr, 0
		case errors.Is(a.err, forge.ErrNoGH):
			s.pr, s.fails = nil, 0
		case errors.Is(a.err, forge.ErrRateLimited):
			d.prs.pause = now.Add(forge.RateLimitPause)
			log.Printf("pull requests: %q; pausing", a.err)
		default:
			s.fails++
			log.Printf("pull requests: %q", a.err)
		}
	}
	changed := false
	var archive []string
	for _, w := range d.st.Workspaces {
		old, had := d.st.PRs[w.ID]
		var pr *model.PR
		if s := d.prs.seen[keys[w.ID]]; s != nil {
			pr = s.pr
		}
		switch {
		case pr == nil && had:
			delete(d.st.PRs, w.ID)
			changed = true
		case pr != nil && (!had || !reflect.DeepEqual(old, *pr)):
			d.prMilestones(w, old, had, *pr, now)
			if had && old.Number == pr.Number && old.State == model.PROpen && pr.State == model.PRMerged && w.WorktreeRoot != "" && !w.Pinned {
				archive = append(archive, w.ID)
			}
			d.st.PRs[w.ID] = *pr
			changed = true
		}
	}
	if changed {
		d.changed()
	}
	var sends []autoSend
	var escs []escalation
	if failures || reviews {
		answered := map[string]bool{}
		for k := range answers {
			answered[k] = true
		}
		if fleetOn {
			escs = d.escalations(answered, keys, fleet.Fleet.Escalate)
		}
		sends = d.autoSends(answered, keys, failures, reviews, fleetOn && fleet.Fleet.Escalate != "off")
	}
	d.mu.Unlock()

	for _, a := range sends {
		d.sendAgent(ctx, a)
	}
	for _, e := range escs {
		d.escalate(ctx, fleet, e)
	}

	if len(archive) == 0 || d.o.ArchiveOnMerge == nil || !d.o.ArchiveOnMerge() {
		return
	}
	for _, id := range archive {
		log.Printf("tab %s: pull request merged; archiving", id)
		if err := d.deleteWorkspace(ctx, proto.DeleteWorkspace{WorkspaceID: id, RemoveBranch: true}); err != nil {
			log.Printf("archive tab %s: %q", id, err)
		}
	}
}
