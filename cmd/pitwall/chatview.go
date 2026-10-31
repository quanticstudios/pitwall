package main

import (
	"encoding/json"
	"log"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

// chatCache is the transcript of the one chat the window shows, kept up
// to date with proto.ChatRead.
//
// shortcut: the window polls ChatRead every app.ChatPoll while it draws the
// chat; a ChatSubscribe that pushes deltas would replace the polling.
type chatCache struct {
	id   string
	snap *session.Snapshot
	seq  int64     // snap's seq; 0 asks for a new snapshot
	out  bool      // a ChatRead is waiting for its reply
	at   time.Time // when the last ChatRead went out
}

// Chat implements app.Chatter: the chat's transcript as last read, and a
// ChatRead for what came since when none is out and the last is
// app.ChatPoll old. Asking for another chat drops the one kept.
func (b *backend) Chat(id string) (session.Snapshot, bool) {
	b.mu.Lock()
	c := &b.chat
	if c.id != id {
		*c = chatCache{id: id}
	}
	var read *proto.ChatRead
	if now := time.Now(); !c.out && now.Sub(c.at) >= app.ChatPoll {
		c.out, c.at = true, now
		read = &proto.ChatRead{Chat: id, After: c.seq}
	}
	var s session.Snapshot
	ok := c.snap != nil
	if ok {
		s = *c.snap
	}
	b.mu.Unlock()
	if read != nil {
		if err := b.Send(*read); err != nil {
			b.mu.Lock()
			if b.chat.id == id {
				b.chat.out = false
			}
			b.mu.Unlock()
		}
	}
	return s, ok
}

// take applies a ChatRead's reply and reports whether the transcript
// changed. A delta that does not apply makes the next read ask for a
// snapshot, and the old transcript stays on screen until it comes.
func (c *chatCache) take(m proto.ChatDeltas) bool {
	if m.Chat != c.id {
		return false
	}
	c.out = false
	changed := false
	if m.Snapshot != nil {
		var s session.Snapshot
		if err := json.Unmarshal(m.Snapshot, &s); err != nil {
			log.Printf("chat %s: snapshot: %q", c.id, err)
			c.seq = 0
			return false
		}
		c.snap, c.seq, changed = &s, m.Seq, true
	}
	if c.snap == nil || c.seq != m.Seq {
		c.seq = 0
		return changed
	}
	for _, raw := range m.Deltas {
		var d session.Delta
		err := json.Unmarshal(raw, &d)
		var n session.Snapshot
		if err == nil {
			n, err = session.Apply(c.snap, c.seq, d)
		}
		if err != nil {
			log.Printf("chat %s: delta after %d: %q", c.id, c.seq, err)
			c.seq = 0
			return changed
		}
		c.snap, c.seq, changed = &n, d.Seq, true
	}
	return changed
}
