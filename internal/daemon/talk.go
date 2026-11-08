package daemon

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/logs"
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
	msg = logs.Redact(msg)
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
// running a turn or waiting on the user, one whose screen shows a form,
// which the Enter would answer, and one with the user's draft in its box.
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
// waiting, a form on screen, the user's draft in the input box.
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
	hooked := !d.live.hookAt[pane].IsZero() || agent.HooksFor(sp.Provider) && !sp.HooksMissing
	var a model.Activity
	if ai := d.activityIndex(pane); ai >= 0 {
		a = d.st.Activities[ai]
	}
	drafting := d.mail.drafting[pane]
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
	}
	switch g := p.Snapshot(); {
	case agent.ReadScreen(g) == agent.ScreenForm:
		return nil, true, fmt.Errorf("%s shows a prompt to answer", sp.Provider)
	case drafting && agent.Draft(sp.Provider, g):
		return nil, true, errDraft
	}
	return p, false, nil
}

// errDraft is ready's reason while the user's draft sits in the input box.
var errDraft = errors.New("the user has a draft in the agent's input box")

// Message timings and the loop guard; tests shorten them.
var (
	// sendSettle is how long after a hook a message waits, so that after a
	// Stop the agent's TUI has redrawn its input box.
	sendSettle = newTiming(500 * time.Millisecond)
	// sendQuiet is how long a key of the user's holds messages to its pane.
	sendQuiet = newTiming(3 * time.Second)
	// sendLook is how often a draft that holds messages is read again.
	// why: nothing reports a box going empty, and only such a pane is looked at.
	sendLook = newTiming(time.Second)
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
	// drafting marks a pane the user typed in since a prompt of theirs went
	// in, and returnAt when their last key there was Return, which is how a
	// prompt hook is told from another turn's. Box text counts as a draft only
	// while drafting, so a box pitwall misreads never holds messages in a pane
	// nobody typed in.
	// ponytail: in a typed-in pane a misread holds them until the user's next
	// prompt; shipping the box patterns in screen_patterns.json would fix a
	// misread without a release, once old binaries accept the new field.
	drafting map[string]bool
	returnAt map[string]time.Time
	held     map[string]bool // pane: a draft holds its messages
	// delivered is the highest proto.Deliver.Seq typed into a pane.
	delivered map[string]uint64
	// daemonPrompt keeps hooks for daemon-typed text from counting as user submissions.
	daemonPrompt map[string]bool
}

func newMail() mail {
	return mail{queue: map[string][]letter{}, typed: map[string]time.Time{}, keyAt: map[string]time.Time{}, sent: map[string]int{}, wake: map[string]*time.Timer{}, drafting: map[string]bool{}, returnAt: map[string]time.Time{}, held: map[string]bool{}, delivered: map[string]uint64{}, daemonPrompt: map[string]bool{}}
}

// letter is a queued message; from is the sending pane, "" for a script.
// A plain letter is pitwall's own and is typed without a header.
type letter struct {
	from, text string
	plain      bool
	notice     string   // success notice prefix, completed with the agent name
	images     []string // paths typed before the text
	window     bool     // from the window, which judged the agent ready itself
	seq        uint64   // the window's proto.Deliver.Seq
}

// send queues m for its pane's agent, which gets it when it next sits at
// its prompt. It refuses a pane whose agent cannot take a prompt even
// after its turn, and agents past sendLimit.
func (d *Daemon) send(m proto.Send) error {
	return d.sendMessage(m, "", false)
}

// sendMessage optionally limits a pane to one queued message and gives it
// a notice only after delivery.
func (d *Daemon) sendMessage(m proto.Send, notice string, one bool) error {
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
	if one && len(d.mail.queue[m.Pane]) > 0 {
		return errors.New("a message already waits for the agent")
	}
	if m.From != "" {
		if d.mail.sent[m.Pane] >= sendLimit {
			return fmt.Errorf("agents sent that pane %d messages since the user last typed in it: stop, and ask the user before sending more", sendLimit)
		}
		d.mail.sent[m.Pane]++
	}
	d.mail.queue[m.Pane] = append(d.mail.queue[m.Pane], letter{from: m.From, text: m.Text, notice: notice})
	d.changed()
	d.wake(m.Pane, 0)
	return nil
}

// windowDeliver queues what the window sends pane's agent. It counts as the
// user's act for the loop guard and a resume, but as no key: nothing typed in
// the pane holds it back.
func (d *Daemon) windowDeliver(m proto.Deliver) error {
	d.mu.Lock()
	dropped := false
	defer func() {
		d.mu.Unlock()
		if dropped {
			d.save()
		}
	}()
	if d.panes[m.Pane] == nil {
		return fmt.Errorf("no running pane %s", m.Pane)
	}
	if remote.Clean(m.Text) == "" && len(m.Images) == 0 {
		return errors.New("the message is empty")
	}
	d.recordPromptLocked(m.Pane)
	delete(d.mail.sent, m.Pane)
	dropped = d.dropResume(m.Pane)
	d.mail.queue[m.Pane] = append(d.mail.queue[m.Pane], letter{text: m.Text, plain: true, images: m.Images, window: true, seq: m.Seq})
	d.changed()
	d.wake(m.Pane, 0)
	return nil
}

// readyWindow is ready for a letter from the window: an agent the window may
// reply to, question or no hooks included, unless a turn began since, the
// user's draft sits in its box or its screen shows a form Enter would answer.
func (d *Daemon) readyWindow(pane string) (Pane, error) {
	d.mu.Lock()
	p := d.panes[pane]
	var prov model.Provider
	if i := slices.IndexFunc(d.st.Panes, func(sp model.Pane) bool { return sp.ID == pane }); i >= 0 {
		prov = d.st.Panes[i].Provider
	}
	var a model.Activity
	if ai := d.activityIndex(pane); ai >= 0 {
		a = d.st.Activities[ai]
	}
	drafting := d.mail.drafting[pane]
	d.mu.Unlock()
	switch {
	case p == nil:
		return nil, fmt.Errorf("no running pane %s", pane)
	case a.State != "" && !remote.Replies(a.State):
		return nil, fmt.Errorf("%s is busy", prov)
	case drafting && agent.Draft(prov, p.Snapshot()):
		return nil, errDraft
	case formHolds(a, p.Snapshot()):
		return nil, fmt.Errorf("%s shows a form to answer", prov)
	}
	return p, nil
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
// reported, and not into the user's draft. A hold that ends at a known time
// wakes deliver then; for a turn running or a question, the next hook or
// user key does; for a draft or a letter from the window, whose agent may send
// no hooks, sendLook.
func (d *Daemon) deliver(pane string) {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	d.mu.Lock()
	if len(d.mail.queue[pane]) == 0 {
		d.mu.Unlock()
		return
	}
	now, hook := time.Now(), d.live.hookAt[pane]
	wait := max(hook.Add(sendSettle.get()).Sub(now), d.mail.keyAt[pane].Add(sendQuiet.get()).Sub(now))
	// why: Windows' clock can stamp a fast hook with the time of the typing
	// itself; the hook before the typing is at least sendSettle older.
	if t := d.mail.typed[pane]; hook.Before(t) {
		wait = max(wait, t.Add(sendStart).Sub(now))
	}
	if wait > 0 {
		d.wake(pane, wait)
		d.mu.Unlock()
		return
	}
	first := slices.IndexFunc(d.mail.queue[pane], func(l letter) bool { return l.window })
	d.mu.Unlock()
	// i is the letter to type: the first, or a letter from the window, which
	// passes one that cannot go, as that one may wait on the question it answers.
	i, p, err := 0, Pane(nil), error(nil)
	if first == 0 {
		p, err = d.readyWindow(pane)
	} else if p, _, err = d.ready(pane); err != nil && first > 0 {
		i = first
		p, err = d.readyWindow(pane)
	}
	d.mu.Lock()
	held := errors.Is(err, errDraft)
	if held != d.mail.held[pane] {
		if held {
			d.mail.held[pane] = true
		} else {
			delete(d.mail.held, pane)
		}
		d.changed()
	}
	if (held || first >= 0 && err != nil) && !d.closing && len(d.mail.queue[pane]) > 0 {
		d.wake(pane, sendLook.get())
	}
	d.mu.Unlock()
	if err != nil {
		return
	}
	d.mu.Lock()
	q := d.mail.queue[pane]
	if i >= len(q) { // the pane closed
		d.mu.Unlock()
		return
	}
	l := q[i]
	if len(q) == 1 {
		delete(d.mail.queue, pane)
	} else {
		d.mail.queue[pane] = slices.Delete(q, i, i+1)
		d.wake(pane, 0) // why: it waits for this turn, or sendStart if the turn never reports
	}
	d.mail.typed[pane] = time.Now()
	delete(d.replies, pane)
	text := l.text
	if !l.plain {
		text = d.header(l.from) + "\n" + text
	}
	// why: here, with the letter leaving the queue, so no state shows it in neither.
	if l.seq > d.mail.delivered[pane] {
		d.mail.delivered[pane] = l.seq
	}
	d.changed()
	d.mu.Unlock()
	if err := d.typeLetter(pane, p, l.images, text); err != nil {
		log.Printf("pane %s: a message was lost: %q", pane, err)
	} else if l.notice != "" {
		d.mu.Lock()
		if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == pane }); i >= 0 {
			d.st.Notice = l.notice + string(d.st.Panes[i].Provider)
			d.changed()
		}
		d.mu.Unlock()
	}
}

// noteUserKey records the user's input into pane: it holds messages for
// sendQuiet, resets the loop guard and drops a resume after a usage
// limit. Focus and mouse reports are no keys.
func (d *Daemon) noteUserKey(pane string, b []byte) {
	if s := string(b); s == "\x1b[I" || s == "\x1b[O" || strings.HasPrefix(s, "\x1b[<") || strings.HasPrefix(s, "\x1b[M") {
		return
	}
	d.mu.Lock()
	dropped := false
	defer func() {
		d.mu.Unlock()
		if dropped {
			d.save()
		}
	}()
	d.mail.keyAt[pane] = time.Now()
	if len(b) > 0 { // a reply or an answer from the phone leaves no draft
		if !d.mail.drafting[pane] {
			d.mail.drafting[pane] = true
			d.changed()
		}
		delete(d.mail.returnAt, pane)
		if p := d.panes[pane]; p != nil && bytes.Equal(b, press(key.NameReturn, p.Modes())) {
			d.mail.returnAt[pane] = time.Now()
			delete(d.mail.daemonPrompt, pane)
		}
	}
	delete(d.mail.sent, pane)
	dropped = d.dropResume(pane)
	if dropped {
		d.changed()
	}
	if len(d.mail.queue[pane]) > 0 {
		d.wake(pane, 0)
	}
}

// notePrompt ends the user's typing in pane when a prompt goes in right after
// they pressed Return with no key since: what they typed went with it. Any
// other turn leaves their draft in place. Callers hold d.mu.
func (d *Daemon) notePrompt(pane string, now time.Time) {
	if t, ok := d.mail.returnAt[pane]; ok && now.Sub(t) < sendQuiet.get() {
		delete(d.mail.returnAt, pane)
		delete(d.mail.drafting, pane)
		d.changed()
	}
}

// drafted reports whether screen g of agent prov in pane holds the user's
// draft. Callers hold d.mu.
func (d *Daemon) drafted(pane string, prov model.Provider, g vt.Grid) bool {
	return d.mail.drafting[pane] && agent.Draft(prov, g)
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

// recordPrompt remembers a user submission without saving it across restarts.
func (d *Daemon) recordPrompt(pane string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.recordPromptLocked(pane)
}

func (d *Daemon) recordPromptLocked(pane string) {
	for i := range d.st.Panes {
		if p := &d.st.Panes[i]; p.ID == pane {
			p.LastPromptAt = time.Now()
			d.changed()
			return
		}
	}
}
