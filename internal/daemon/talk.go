package daemon

import (
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// One agent talks to another through pitwall prompt, send and read: the
// daemon types a prompt into an agent waiting at its prompt, queues a
// message until the agent waits there, and keeps each pane's last reply
// for read.

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
	p, _, err := d.ready(m.Pane)
	if err != nil {
		return err
	}
	// why: the reply kept is the last turn's, and this turn's may come
	// before typeLine returns.
	d.mu.Lock()
	delete(d.replies, m.Pane)
	d.mail.typed[m.Pane] = time.Now() // a queued message waits for this turn
	d.mu.Unlock()
	return d.typeLine(m.Pane, p, m.Text, "the prompt is empty")
}

// ready returns pane's handle when its agent sits at its prompt, waiting
// for a new one. Otherwise err says why, and busy reports a reason that
// the end of a turn or the user clears: a turn running, a question
// waiting, a form on screen.
func (d *Daemon) ready(pane string) (p Pane, busy bool, err error) {
	d.mu.Lock()
	p = d.panes[pane]
	i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == pane })
	if p == nil || i < 0 || d.inputs[pane] == nil {
		d.mu.Unlock()
		return nil, false, fmt.Errorf("no running pane %s", pane)
	}
	sp := d.st.Panes[i]
	// why: Codex sends its first hook with its first prompt, so an agent
	// pitwall has hooks for, seen running and not missing them, counts.
	hooked := !d.live.hookAt[pane].IsZero() || agent.HooksFor(sp.Provider) && !sp.HooksMissing && d.dec.screens[pane] == nil
	var a model.Activity
	if ai := d.activityIndex(pane); ai >= 0 {
		a = d.st.Activities[ai]
	}
	d.mu.Unlock()
	switch {
	case sp.Provider == "" || sp.Provider == model.ProviderTerminal:
		return nil, false, errors.New("the pane runs no agent pitwall knows")
	case !hooked:
		return nil, false, fmt.Errorf("%s reports nothing through hooks, so pitwall cannot tell when its turn ends (pitwall hooks install)", sp.Provider)
	case a.State == model.StatePendingApproval || a.State == model.StateAwaitingInput || a.State == model.StatePlanReady:
		return nil, true, fmt.Errorf("%s waits on the user: %s", sp.Provider, a.Detail)
	case a.State != "" && a.State != model.StateCompleted && a.State != model.StateError:
		return nil, true, fmt.Errorf("%s is working on a turn", sp.Provider)
	case agent.ReadScreen(p.Snapshot()) == agent.ScreenForm:
		return nil, true, fmt.Errorf("%s shows a prompt to answer", sp.Provider)
	}
	return p, false, nil
}

// Message timings and the loop guard; tests shorten them.
var (
	// sendSettle is how long after a hook a message waits, so that after a
	// Stop the agent's TUI has redrawn its input box.
	sendSettle = 500 * time.Millisecond
	// sendQuiet is how long a key of the user's holds messages to its pane.
	sendQuiet = 3 * time.Second
	// sendStart is how long the next message waits for the turn the last
	// one started to report; past it, the agent dropped that one.
	sendStart = 30 * time.Second
	// sendLimit is how many messages agents may send one pane between two
	// keys of the user's in it.
	// why: two agents answering each other would talk forever on the user's quota.
	sendLimit = 20
)

// mail is what pitwall send keeps, in memory only: closing a pane drops
// its messages, and a daemon restart loses them all. Guarded by d.mu.
type mail struct {
	queue map[string][]letter    // pane: messages to type, first first
	typed map[string]time.Time   // pane: when a message or prompt was last typed
	keyAt map[string]time.Time   // pane: the user's last key
	sent  map[string]int         // pane: agents' messages since the user's last key
	wake  map[string]*time.Timer // pane: the next deliver
}

func newMail() mail {
	return mail{queue: map[string][]letter{}, typed: map[string]time.Time{}, keyAt: map[string]time.Time{}, sent: map[string]int{}, wake: map[string]*time.Timer{}}
}

// letter is a queued message; from is the sending pane, "" for a script.
type letter struct{ from, text string }

// send queues m for its pane's agent, which gets it when it next sits at
// its prompt. It refuses a pane whose agent cannot take a prompt even
// after its turn, and agents past sendLimit.
func (d *Daemon) send(m proto.Send) error {
	switch {
	case m.From == m.Pane:
		return errors.New("that is the sending pane: send to another agent's pane")
	case remote.Clean(m.Text) == "":
		return errors.New("the message is empty")
	}
	if _, busy, err := d.ready(m.Pane); err != nil && !busy {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if m.From != "" {
		if d.mail.sent[m.Pane] >= sendLimit {
			return fmt.Errorf("agents sent that pane %d messages since the user last typed in it: stop, and ask the user before sending more", sendLimit)
		}
		d.mail.sent[m.Pane]++
	}
	d.mail.queue[m.Pane] = append(d.mail.queue[m.Pane], letter{m.From, m.Text})
	d.changed()
	d.wake(m.Pane, 0)
	return nil
}

// wake runs deliver for pane after wait, in place of an earlier wake:
// deliver judges every hold anew. Callers hold d.mu.
func (d *Daemon) wake(pane string, wait time.Duration) {
	if t := d.mail.wake[pane]; t != nil {
		t.Stop()
	}
	d.mail.wake[pane] = time.AfterFunc(wait, func() { d.deliver(pane) })
}

// deliver types pane's next message, under a header, once its agent sits
// at its prompt: sendSettle after its last hook, sendQuiet after the
// user's last key, and once the turn the last message started has
// reported. A hold that ends at a known time wakes deliver then; for a
// turn running or a question, the next hook or user key does.
func (d *Daemon) deliver(pane string) {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	d.mu.Lock()
	if len(d.mail.queue[pane]) == 0 {
		d.mu.Unlock()
		return
	}
	now, hook := time.Now(), d.live.hookAt[pane]
	wait := max(hook.Add(sendSettle).Sub(now), d.mail.keyAt[pane].Add(sendQuiet).Sub(now))
	if t := d.mail.typed[pane]; !hook.After(t) {
		wait = max(wait, t.Add(sendStart).Sub(now))
	}
	if wait > 0 {
		d.wake(pane, wait)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	p, _, err := d.ready(pane)
	if err != nil {
		return
	}
	d.mu.Lock()
	q := d.mail.queue[pane]
	if len(q) == 0 { // the pane closed
		d.mu.Unlock()
		return
	}
	if len(q) == 1 {
		delete(d.mail.queue, pane)
	} else {
		d.mail.queue[pane] = q[1:]
		d.wake(pane, 0) // why: it waits for this turn, or sendStart if the turn never reports
	}
	d.mail.typed[pane] = time.Now()
	delete(d.replies, pane)
	text := d.header(q[0].from) + "\n" + q[0].text
	d.changed()
	d.mu.Unlock()
	if err := d.typeLine(pane, p, text, "the message is empty"); err != nil {
		log.Printf("pane %s: a message was lost: %q", pane, err)
	}
}

// noteUserKey records the user's input into pane: it holds messages for
// sendQuiet and resets the loop guard. Focus and mouse reports are no
// keys.
func (d *Daemon) noteUserKey(pane string, b []byte) {
	if s := string(b); s == "\x1b[I" || s == "\x1b[O" || strings.HasPrefix(s, "\x1b[<") || strings.HasPrefix(s, "\x1b[M") {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mail.keyAt[pane] = time.Now()
	delete(d.mail.sent, pane)
	if len(d.mail.queue[pane]) > 0 {
		d.wake(pane, 0)
	}
}

// header is the line typed above a message from pane from: who sent it
// and the address to answer, which stays valid while tabs move. Callers
// hold d.mu.
func (d *Daemon) header(from string) string {
	if from == "" {
		return "[pitwall: message from a script]"
	}
	who := "a pane"
	if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == from }); i >= 0 {
		if pr := d.st.Panes[i].Provider; pr != "" && pr != model.ProviderTerminal {
			who = string(pr)
		}
		if a := d.paneAddr(d.st.Panes[i]); a != "" {
			who += " in " + a
		}
	}
	return fmt.Sprintf(`[pitwall: message from %s; reply with: pitwall send @%s "..."]`, who, from)
}

// paneAddr is p's address as pitwall ls numbers it, as #3.2, or "".
// Callers hold d.mu.
func (d *Daemon) paneAddr(p model.Pane) string {
	for i, w := range d.st.Numbered(d.st.SessionOf(p.WorkspaceID)) {
		if w.ID != p.WorkspaceID {
			continue
		}
		n := 0
		for _, t := range w.Tabs {
			for _, id := range layout.Panes(t.Layout) {
				if slices.ContainsFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == id }) {
					n++
				}
				if id == p.ID {
					return fmt.Sprintf("#%d.%d", i+1, n)
				}
			}
		}
	}
	return ""
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
