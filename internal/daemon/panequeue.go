package daemon

import (
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// chatQueue is what a pane chat queued mid-turn, in order: each message's
// row, as the chat shows it, and the letter that types it. A row's letter
// waits in d.mail.queue, so deliver types it once the turn ends, unless
// the row failed or the queue is held; then it waits here.
type chatQueue struct {
	rows []chatRow
	held bool // the chat stopped the turn: the rows wait for the next send or Resume
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
	d.chatSync(pane)
}

// chatSync puts pane's live rows' letters in d.mail.queue, in the rows'
// order, in the places its chat letters held, and wakes deliver. Callers
// hold d.mu.
func (d *Daemon) chatSync(pane string) {
	q := d.mail.chat[pane]
	var live []letter
	if q != nil {
		for _, r := range q.rows {
			if q.live(r) {
				live = append(live, r.l)
			}
		}
		if len(q.rows) == 0 {
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
	d.changed()
}

// chatTaken drops the row of chat letter l, which deliver is about to
// type, and returns it. Callers hold d.mu.
func (d *Daemon) chatTaken(pane string, l letter) (chatRow, bool) {
	q := d.mail.chat[pane]
	if q == nil || l.chat == "" {
		return chatRow{}, false
	}
	i := slices.IndexFunc(q.rows, func(r chatRow) bool { return r.in.ID == l.chat })
	if i < 0 {
		return chatRow{}, false
	}
	r := q.rows[i]
	q.rows = slices.Delete(q.rows, i, i+1)
	if len(q.rows) == 0 {
		delete(d.mail.chat, pane)
	}
	return r, true
}

// chatFailed puts row r back at the head of pane's queue with why it could
// not be typed; it waits there for Send now. Callers hold d.mu.
func (d *Daemon) chatFailed(pane string, r chatRow, err error) {
	q := d.mail.chat[pane]
	if q == nil {
		q = &chatQueue{}
		d.mail.chat[pane] = q
	}
	r.in.Error = err.Error()
	q.rows = slices.Insert(q.rows, 0, r)
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
func (d *Daemon) chatQueued(pane string) ([]session.QueuedInput, bool) {
	q := d.mail.chat[pane]
	if q == nil {
		return nil, false
	}
	out := make([]session.QueuedInput, len(q.rows))
	for i, r := range q.rows {
		out[i] = r.in
	}
	return out, q.held
}

// paneQueue runs ChatQueue m on pane chat pane's queue: edit, move and
// remove a row; now (or steer) types one into the running turn, or at
// once when idle; resume lets a held queue go.
func (d *Daemon) paneQueue(pane string, m proto.ChatQueue) error {
	d.mu.Lock()
	if m.Action == "resume" {
		d.chatHold(pane, false)
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
		d.chatFailed(pane, *now, err)
		d.mu.Unlock()
		return err
	}
	return nil
}
