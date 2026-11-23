//go:build !nogui

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/app"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
)

// chatCache is the transcript of a chat the window shows, kept up to date
// by the deltas a proto.ChatSubscribe has the daemon push, or against a
// daemon below proto.SubscribeLevel by polling proto.ChatRead while the
// window draws the chat, every app.ChatPoll, and every chatStream while a
// turn runs: the daemon's snapshot of its newest items, the pages of older
// history read as the reader scrolls up, and the items read whole that
// the snapshot cut.
type chatCache struct {
	id    string
	snap  *session.Snapshot
	seq   int64     // snap's seq; 0 asks for a new snapshot
	sub   bool      // a ChatSubscribe went out and stands
	stale bool      // a push missed some: the next read asks for a snapshot
	out   bool      // a ChatRead or ChatSubscribe is waiting for its reply
	at    time.Time // when the last ChatRead went out
	shown time.Time // when the window last asked for the transcript
	first time.Time // when the first ChatRead went out
	err   string    // why no transcript came, "" while one may

	older  []session.Item // the pages read, oldest first
	turns  []session.TurnSummary
	more   int64     // the cursor before older; 0 when they reach the start
	paging time.Time // when the page read that is out went out, zero for none

	full  map[string]session.Item // the items read whole, by id
	asked map[string]string       // the whole items asked for, by id: the UpdatedAt asked at

	view *session.Snapshot // snap with older and full, made when it is nil
}

// readWait is how long the window waits on a ChatRead's reply before it
// says so and lets the user ask again.
const readWait = 15 * time.Second

// chatStream is how often the window reads a chat while its turn runs:
// once a frame, so a reply grows as it streams. A read goes out only
// after the last one's reply, so a slow link reads as often as it
// answers, and a reply that changes nothing draws no frame.
const chatStream = time.Second / 60

// chatAfter runs f after d, off the caller's goroutine; tests stop it.
var chatAfter = func(d time.Duration, f func()) { time.AfterFunc(d, f) }

// chatShown is how long a chat counts as shown after the window last
// asked for it: it draws every chat on screen each frame.
const chatShown = time.Second

// Chat implements app.Chatter: the chat's transcript as last read, and a
// ChatRead for what came since (chatRead). A chat new to the window drops
// the ones it no longer shows, so the window keeps one per chat on screen.
func (b *backend) Chat(id string) (session.Snapshot, bool) {
	b.mu.Lock()
	now := time.Now()
	c := b.chats[id]
	var off []string
	if c == nil {
		for k, o := range b.chats {
			if now.Sub(o.shown) > chatShown {
				delete(b.chats, k)
				if o.sub {
					off = append(off, k)
				}
			}
		}
		if b.chats == nil {
			b.chats = map[string]*chatCache{}
		}
		c = &chatCache{id: id}
		b.chats[id] = c
	}
	c.shown = now
	var s session.Snapshot
	ok := c.snap != nil
	if ok {
		s = *c.merged()
	}
	b.mu.Unlock()
	for _, k := range off {
		b.Send(proto.ChatSubscribe{Chat: k, Off: true})
	}
	b.chatRead(id)
	return s, ok
}

// chatIdle drops chat id's subscription once the window stopped drawing
// it for chatShown, and looks again before then.
func (b *backend) chatIdle(id string) {
	b.mu.Lock()
	c := b.chats[id]
	if c == nil || !c.sub {
		b.mu.Unlock()
		return
	}
	if wait := chatShown - time.Since(c.shown); wait > 0 {
		b.mu.Unlock()
		chatAfter(wait, func() { b.chatIdle(id) })
		return
	}
	delete(b.chats, id)
	b.mu.Unlock()
	b.Send(proto.ChatSubscribe{Chat: id, Off: true})
}

// chatRead subscribes to chat id while the window shows it, or reads it
// again when a push missed some; against an older daemon, it sends a
// ChatRead for what the chat gained when none is out and the last went
// out app.ChatPoll ago, or chatStream while a turn runs.
func (b *backend) chatRead(id string) {
	b.mu.Lock()
	c := b.chats[id]
	if c == nil {
		b.mu.Unlock()
		return
	}
	now := time.Now()
	if c.out && now.Sub(c.at) >= readWait {
		log.Printf("chat %s: no reply to a read in %v", id, readWait)
		if c.snap == nil {
			c.err = fmt.Sprintf("The daemon sent no transcript in %d seconds.", int(readWait.Seconds()))
		} else {
			c.out = false // the transcript stays; read again
		}
	}
	push := b.link.Level >= proto.SubscribeLevel
	every := app.ChatPoll
	if c.snap != nil && c.snap.Busy() || push && c.sub {
		every = chatStream
	}
	if c.out || c.err != "" || now.Sub(c.at) < every || now.Sub(c.shown) > chatShown || push && c.sub && !c.stale {
		b.mu.Unlock()
		return
	}
	c.out, c.at = true, now
	if c.first.IsZero() {
		c.first = now
	}
	var read any = proto.ChatRead{Chat: id, After: c.seq}
	if push && !c.sub {
		read, c.sub = proto.ChatSubscribe{Chat: id, After: c.seq}, true
		chatAfter(chatShown, func() { b.chatIdle(id) })
	}
	b.mu.Unlock()
	if err := b.Send(read); err != nil {
		b.mu.Lock()
		c.out = false
		if _, ok := read.(proto.ChatSubscribe); ok {
			c.sub = false
		}
		b.mu.Unlock()
	}
}

// ChatWait implements app.ChatLoader.
func (b *backend) ChatWait(id string) (time.Time, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.chats[id]
	if c == nil {
		return time.Time{}, ""
	}
	return c.first, c.err
}

// ChatRetry implements app.ChatLoader.
func (b *backend) ChatRetry(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if c := b.chats[id]; c != nil {
		c.err, c.out, c.seq, c.first, c.sub = "", false, 0, time.Time{}, false
	}
}

// ChatOlder implements app.ChatPager: it reads the page before the oldest
// item, when none is out.
func (b *backend) ChatOlder(id string) bool {
	b.mu.Lock()
	c := b.chats[id]
	if c == nil || c.more == 0 {
		b.mu.Unlock()
		return false
	}
	now := time.Now()
	read := proto.ChatRead{Chat: id, Before: c.more}
	send := c.paging.IsZero() || now.Sub(c.paging) >= readWait
	if send {
		c.paging = now
	}
	b.mu.Unlock()
	if send && b.Send(read) != nil {
		b.mu.Lock()
		c.paging = time.Time{}
		b.mu.Unlock()
	}
	return true
}

// ChatItem implements app.ChatPager: it reads item whole, once per
// UpdatedAt, since merged shows a whole copy only at its cut copy's.
func (b *backend) ChatItem(id, item string) {
	b.mu.Lock()
	c := b.chats[id]
	if c == nil {
		b.mu.Unlock()
		return
	}
	at := c.stamp(item)
	if was, ok := c.asked[item]; ok && was == at {
		b.mu.Unlock()
		return
	}
	if c.asked == nil {
		c.asked = map[string]string{}
	}
	c.asked[item] = at
	b.mu.Unlock()
	b.Send(proto.ChatRead{Chat: id, Item: item})
}

// stamp is item's UpdatedAt in the transcript, "" when it holds no item.
// b.mu is held.
func (c *chatCache) stamp(item string) string {
	if c.snap == nil {
		return ""
	}
	for _, items := range [][]session.Item{c.snap.Content, c.older} {
		if i := slices.IndexFunc(items, func(it session.Item) bool { return it.ID == item }); i >= 0 {
			return items[i].UpdatedAt
		}
	}
	return ""
}

// gone takes item's cut copy for whole once the daemon no longer has the
// item: what the copy lacks, its images' data among it, is gone, and the
// window says so instead of waiting. b.mu is held.
func (c *chatCache) gone(item string) {
	if c.snap == nil {
		return
	}
	for _, items := range [][]session.Item{c.snap.Content, c.older} {
		if i := slices.IndexFunc(items, func(it session.Item) bool { return it.ID == item }); i >= 0 {
			it := items[i]
			it.Cut = false
			if c.full == nil {
				c.full = map[string]session.Item{}
			}
			c.full[item], c.view = it, nil
			return
		}
	}
}

// merged is snap with the pages before it and the items read whole, made
// again only after a change. b.mu is held.
func (c *chatCache) merged() *session.Snapshot {
	if c.view != nil {
		return c.view
	}
	s := *c.snap
	if len(c.older) > 0 || len(c.full) > 0 {
		s.Content = append(slices.Clip(c.older), c.snap.Content...)
		for i, it := range s.Content {
			if f, ok := c.full[it.ID]; ok && it.Cut && f.UpdatedAt == it.UpdatedAt {
				s.Content[i] = f
			}
		}
		s.Turns = append(slices.Clip(c.turns), c.snap.Turns...)
	}
	c.view = &s
	return c.view
}

// chatTook applies a ChatRead's reply or a push to its chat's transcript
// (take), schedules the next read (again) and reports whether it changed.
// b.mu is held.
func (b *backend) chatTook(m proto.ChatDeltas) bool {
	c := b.chats[m.Chat]
	if c == nil {
		return false
	}
	changed := c.take(m)
	if after, ok := c.again(m.Chat, changed); ok {
		chatAfter(after, func() { b.chatRead(m.Chat) })
	}
	return changed
}

// again is when to read chat id next without waiting for a frame: at once
// when a subscribed chat missed a push, else none while subscribed;
// chatStream while its turn runs, and app.ChatPoll after a reply that
// changed it, so what lands as a turn ends shows though the window draws
// no frame for a chat that went idle. Callers hold b.mu.
func (c *chatCache) again(id string, changed bool) (time.Duration, bool) {
	if c.id != id || c.out {
		return 0, false
	}
	if c.sub {
		return chatStream, c.stale
	}
	if c.snap == nil {
		return 0, false
	}
	if c.snap.Busy() {
		return chatStream, true
	}
	return app.ChatPoll, changed
}

// ChatResults implements app.ChatResulter.
func (b *backend) ChatResults(id string) []chatin.Result {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.chatOut[id]
	delete(b.chatOut, id)
	return r
}

// take applies a ChatRead's reply or a push and reports whether the
// transcript changed: a page goes before the items, an item read whole
// stands in for its cut copy, and a snapshot whose first item is not in
// the transcript drops the pages. Deltas it has are skipped. A gap, or a
// delta that does not apply, makes the next read ask for a snapshot, and
// the old transcript stays on screen until it comes.
func (c *chatCache) take(m proto.ChatDeltas) bool {
	if m.Chat != c.id {
		return false
	}
	switch {
	case m.Item != nil:
		var it session.Item
		if err := json.Unmarshal(m.Item, &it); err != nil {
			log.Printf("chat %s: item: %q", c.id, err)
			return false
		}
		if c.full == nil {
			c.full = map[string]session.Item{}
		}
		c.full[it.ID], c.view = it, nil
		return true
	case m.Page != 0:
		c.paging = time.Time{}
		var s session.Snapshot
		if m.Page != c.more || json.Unmarshal(m.Snapshot, &s) != nil {
			return false // asked of a snapshot since replaced
		}
		c.older = append(s.Content, c.older...)
		c.turns = append(s.Turns, c.turns...)
		c.more, c.view = m.Older, nil
		return true
	}
	if !m.Push {
		c.out, c.err = false, ""
	}
	changed := false
	if m.Snapshot != nil {
		var s session.Snapshot
		if err := json.Unmarshal(m.Snapshot, &s); err != nil {
			log.Printf("chat %s: snapshot: %q", c.id, err)
			c.seq, c.stale = 0, true
			return false
		}
		if !c.rebase(s) { // a managed chat's cursor is an index that moves with each new item
			c.older, c.turns, c.more, c.paging = nil, nil, m.Older, time.Time{}
		}
		c.snap, c.seq, c.view, c.stale, changed = &s, m.Seq, nil, false, true
	}
	if c.snap == nil || c.stale || m.Seq > c.seq {
		c.seq, c.stale = 0, c.stale || c.snap != nil // a gap; a read for a snapshot that is out answers it
		return changed
	}
	reread := false
	for _, raw := range m.Deltas {
		var d session.Delta
		err := json.Unmarshal(raw, &d)
		if err == nil && d.Seq <= c.seq {
			continue // a push the snapshot already holds
		}
		var n session.Snapshot
		if err == nil {
			n, err = session.Apply(c.snap, c.seq, d)
		}
		if err != nil {
			log.Printf("chat %s: delta after %d: %q", c.id, c.seq, err)
			c.seq, c.stale = 0, true
			return changed
		}
		// A snapshot delta, as a rewind commits, holds the daemon's whole
		// snapshot: a managed chat's every item, a pane chat's tail.
		if d.Type == session.DeltaSnapshot && !c.rebase(n) {
			c.older, c.turns, c.more, c.paging = nil, nil, 0, time.Time{}
			reread = true // a snapshot's cursor tells where the history before it starts
		}
		c.snap, c.seq, c.view, changed = &n, d.Seq, nil, true
	}
	if reread {
		c.seq, c.stale = 0, true
	}
	return changed
}

// rebase keeps the history before n that the transcript holds, once n's
// first item is in it, and reports whether it was. b.mu is held.
func (c *chatCache) rebase(n session.Snapshot) bool {
	if len(n.Content) == 0 || c.snap == nil {
		return false
	}
	old := slices.Concat(c.older, c.snap.Content)
	p := slices.IndexFunc(old, func(it session.Item) bool { return it.ID == n.Content[0].ID })
	if p < 0 {
		return false
	}
	turns := c.turns
	if p > len(c.older) {
		turns = slices.Concat(c.turns, c.snap.Turns)
	}
	c.older = old[:p:p]
	c.turns = slices.DeleteFunc(slices.Clone(turns), func(t session.TurnSummary) bool {
		return slices.ContainsFunc(n.Turns, func(u session.TurnSummary) bool { return u.ID == t.ID })
	})
	return true
}

// refused takes the daemon's refusal of read r: the first read's says why
// the chat shows no transcript; a page's ends the paging until the next
// snapshot; an item's stays asked until the item changes.
func (c *chatCache) refused(r proto.ChatRead, why string) {
	log.Printf("chat %s: read %+v refused: %s", c.id, r, why)
	switch {
	case r.Item != "":
	case r.Before != 0:
		c.paging, c.more = time.Time{}, 0
	default:
		c.out = false
		if c.snap == nil {
			c.err = why
		}
	}
}
