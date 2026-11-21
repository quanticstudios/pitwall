package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// chatQueue is what a pane chat queued mid-turn, in order: each message's
// row, as the chat shows it, and the letter that types it. A row's letter
// waits in d.mail.queue, so deliver types it once the turn ends, unless
// the row failed or the queue is held; then it waits here. Options.Queues
// keeps every queue, so a restart or a newer daemon finds them.
type chatQueue struct {
	rows []chatRow
	held bool // the chat stopped the turn: the rows wait for the next send or Resume
	// sending is what deliver took to type and has not finished typing: a
	// restart that finds it there shows it as maybe sent (mayHaveSent).
	sending []chatRow
	// errOK is the UpdatedAt of the failed turn whose hold Send let go.
	errOK time.Time
	// reason is why the live rows wait although the turn ended
	// (queueWait); paused when only the user's Send lets them go.
	reason string
	paused bool
	// started: the agent a restart resumed was back at its box once
	// (resumeStarted), so a later misread of its screen holds nothing.
	started bool
}

type chatRow struct {
	in session.QueuedInput
	l  letter
}

// live reports whether row r's letter belongs in d.mail.queue.
func (q *chatQueue) live(r chatRow) bool { return !q.held && r.in.Error == "" }

// chatEnqueue adds message in, typed by l, to pane's queue. Callers hold
// d.mu.
func (d *Daemon) chatEnqueue(pane string, in session.QueuedInput, l letter) {
	q := d.mail.chat[pane]
	if q == nil {
		q = &chatQueue{}
		d.mail.chat[pane] = q
	}
	l.chat = in.ID
	q.rows = append(q.rows, chatRow{in, l})
	// why: the queue goes on with the work a limit stopped when it resets, so the limit resume would continue twice.
	if d.dropResume(pane) {
		d.changed()
	}
	d.chatSync(pane)
}

// chatSync puts pane's live rows' letters in d.mail.queue, in the rows'
// order, in the places its chat letters held, wakes deliver and saves the
// queues. Callers hold d.mu.
func (d *Daemon) chatSync(pane string) {
	q := d.mail.chat[pane]
	var live []letter
	if q != nil {
		for _, r := range q.rows {
			if q.live(r) {
				live = append(live, r.l)
			}
		}
		if len(q.rows) == 0 && len(q.sending) == 0 {
			delete(d.mail.chat, pane)
		}
	}
	old := d.mail.queue[pane]
	out := make([]letter, 0, len(old)+len(live))
	k := 0
	for _, l := range old {
		switch {
		case l.chat == "":
			out = append(out, l)
		case k < len(live):
			out = append(out, live[k])
			k++
		}
	}
	out = append(out, live[k:]...)
	if len(out) == 0 {
		delete(d.mail.queue, pane)
	} else {
		d.mail.queue[pane] = out
		d.wake(pane, 0)
	}
	d.queuesChanged()
	d.changed()
}

// chatTake takes letter i of pane's queue q, a chat row's, out of q
// together with the plain rows right after it, which go in the same
// prompt (joins), and returns the letter that types them all and how
// many letters of q it stands for. The rows wait in sending until
// chatSent. Callers hold d.mu.
func (d *Daemon) chatTake(pane string, q []letter, i int) (letter, int) {
	cq := d.mail.chat[pane]
	var rows []chatRow
	for _, l := range q[i:] {
		k := -1
		if cq != nil && l.chat != "" {
			k = slices.IndexFunc(cq.rows, func(r chatRow) bool { return r.in.ID == l.chat })
		}
		if k < 0 {
			break
		}
		rows = append(rows, cq.rows[k])
	}
	if len(rows) == 0 {
		return q[i], 1
	}
	texts, alone, images := make([]string, len(rows)), make([]bool, len(rows)), make([]int, len(rows))
	for k, r := range rows {
		texts[k], alone[k], images[k] = r.l.text, r.in.Alone, len(r.l.images)
	}
	rows = rows[:joins(texts, alone)]
	l := rows[0].l
	if len(rows) > 1 {
		l.text = joinPrompts(texts[:len(rows)], images)
		l.images, l.quoted = nil, nil
		for _, r := range rows {
			l.images = append(l.images, r.l.images...)
			l.quoted = append(l.quoted, r.l.quoted...)
		}
	}
	for _, r := range rows {
		cq.rows = slices.DeleteFunc(cq.rows, func(o chatRow) bool { return o.in.ID == r.in.ID })
	}
	cq.sending = rows
	if d.dropResume(pane) { // the queue goes on with the work a limit stopped
		d.changed()
	}
	d.queuesChanged()
	return l, len(rows)
}

// chatSent ends the typing chatTake began: err nil drops the rows, else
// they go back to the head of the queue with err, to wait for Retry.
// Callers hold d.mu.
func (d *Daemon) chatSent(pane string, err error) {
	q := d.mail.chat[pane]
	if q == nil || len(q.sending) == 0 {
		return
	}
	rows := q.sending
	q.sending = nil
	if err == nil {
		d.chatSync(pane)
		return
	}
	for i := range rows {
		rows[i].in.Error = err.Error()
	}
	q.rows = slices.Insert(q.rows, 0, rows...)
	d.chatSync(pane)
}

// chatHold holds pane's queue, as a managed chat's interrupt does, or
// lets it go again. Callers hold d.mu.
func (d *Daemon) chatHold(pane string, held bool) {
	if q := d.mail.chat[pane]; q != nil && q.held != held {
		q.held = held
		d.chatSync(pane)
	}
}

// chatQueued is pane's queue as a chat snapshot shows it, and whether it
// is held. Callers hold d.mu.
func (d *Daemon) chatQueued(pane string) (rows []session.QueuedInput, held bool, reason string) {
	q := d.mail.chat[pane]
	if q == nil || len(q.rows) == 0 {
		return nil, false, ""
	}
	out := make([]session.QueuedInput, len(q.rows))
	for i, r := range q.rows {
		out[i] = r.in
	}
	if q.held {
		return out, true, ""
	}
	return out, q.paused, q.reason
}

// paneSteers reports whether an agent in a pane takes a message typed
// mid-turn into the running turn. Claude Code 2.1.330 holds Enter's
// message until the running tool returns, then adds it to the turn; Codex
// 0.171's Enter steers at once (Tab would queue it); Antigravity CLI
// 1.5.13's Enter queues a turn of its own, and Alt+M steers (steerKey).
func paneSteers(p model.Provider) bool {
	return p == model.ProviderClaude || p == model.ProviderCodex || p == model.ProviderAntigravity
}

// paneQueue runs ChatQueue m on pane chat pane's queue: edit, move and
// remove a row, mark it to go separately or with the others; now (or
// steer) types one into the running turn (paneSteers), or at once when
// idle; resume lets a held queue go, past a failed turn too.
func (d *Daemon) paneQueue(pane string, m proto.ChatQueue) error {
	d.mu.Lock()
	if m.Action == "resume" {
		if q := d.mail.chat[pane]; q != nil {
			if i := d.activityIndex(pane); i >= 0 && d.st.Activities[i].State == model.StateError {
				q.errOK = d.st.Activities[i].UpdatedAt
			}
			q.paused = false
			if d.queueOrphaned(pane, q) {
				prov := d.paneProvider(pane)
				if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == pane }); !paneSteers(prov) || i < 0 || d.st.Panes[i].Exited {
					d.mu.Unlock()
					return invalid("no agent runs in this pane to send them to")
				}
				for i := range q.rows {
					q.rows[i].l.owner = prov // Send hands them to the agent there now
				}
			}
		}
		d.chatHold(pane, false)
		d.chatSync(pane)
		d.mu.Unlock()
		return nil
	}
	q := d.mail.chat[pane]
	i := -1
	if q != nil {
		i = slices.IndexFunc(q.rows, func(r chatRow) bool { return r.in.ID == m.ID })
	}
	if i < 0 {
		d.mu.Unlock()
		return invalid("no queued message %s", m.ID)
	}
	var now *chatRow
	switch m.Action {
	case "edit":
		if strings.TrimSpace(m.Text) == "" {
			d.mu.Unlock()
			return invalid("nothing to send")
		}
		r := &q.rows[i]
		// why: paneSend typed the text without image tokens, after a skill's /name.
		skill := ""
		if s, ok := strings.CutSuffix(r.l.text, strings.TrimSpace(imageToken.ReplaceAllString(r.in.Text, ""))); ok && strings.HasPrefix(s, "/") {
			skill = s
		}
		r.in.Text, r.l.text = m.Text, skill+strings.TrimSpace(imageToken.ReplaceAllString(m.Text, ""))
	case "move":
		if m.Index < 0 || m.Index >= len(q.rows) {
			d.mu.Unlock()
			return invalid("queue index out of range")
		}
		r := q.rows[i]
		q.rows = slices.Insert(slices.Delete(q.rows, i, i+1), m.Index, r)
	case "remove":
		q.rows = slices.Delete(q.rows, i, i+1)
	case "separate", "together":
		q.rows[i].in.Alone = m.Action == "separate"
	case "now", "steer":
		r := q.rows[i]
		q.rows = slices.Delete(q.rows, i, i+1)
		now = &r
	default:
		d.mu.Unlock()
		return invalid("unknown queue action %q", m.Action)
	}
	d.chatSync(pane)
	var l letter
	if now != nil {
		// A phone letter: Claude Code takes it into the running turn, or types it now.
		d.phoneSeq++
		l = now.l
		l.chat, l.phone, l.owner = "", d.phoneSeq, d.paneProvider(pane)
	}
	d.mu.Unlock()
	if now == nil {
		return nil
	}
	if err := d.userDeliver(pane, l); err != nil {
		d.mu.Lock()
		now.in.Error = err.Error()
		if q := d.mail.chat[pane]; q != nil {
			q.rows = slices.Insert(q.rows, 0, *now)
		} else {
			d.mail.chat[pane] = &chatQueue{rows: []chatRow{*now}}
		}
		d.chatSync(pane)
		d.mu.Unlock()
		return err
	}
	return nil
}

// paneBusy reports whether pane's agent is mid-turn. Callers hold d.mu.
func (d *Daemon) paneBusy(pane string) bool {
	i := d.activityIndex(pane)
	return i >= 0 && busy(d.st.Activities[i].State) && !d.st.Activities[i].Background
}

// chatReason works out why pane's chat queue waits (queueWait) and
// returns when to look again: a limit's reset, sendLook for a screen
// that holds it, zero for nothing or a hook's next report.
func (d *Daemon) chatReason(pane string) (wait bool, again time.Duration) {
	d.mu.Lock()
	q, p := d.mail.chat[pane], d.panes[pane]
	d.mu.Unlock()
	if q == nil {
		return false, 0
	}
	var g vt.Grid
	if p != nil {
		g = p.Snapshot()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.mail.chat[pane] != q {
		return false, 0
	}
	why, paused, until := d.queueWait(pane, q, g)
	if why != q.reason || paused != q.paused {
		q.reason, q.paused = why, paused
		d.changed()
	}
	switch {
	case why == "":
		return false, 0
	case !until.IsZero():
		return true, max(0, time.Until(until))
	case why == waitAnswer || why == waitStart:
		return true, sendLook.get() // a form leaves the screen, and an agent draws its box, with no hook
	}
	return true, 0
}

// queueWait is why pane's chat queue q waits although its agent may be
// done with the turn, "" when nothing does: a question, an approval or a
// plan, a form on screen g, a usage limit until it resets (until), or a
// failed turn, which waits for the user's Send (paused). Callers hold
// d.mu.
func (d *Daemon) queueWait(pane string, q *chatQueue, g vt.Grid) (why string, paused bool, until time.Time) {
	if d.queueOrphaned(pane, q) {
		return waitGone, true, time.Time{}
	}
	var a model.Activity
	if i := d.activityIndex(pane); i >= 0 {
		a = d.st.Activities[i]
	}
	if a.State == model.StateWorking {
		return "", false, time.Time{} // the turn, which the queue waits for anyway
	}
	if at, ok := d.resumed[pane]; ok && !q.started {
		if !d.resumeStarted(pane, at, g) {
			return waitStart, false, time.Time{}
		}
		q.started = true
	}
	// why: what was queued waited for the turn a restart cut, which restartLine continues first.
	if slices.ContainsFunc(d.mail.queue[pane], func(l letter) bool { return l.chat == "" && l.text == restartLine }) {
		return waitStart, false, time.Time{}
	}
	switch a.State {
	case model.StatePendingApproval, model.StatePlanReady:
		return waitApproval, false, time.Time{}
	case model.StateAwaitingInput:
		return waitAnswer, false, time.Time{}
	case model.StateError:
		if a.UpdatedAt.Equal(q.errOK) {
			break
		}
		now := resumeNow()
		if _, resets, ok := agent.UsageLimit(a.Detail, now); ok {
			if resets.IsZero() {
				return waitFailed, true, time.Time{}
			}
			// why: as late as the limit resume would type continue, which the queue then stands in for (chatTake).
			if at := resets.Add(resumeDelay); at.After(now) {
				return waitLimit(resets), false, at
			}
			break
		}
		return waitFailed, true, time.Time{}
	}
	if g.Rows > 0 && agent.ReadScreen(g) == agent.ScreenForm {
		return waitAnswer, false, time.Time{}
	}
	return "", false, time.Time{}
}

// queueOrphaned reports whether the agent q's rows were queued for left
// pane: it exited, or another program or agent runs there. Such rows never
// go on their own, into a shell or to another agent. Callers hold d.mu.
func (d *Daemon) queueOrphaned(pane string, q *chatQueue) bool {
	for _, r := range q.rows {
		if r.l.owner != "" && d.agentGone(pane, r.l) {
			return true
		}
	}
	return false
}

// resumeStarted reports whether pane's agent, which a restart resumed at
// at, is back at its input box with screen g: restartWait has passed and
// Claude Code or Codex drew the box. A hook proves less: Claude 2.1.330
// sends Stop for a tool call the restart cut while it still loads, and
// what is typed then goes in with the next prompt, its blank lines spaces.
// Callers hold d.mu.
func (d *Daemon) resumeStarted(pane string, at time.Time, g vt.Grid) bool {
	if time.Since(at) < restartWait {
		return false
	}
	p := d.paneProvider(pane)
	return p != model.ProviderClaude && p != model.ProviderCodex || agent.InputReady(p, g)
}

// savedQueue is a chatQueue as Options.Queues keeps it, by pane.
type savedQueue struct {
	Rows    []savedRow `json:"rows,omitempty"`
	Held    bool       `json:"held,omitempty"`
	Sending []savedRow `json:"sending,omitempty"`
	ErrOK   time.Time  `json:"errOK,omitzero"`
}

type savedRow struct {
	In     session.QueuedInput `json:"in"`
	Text   string              `json:"text"`
	Images []string            `json:"images,omitempty"`
	Quoted []string            `json:"quoted,omitempty"`
	Owner  model.Provider      `json:"owner,omitempty"` // the agent it was queued for
}

func saveRows(rows []chatRow) []savedRow {
	out := make([]savedRow, len(rows))
	for i, r := range rows {
		out[i] = savedRow{In: r.in, Text: r.l.text, Images: r.l.images, Quoted: r.l.quoted, Owner: r.l.owner}
	}
	return out
}

func loadRows(rows []savedRow) []chatRow {
	out := make([]chatRow, len(rows))
	for i, r := range rows {
		out[i] = chatRow{r.In, letter{text: r.Text, images: r.Images, quoted: r.Quoted, plain: true, window: true, chat: r.In.ID, owner: r.Owner}}
	}
	return out
}

// queuesJSON is every pane chat's queue as Options.Queues keeps it, and
// its generation. Callers hold d.mu.
func (d *Daemon) queuesJSON() ([]byte, uint64) {
	all := map[string]savedQueue{}
	for pane, q := range d.mail.chat {
		if len(q.rows)+len(q.sending) == 0 {
			continue
		}
		all[pane] = savedQueue{Rows: saveRows(q.rows), Held: q.held, Sending: saveRows(q.sending), ErrOK: q.errOK}
	}
	b, _ := json.MarshalIndent(all, "", "  ")
	d.queueGen++
	return append(b, '\n'), d.queueGen
}

// queuesChanged saves the queues soon. Callers hold d.mu.
func (d *Daemon) queuesChanged() {
	if d.o.Queues != "" && !d.closing && !d.frozen {
		go d.saveQueues()
	}
}

// saveQueues writes Options.Queues now, unless the daemon is closing or
// handing over, when the panes it removes are no reason to drop their
// queues.
func (d *Daemon) saveQueues() {
	if d.o.Queues == "" {
		return
	}
	d.mu.Lock()
	if d.closing || d.frozen {
		d.mu.Unlock()
		return
	}
	data, gen := d.queuesJSON()
	d.mu.Unlock()
	d.writeQueues(data, gen)
}

// writeQueues writes data, the queues' generation gen, to Options.Queues
// unless a later generation or the same data is there already. It takes
// queueMu, which is never held while taking d.mu.
func (d *Daemon) writeQueues(data []byte, gen uint64) {
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	if gen <= d.queueWrote || bytes.Equal(data, d.queueSaved) {
		return
	}
	d.queueWrote = gen
	if err := store.WriteAtomic(d.o.Queues, data); err != nil {
		log.Printf("chat queues: %q", err)
		return
	}
	d.queueSaved = data
}

// loadQueues brings back the pane chats' queues Options.Queues kept, for
// the panes the state has. What was being typed comes back first, as
// maybe sent, to wait for Retry. Callers hold d.mu.
func (d *Daemon) loadQueues() {
	if d.o.Queues == "" {
		return
	}
	data, err := os.ReadFile(d.o.Queues)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var all map[string]savedQueue
	if err == nil {
		err = json.Unmarshal(data, &all)
	}
	if err != nil {
		log.Printf("chat queues: %q", err)
		return
	}
	for pane, s := range all {
		if !slices.ContainsFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == pane }) {
			continue
		}
		q := &chatQueue{rows: loadRows(s.Rows), held: s.Held, errOK: s.ErrOK}
		sent := loadRows(s.Sending)
		for i := range sent {
			sent[i].in.Error = mayHaveSent
		}
		q.rows = append(sent, q.rows...)
		d.mail.chat[pane] = q
		d.chatSync(pane)
	}
}

// queueHeld is a queue's ChatQueue.Held: its reason, or Paused when only
// Resume lets it go.
func queueHeld(paused bool, reason string) string {
	if reason == "" && paused {
		return "Paused"
	}
	return reason
}

// chatQueues is State.ChatQueues: every pane chat's queue and every
// chat's of chats. Callers hold d.mu.
func (d *Daemon) chatQueues(chats []model.Chat) map[string]model.ChatQueue {
	var out map[string]model.ChatQueue
	add := func(id string, q model.ChatQueue) {
		if out == nil {
			out = map[string]model.ChatQueue{}
		}
		out[id] = q
	}
	for pane := range d.mail.chat {
		if rows, held, reason := d.chatQueued(pane); len(rows) > 0 {
			add(pane, model.ChatQueue{N: len(rows), Held: queueHeld(held, reason)})
		}
	}
	for _, c := range chats {
		if c.Queued > 0 {
			add(c.ID, model.ChatQueue{N: c.Queued, Held: c.QueueHeld})
		}
	}
	return out
}
