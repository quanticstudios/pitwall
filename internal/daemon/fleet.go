package daemon

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// Fleet mode ([fleet]): a worker started on a lane gets one nudge past its
// lane's timebox with no pull request open, and its parent tab one line
// past twice the timebox. A worker whose checks fail again after
// auto_send_failures sent them twice is escalated as [fleet] escalate says.

// fleetOn is config.toml when fleet mode is on.
func (d *Daemon) fleetOn() (config.Settings, bool) {
	if d.o.Fleet == nil {
		return config.Settings{}, false
	}
	s := d.o.Fleet()
	return s, s.Fleet.On
}

// note is a message to type at a pane's prompt once d.mu is free.
type note struct{ pane, text string }

// agentPane is the pane of w that runs its agent: the first live agent
// pane, else its first live pane; "" for none. Callers hold d.mu.
func (d *Daemon) agentPane(w model.Workspace) string {
	for _, ap := range d.st.AgentPanes(w) {
		if !ap.Pane.Exited {
			return ap.Pane.ID
		}
	}
	for _, t := range w.Tabs {
		for _, id := range layout.Panes(t.Layout) {
			if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id }); i >= 0 && !d.st.Panes[i].Exited {
				return id
			}
		}
	}
	return ""
}

// workerName is how a message to a parent names worker w: its address
// and name, as "#4.1 fix-login". Callers hold d.mu.
func (d *Daemon) workerName(w model.Workspace, pane string) string {
	name := d.tabName(w.ID, w.ID)
	if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == pane }); i >= 0 {
		if a := d.paneAddr(d.st.Panes[i]); a != "" {
			return a + " " + name
		}
	}
	return name
}

// checkFleet nudges each lane worker past its lane's timebox with no pull
// request open or merged, once, and past twice the timebox tells its
// parent tab, once.
func (d *Daemon) checkFleet(now time.Time) {
	s, on := d.fleetOn()
	if !on {
		return
	}
	var notes []note
	d.mu.Lock()
	for i := range d.st.Workspaces {
		w := &d.st.Workspaces[i]
		tb := s.Fleet.Timebox(w.Lane.Name)
		if tb <= 0 || w.Lane.Start.IsZero() || w.Lane.Nudged && w.Lane.Told {
			continue
		}
		if pr, ok := d.st.PRs[w.ID]; ok && pr.State != model.PRClosed {
			continue
		}
		age := now.Sub(w.Lane.Start)
		worker := d.agentPane(*w)
		if age >= tb && !w.Lane.Nudged {
			w.Lane.Nudged = true
			d.changed()
			if worker != "" {
				notes = append(notes, note{worker, s.Fleet.Nudge})
			}
		}
		if age >= 2*tb && !w.Lane.Told {
			w.Lane.Told = true
			d.changed()
			if p := d.workspace(w.Parent); p != nil {
				if to := d.agentPane(*p); to != "" {
					notes = append(notes, note{to, fmt.Sprintf("pitwall: worker %s on the %s lane is past twice its %s timebox with no pull request open.",
						d.workerName(*w, worker), w.Lane.Name, fmt.Sprintf("%gm", tb.Minutes()))})
				}
			}
		}
	}
	d.mu.Unlock()
	d.sendNotes(notes)
}

func (d *Daemon) sendNotes(notes []note) {
	for _, n := range notes {
		if err := d.send(proto.Send{Pane: n.pane, Text: n.text}); err != nil {
			log.Printf("fleet: pane %s: %q", n.pane, err)
		}
	}
}

// escalation is a lane worker whose checks failed again after two sends.
type escalation struct {
	ws, key, dir, pane string
	pr                 model.PR
}

// escalations lists, for [fleet] escalate, the lane workers whose open
// pull request has a failed check on a head auto_send_failures has not
// sent, after it sent autoSendMax, and marks each escalated. Callers hold
// d.mu.
func (d *Daemon) escalations(answered map[string]bool, keys map[string]string, mode string) []escalation {
	if mode == "off" {
		return nil
	}
	var out []escalation
	for i := range d.st.Workspaces {
		w := &d.st.Workspaces[i]
		k := keys[w.ID]
		pr, ok := d.st.PRs[w.ID]
		sent := d.prs.sent[k]
		if w.Lane.Name == "" || w.Lane.Escalated || !answered[k] || !ok || pr.State != model.PROpen || pr.CI() != model.CheckFail ||
			pr.Head == "" || len(sent) < autoSendMax || slices.Contains(sent, pr.Head) {
			continue
		}
		pane := d.agentPane(*w)
		if pane == "" {
			continue
		}
		w.Lane.Escalated = true
		d.changed()
		out = append(out, escalation{w.ID, k, d.st.LivePath(*w), pane, pr})
	}
	return out
}

// escalate tells e's parent tab, or with [fleet] escalate = "respawn" and a
// big lane, closes the worker's agent and starts the big lane in its place.
func (d *Daemon) escalate(ctx context.Context, s config.Settings, e escalation) {
	big, ok := s.Fleet.Lane("big")
	preset, found := s.FindPreset(big.Preset)
	if s.Fleet.Escalate == "respawn" && ok && found {
		err := d.respawn(ctx, s, e, preset)
		if err == nil {
			return
		}
		log.Printf("fleet: tab %s: respawn on the big lane: %q; telling its parent", e.ws, err)
	}
	d.mu.Lock()
	var notes []note
	if w := d.workspace(e.ws); w != nil {
		if p := d.workspace(w.Parent); p != nil {
			if to := d.agentPane(*p); to != "" {
				notes = append(notes, note{to, fmt.Sprintf("pitwall: worker %s: CI failed again on pull request #%d after pitwall sent it the failures twice (%s).",
					d.workerName(*w, e.pane), e.pr.Number, strings.Join(e.pr.Failed(), ", "))})
			}
		}
	}
	d.mu.Unlock()
	d.sendNotes(notes)
}

// respawn closes e's agent pane and starts preset, the big lane's, in its
// place in the same worktree. Its brief is the worker contract, the
// branch's diff stat, the failed checks and the old agent's last screen.
func (d *Daemon) respawn(ctx context.Context, s config.Settings, e escalation, preset config.AgentPreset) error {
	d.mu.Lock()
	old := d.panes[e.pane]
	d.mu.Unlock()
	if old == nil {
		return fmt.Errorf("no pane %s", e.pane)
	}
	screen := screenText(old.Snapshot())
	var stat, failures string
	if d.o.DiffStat != nil {
		stat, _ = d.o.DiffStat(ctx, e.dir)
	}
	if d.o.Failures != nil {
		cctx, cancel := context.WithTimeout(ctx, autoSendTimeout)
		if checks, err := d.o.Failures(cctx, e.dir); err == nil && len(checks) > 0 {
			failures = forge.FailuresPrompt(e.pr.Number, checks)
		}
		cancel()
	}
	brief, err := s.Fleet.Brief(respawnBrief(e.pr, stat, failures, screen))
	if err != nil {
		return err
	}
	cmd := append(append([]string{preset.Agent}, preset.Args...), brief)
	env := slices.Clone(preset.Env)
	if preset.Guard || s.GuardWorkers {
		env = append(env, guard.Env+"=1")
	}

	d.mu.Lock()
	w := d.workspace(e.ws)
	if w == nil || d.panes[e.pane] != old {
		d.mu.Unlock()
		return fmt.Errorf("the worker's tab or pane closed")
	}
	ti := paneTab(w, e.pane)
	if ti < 0 {
		d.mu.Unlock()
		return fmt.Errorf("pane %s is in no tab", e.pane)
	}
	id := newID()
	if err := d.start(id, cmd, w.Path, w.Ports, env); err != nil {
		d.mu.Unlock()
		return err
	}
	d.st.Panes = append(d.st.Panes, model.Pane{ID: id, WorkspaceID: w.ID, Cmd: cmd, Cwd: w.Path, Env: env})
	w.Tabs[ti].Layout = d.o.Split(w.Tabs[ti].Layout, e.pane, id, layout.Horizontal)
	closing := d.removePane(e.pane)
	if w = d.workspace(e.ws); w != nil {
		w.Lane = model.Lane{Name: "big", Start: time.Now(), Escalated: true}
	}
	delete(d.prs.sent, e.key) // the new agent gets its own sends
	d.changed()
	d.mu.Unlock()
	closeAll(closing)
	log.Printf("fleet: tab %s: CI failed again; started the big lane in place of pane %s", e.ws, e.pane)
	return nil
}

// respawnBrief is the brief of the agent that takes over a branch whose
// checks kept failing.
func respawnBrief(pr model.PR, stat, failures, screen string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You take over this branch from an agent that could not get its checks to pass: pull request #%d failed again after that agent got its failures twice. Fix the branch and push.\n", pr.Number)
	if stat != "" {
		b.WriteString("\nThe branch's changes against the default branch:\n" + stat + "\n")
	}
	if failures != "" {
		b.WriteString("\n" + failures + "\n")
	} else if f := pr.Failed(); len(f) > 0 {
		b.WriteString("\nFailed checks: " + strings.Join(f, ", ") + "\n")
	}
	if screen != "" {
		b.WriteString("\nThe previous agent's last screen:\n" + screen + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// diffStat is the work tree at dir's changes against the default branch,
// a line per file as "path +3 -1".
func diffStat(ctx context.Context, dir string) (string, error) {
	_, files, err := gitstat.Files(ctx, dir)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, f := range files {
		lines = append(lines, fmt.Sprintf("%s +%d -%d", f.Path, f.Add, f.Del))
	}
	return strings.Join(lines, "\n"), nil
}
