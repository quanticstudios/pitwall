package web

// Subscriptions, resync and backpressure (docs/web-protocol.md). Each
// connection has one outgoing queue and one writer. A chat's loop pushes
// its numbered deltas into the queues of the connections that subscribe;
// snapshots and the session list are built when the writer reaches them,
// so a client that falls behind holds at most one of each.

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// Chats is what the server streams; the daemon implements it.
type Chats interface {
	// Subscribe adds s to chat id's subscribers; Unsubscribe removes it.
	Subscribe(id string, s Sink) error
	Unsubscribe(id string, s Sink)
	// Read is ChatRead: the deltas after seq after, or a snapshot when
	// after is 0 or they are gone.
	Read(id string, after int64) (proto.ChatDeltas, error)
	// List is the live chats. WatchList(s, true) has s told when it may
	// have changed, false stops that.
	List() []model.Chat
	WatchList(s Sink, on bool)
}

// Sink is a connection's end of its subscriptions. The daemon calls it on
// a chat's loop and under its own lock, so no method blocks.
type Sink interface {
	Deltas(proto.ChatDeltas) // the deltas after Seq, as the chat numbers them
	Removed(id string)       // the chat was archived or deleted
	ListChanged()
}

// The queue limits: past either, the queued deltas and lists are dropped
// for fresh snapshots. Vars for tests.
var (
	maxQueue      = 256
	maxQueueBytes = 4 << 20
)

// out is one entry of a connection's queue.
type out struct {
	frame []byte           // result, error, removed: never dropped
	batch proto.ChatDeltas // pushed deltas of batch.Chat
	size  int              // batch's bytes
	base  string           // a chat whose deltas after `after`, or snapshot, the writer reads
	after int64
	list  bool // the session list, whole or what changed
}

func (o out) dropped() bool { return o.frame == nil }

// sub is one chat a connection subscribes to.
type sub struct {
	pending bool  // a base waits in the queue; pushes before it are in it
	sent    int64 // the seq of the last delta written
}

var _ Sink = (*conn)(nil)

func (c *conn) push(o out) {
	c.q = append(c.q, o)
	if o.batch.Chat != "" {
		c.held++
		c.bytes += o.size
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *conn) reply(v any) {
	data, _ := json.Marshal(v)
	c.mu.Lock()
	c.push(out{frame: data})
	c.mu.Unlock()
}

func (c *conn) Deltas(d proto.ChatDeltas) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s := c.subs[d.Chat]; s == nil || s.pending {
		return
	}
	size := 0
	for _, b := range d.Deltas {
		size += len(b)
	}
	if c.held+1 > maxQueue || c.bytes+size > maxQueueBytes {
		c.overflow()
		return
	}
	c.push(out{batch: d, size: size})
}

// overflow drops every queued delta and list for one snapshot per chat
// and the whole list. Callers hold c.mu.
func (c *conn) overflow() {
	c.q = slices.DeleteFunc(c.q, out.dropped)
	c.held, c.bytes = 0, 0
	for id, s := range c.subs {
		s.pending = true
		c.push(out{base: id})
	}
	if c.list {
		c.listFull = true
		c.push(out{list: true})
	}
}

func (c *conn) Removed(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subs[id] == nil {
		return
	}
	delete(c.subs, id)
	data, _ := json.Marshal(map[string]string{"type": "removed", "sessionId": id})
	c.push(out{frame: data})
}

func (c *conn) ListChanged() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.list && !slices.ContainsFunc(c.q, func(o out) bool { return o.list }) {
		c.push(out{list: true})
	}
}

// command runs a streaming command; false for any other.
func (s *Server) command(c *conn, typ, id string, msg []byte) bool {
	var m struct {
		SessionID string `json:"sessionId"`
		After     int64  `json:"after"`
	}
	switch typ {
	case "subscribe", "unsubscribe":
		if json.Unmarshal(msg, &m) != nil || m.SessionID == "" || m.After < 0 {
			c.reply(result(id, "invalid", "want a sessionId and an after of 0 or more"))
			return true
		}
	case "subscribeSessions", "unsubscribeSessions":
	default:
		return false
	}
	switch typ {
	case "subscribe":
		if err := s.Chats.Subscribe(m.SessionID, c); err != nil {
			c.reply(result(id, code(err), err.Error()))
			return true
		}
		c.mu.Lock()
		su := c.subs[m.SessionID]
		if su == nil {
			su = &sub{}
			c.subs[m.SessionID] = su
		}
		su.pending = true
		c.q = slices.DeleteFunc(c.q, func(o out) bool { return o.base == m.SessionID })
		c.push(out{frame: mustJSON(result(id, "", ""))})
		c.push(out{base: m.SessionID, after: m.After})
		c.mu.Unlock()
		return true
	case "unsubscribe":
		s.Chats.Unsubscribe(m.SessionID, c)
		c.mu.Lock()
		delete(c.subs, m.SessionID)
		c.mu.Unlock()
	case "subscribeSessions":
		s.Chats.WatchList(c, true)
		c.mu.Lock()
		c.list, c.listFull = true, true
		c.push(out{frame: mustJSON(result(id, "", ""))})
		c.push(out{list: true})
		c.mu.Unlock()
		return true
	case "unsubscribeSessions":
		s.Chats.WatchList(c, false)
		c.mu.Lock()
		c.list = false
		c.mu.Unlock()
	}
	c.reply(result(id, "", ""))
	return true
}

// unsubscribeAll ends c's subscriptions when it closes.
func (s *Server) unsubscribeAll(c *conn) {
	c.mu.Lock()
	ids := slices.Collect(maps.Keys(c.subs))
	c.mu.Unlock()
	for _, id := range ids {
		s.Chats.Unsubscribe(id, c)
	}
	s.Chats.WatchList(c, false)
}

// writer writes c's queue until done closes or a write fails.
func (s *Server) writer(c *conn, done <-chan struct{}) error {
	var sent map[string]model.Chat // the list as the client has it
	for {
		c.mu.Lock()
		for len(c.q) == 0 {
			c.mu.Unlock()
			select {
			case <-c.wake:
			case <-done:
				return nil
			}
			c.mu.Lock()
		}
		o := c.q[0]
		c.q = c.q[1:]
		if o.batch.Chat != "" {
			c.held--
			c.bytes -= o.size
		}
		var frame []byte
		switch {
		case o.frame != nil:
			frame = o.frame
		case o.batch.Chat != "":
			frame = c.batch(o.batch)
		case o.base != "":
			c.mu.Unlock()
			frame = s.base(c, o.base, o.after)
			c.mu.Lock()
		case o.list:
			full, on := c.listFull, c.list
			c.listFull = false
			if !on {
				break
			}
			c.mu.Unlock()
			frame = s.list(&sent, full)
			c.mu.Lock()
		}
		c.mu.Unlock()
		if frame == nil {
			continue
		}
		if err := c.write(opText, frame); err != nil {
			return err
		}
	}
}

// batch is the frame for pushed deltas, less those the client has, or nil.
// A gap (it never happens) gets a base. Callers hold c.mu.
func (c *conn) batch(d proto.ChatDeltas) []byte {
	su := c.subs[d.Chat]
	if su == nil || su.pending {
		return nil
	}
	skip := su.sent - d.Seq
	if skip >= int64(len(d.Deltas)) {
		return nil
	}
	if skip < 0 {
		su.pending = true
		c.push(out{base: d.Chat, after: su.sent})
		return nil
	}
	su.sent = d.Seq + int64(len(d.Deltas))
	return deltasFrame(d.Chat, nil, 0, d.Deltas[skip:])
}

// base reads chat id after `after` for a subscription, and is the frame
// for it, or nil when there is nothing to send.
func (s *Server) base(c *conn, id string, after int64) []byte {
	c.mu.Lock()
	su := c.subs[id]
	if su != nil {
		su.pending = false
	}
	c.mu.Unlock()
	if su == nil {
		return nil
	}
	d, err := s.Chats.Read(id, after)
	if err != nil {
		return nil // the chat is gone: Removed says so
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subs[id] != su || su.pending { // unsubscribed, or another base is on its way
		return nil
	}
	su.sent = d.Seq + int64(len(d.Deltas))
	if d.Snapshot == nil && len(d.Deltas) == 0 {
		return nil
	}
	return deltasFrame(id, d.Snapshot, d.Seq, d.Deltas)
}

// list is the frame that brings the client's list, *sent, up to date: the
// whole list when full or the client has none, else what changed, or nil.
// A change of Seq alone is not one: it moves with every delta.
func (s *Server) list(sent *map[string]model.Chat, full bool) []byte {
	chats := s.Chats.List()
	old := *sent
	*sent = make(map[string]model.Chat, len(chats))
	for _, ch := range chats {
		(*sent)[ch.ID] = ch
	}
	if full || old == nil {
		return mustJSON(map[string]any{"type": "sessions", "chats": nonNil(chats)})
	}
	changed, removed := []model.Chat{}, []string{}
	for _, ch := range chats {
		was, ok := old[ch.ID]
		was.Seq = ch.Seq
		if !ok || was != ch {
			changed = append(changed, ch)
		}
	}
	for id := range old {
		if _, ok := (*sent)[id]; !ok {
			removed = append(removed, id)
		}
	}
	if len(changed) == 0 && len(removed) == 0 {
		*sent = old // keep the Seq the client has, so a later change still shows
		return nil
	}
	slices.Sort(removed)
	return mustJSON(map[string]any{"type": "sessionsChanged", "changed": changed, "removed": removed})
}

func deltasFrame(id string, snapshot []byte, seq int64, deltas [][]byte) []byte {
	f := map[string]any{"type": "deltas", "sessionId": id, "deltas": []json.RawMessage{}}
	if snapshot != nil {
		f["seq"], f["snapshot"] = seq, json.RawMessage(snapshot)
	}
	for _, d := range deltas {
		f["deltas"] = append(f["deltas"].([]json.RawMessage), d)
	}
	return mustJSON(f)
}

func result(id, code, message string) map[string]any {
	r := map[string]any{"type": "result", "id": id, "ok": code == ""}
	if code != "" {
		r["code"], r["message"] = code, message
	}
	return r
}

// code is err's result code: the daemon's errors carry one.
func code(err error) string {
	var c interface{ Code() string }
	if errors.As(err, &c) {
		return c.Code()
	}
	return "provider"
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
