package daemon

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// One agent talks to another through pitwall prompt and pitwall read: the
// daemon types a prompt into an agent waiting at its prompt, and keeps
// each pane's last reply for read.

// maxReply is how much of a turn's last message the daemon keeps, as much
// as pi's extension sends.
const maxReply = 16000

// keepReply keeps msg, the full last message of a turn pane's agent
// finished, for Read: redacted, then cut to maxReply runes. Callers hold
// d.mu.
func (d *Daemon) keepReply(pane, msg string) {
	msg = decide.Redact(msg, d.dec.cur.Secrets...)
	if r := []rune(msg); len(r) > maxReply {
		msg = string(r[:maxReply])
	}
	if d.replies == nil {
		d.replies = map[string]string{}
	}
	d.replies[pane] = msg
}

// prompt types m.Text into an agent at its prompt and presses Enter. It
// refuses a pane whose agent cannot tell pitwall when the turn ends, one
// running a turn or waiting on the user, and one whose screen shows a
// form, which the Enter would answer.
func (d *Daemon) prompt(m proto.Prompt) error {
	d.mu.Lock()
	p := d.panes[m.Pane]
	i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == m.Pane })
	if p == nil || i < 0 || d.inputs[m.Pane] == nil {
		d.mu.Unlock()
		return fmt.Errorf("no running pane %s", m.Pane)
	}
	sp := d.st.Panes[i]
	// why: Codex sends its first hook with its first prompt, so an agent
	// pitwall has hooks for, seen running and not missing them, counts.
	hooked := !d.live.hookAt[m.Pane].IsZero() || agent.HooksFor(sp.Provider) && !sp.HooksMissing && d.dec.screens[m.Pane] == nil
	var a model.Activity
	if ai := d.activityIndex(m.Pane); ai >= 0 {
		a = d.st.Activities[ai]
	}
	d.mu.Unlock()
	switch {
	case sp.Provider == "" || sp.Provider == model.ProviderTerminal:
		return errors.New("the pane runs no agent pitwall knows")
	case !hooked:
		return fmt.Errorf("%s reports nothing through hooks, so pitwall cannot tell when its turn ends (pitwall hooks install)", sp.Provider)
	case a.State == model.StatePendingApproval || a.State == model.StateAwaitingInput || a.State == model.StatePlanReady:
		return fmt.Errorf("%s waits on the user: %s", sp.Provider, a.Detail)
	case a.State != "" && a.State != model.StateCompleted && a.State != model.StateError:
		return fmt.Errorf("%s is working on a turn", sp.Provider)
	case agent.ReadScreen(p.Snapshot()) == agent.ScreenForm:
		return fmt.Errorf("%s shows a prompt to answer", sp.Provider)
	}
	// why: the reply kept is the last turn's, and this turn's may come
	// before typeLine returns.
	d.mu.Lock()
	delete(d.replies, m.Pane)
	d.mu.Unlock()
	return d.typeLine(m.Pane, p, m.Text, "the prompt is empty")
}

// read answers m with the reply kept for its pane, or its screen's text.
func (d *Daemon) read(m proto.Read) proto.ReadResult {
	r := proto.ReadResult{Pane: m.Pane}
	d.mu.Lock()
	p := d.panes[m.Pane]
	reply, ok := d.replies[m.Pane]
	d.mu.Unlock()
	switch {
	case p == nil:
		r.Err = fmt.Sprintf("no pane %s", m.Pane)
	case m.Screen:
		r.Text = screenText(p.Snapshot())
	case !ok:
		r.Err = "no reply kept: the agent has not finished a turn since the daemon started or since its last prompt"
	default:
		r.Text = reply
	}
	return r
}

// screenText is g's rows as lines, trailing spaces and empty rows at the
// bottom trimmed.
func screenText(g vt.Grid) string {
	var b strings.Builder
	for y := range g.Rows {
		var line strings.Builder
		for x := range g.Cols {
			switch c := g.At(x, y); {
			case c.Width == 0: // the right half of a wide character
			case c.Content == "":
				line.WriteByte(' ')
			default:
				line.WriteString(c.Content)
			}
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
