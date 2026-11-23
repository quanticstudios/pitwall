package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/web"
)

// A pane chat with subscribers, the window, the phone page or a web
// protocol client, has one goroutine that refreshes it when something
// happens and pushes what changed to each of them: the pane's hooks, its
// OSC 7501 reports, a change of the daemon's state (queue edits among
// them), its screen, a file changing in its transcript's folders, and a
// slow tick for what none of those tell.

// paneStream is one pane chat's subscribers and its refresh goroutine.
type paneStream struct {
	id   string
	wake chan struct{} // capacity 1: wakes fold into the next refresh
	stop chan struct{} // closed by whoever takes the stream out of Daemon.paneStreams
	// mu orders the publishes: each refresh and a window's subscribe hold
	// it, so the deltas reach every sink in seq order, after its base.
	mu        sync.Mutex
	pc        *paneChat // the pane chat last published
	published int64     // the seq the subscribers were last sent up to
	smu       sync.Mutex
	sinks     map[web.Sink]sinkOpts // guarded by smu
}

// sinkOpts is how a stream treats one subscriber: a remote one's text goes
// through logs.Redact, and a pending one waits for its base.
type sinkOpts struct{ redact, pending bool }

// The refresh pace: never twice within streamGap, so a screen that
// redraws at 60 frames a second refreshes the chat at most every 80 ms,
// and at least every streamTick, or streamPoll where no folder watch
// tells of the transcript's writes.
const streamGap = 80 * time.Millisecond

var (
	streamTick = newTiming(2 * time.Second)
	streamPoll = newTiming(500 * time.Millisecond)
)

// streamRefreshes counts the refreshes of every pane stream, for tests.
var streamRefreshes atomic.Int64

// maxFolders caps the folders one pane chat watches: its transcript's,
// and those of the subagents still running.
const maxFolders = 32

// paneStreamOf is pane id's stream with sink added, made on first use;
// false when the pane runs no pane chat. Callers hold d.mu.
func (d *Daemon) paneStreamOf(id string, sink web.Sink, o sinkOpts) (*paneStream, bool) {
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if i < 0 || d.chats[id] != nil || !model.PaneChat(d.st.Panes[i]) || d.panes[id] == nil {
		return nil, false
	}
	s := d.paneStreams[id]
	if s == nil {
		s = &paneStream{id: id, wake: make(chan struct{}, 1), stop: make(chan struct{}), sinks: map[web.Sink]sinkOpts{}}
		if d.paneStreams == nil {
			d.paneStreams = map[string]*paneStream{}
		}
		d.paneStreams[id] = s
		go d.streamLoop(s)
		s.poke() // the first refresh goes at once
	}
	s.smu.Lock()
	s.sinks[sink] = o
	s.smu.Unlock()
	return s, true
}

// paneSubscribe adds sink to pane chat id's subscribers; redact runs its
// text through logs.Redact. The sink reads its own base (web.Sink).
func (d *Daemon) paneSubscribe(id string, sink web.Sink, redact bool) error {
	d.mu.Lock()
	s, ok := d.paneStreamOf(id, sink, sinkOpts{redact: redact})
	d.mu.Unlock()
	if !ok {
		return chatError{"not-found", "no chat " + id}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d.started(s, -1)
	return nil
}

// started has s publish from seq, or from the pane chat's seq now when
// seq is negative, unless it already publishes: a subscriber's base is at
// that seq or after. Callers hold s.mu.
func (d *Daemon) started(s *paneStream, seq int64) {
	if s.pc != nil {
		return
	}
	d.mu.Lock()
	pc := d.paneChats[s.id]
	d.mu.Unlock()
	if pc == nil {
		return // made by the first refresh, which starts it
	}
	pc.mu.Lock()
	s.pc, s.published = pc, pc.seq
	pc.mu.Unlock()
	if seq >= 0 {
		s.published = seq
	}
}

// paneUnsubscribe drops sink from pane chat id's subscribers, and the
// stream with its folder watch once the last one leaves.
func (d *Daemon) paneUnsubscribe(id string, sink web.Sink) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.paneStreams[id]
	if s == nil {
		return
	}
	s.smu.Lock()
	delete(s.sinks, sink)
	empty := len(s.sinks) == 0
	s.smu.Unlock()
	if empty {
		delete(d.paneStreams, id)
		close(s.stop)
	}
}

// paneStreaming reports whether pane id's chat has subscribers, whose
// stream keeps it current. Callers hold d.mu.
func (d *Daemon) paneStreaming(id string) bool {
	return d.paneStreams[id] != nil
}

// pokePane wakes pane id's stream, if it has one. Callers hold d.mu.
func (d *Daemon) pokePane(id string) {
	if s := d.paneStreams[id]; s != nil {
		s.poke()
	}
}

// pokePanes wakes every pane stream. Callers hold d.mu.
func (d *Daemon) pokePanes() {
	for _, s := range d.paneStreams {
		s.poke()
	}
}

func (s *paneStream) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// streamLoop refreshes s at each wake, no more often than streamGap,
// until s stops.
func (d *Daemon) streamLoop(s *paneStream) {
	w := newFolderWatch(s.poke)
	every := streamPoll.get()
	if w != nil {
		every = streamTick.get()
		defer w.Close()
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	gap := time.NewTimer(0)
	defer gap.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
		case <-tick.C:
		}
		select {
		case <-s.stop:
			return
		case <-gap.C:
		}
		dirs, ok := d.paneRefresh(s)
		if !ok {
			return
		}
		if w != nil {
			w.Set(dirs)
		}
		gap.Reset(streamGap)
	}
}

// paneRefresh catches s's pane chat up and pushes what it gained to the
// subscribers, outside the chat's lock, and returns the folders to watch;
// false once the pane is gone, which ends the stream.
func (d *Daemon) paneRefresh(s *paneStream) ([]string, bool) {
	streamRefreshes.Add(1)
	d.mu.Lock()
	if d.panes[s.id] == nil {
		if d.paneStreams[s.id] == s {
			delete(d.paneStreams, s.id)
			close(s.stop)
		}
		d.mu.Unlock()
		s.smu.Lock()
		var sinks []web.Sink
		for k := range s.sinks {
			sinks = append(sinks, k)
		}
		s.smu.Unlock()
		for _, k := range sinks {
			k.Removed(s.id)
		}
		return nil, false
	}
	d.mu.Unlock()
	pc, l, ok := d.paneChatOf(s.id)
	if !ok {
		return nil, true // the agent left the pane; its subscribers stay for the next one
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pc.mu.Lock()
	pc.catchUp(l)
	out := s.collect(pc)
	dirs := pc.folders()
	pc.mu.Unlock()
	if out != nil {
		s.fanOut(*out)
	}
	return dirs, true
}

// collect is what s's subscribers lack of pc: the deltas since the last
// publish, or none with a later Seq, which has them read a snapshot again
// (a new session in the pane, or deltas no longer kept); nil for nothing.
// Callers hold s.mu and pc.mu.
func (s *paneStream) collect(pc *paneChat) *proto.ChatDeltas {
	if s.pc != pc {
		was := s.pc
		s.pc, s.published = pc, pc.seq
		if was == nil {
			return nil
		}
		return &proto.ChatDeltas{Chat: s.id, Seq: pc.seq}
	}
	if pc.seq == s.published {
		return nil
	}
	from := s.published
	s.published = pc.seq
	kept := pc.recent[max(0, len(pc.recent)-recentDeltas):]
	first := pc.seq - int64(len(kept))
	if from >= first {
		if ds, ok := deltaJSON(kept[from-first:]); ok {
			return &proto.ChatDeltas{Chat: s.id, Seq: from, Deltas: ds}
		}
	}
	return &proto.ChatDeltas{Chat: s.id, Seq: pc.seq}
}

// fanOut hands out to every subscriber with its base, redacted for the
// remote ones. Callers hold s.mu.
func (s *paneStream) fanOut(out proto.ChatDeltas) {
	s.smu.Lock()
	sinks := make(map[web.Sink]sinkOpts, len(s.sinks))
	for k, o := range s.sinks {
		if !o.pending {
			sinks[k] = o
		}
	}
	s.smu.Unlock()
	var clean *proto.ChatDeltas
	for k, o := range sinks {
		if !o.redact {
			k.Deltas(out)
			continue
		}
		if clean == nil {
			c := redactDeltas(out)
			clean = &c
		}
		k.Deltas(*clean)
	}
}

// folders are the folders that hold pc's transcript and those of its
// subagents still running, at most maxFolders: a folder, not a file, so
// the transcript's late creation, a rename and a new session's file next
// to it wake the stream too. Callers hold pc.mu.
func (pc *paneChat) folders() []string {
	if pc.path == "" {
		return nil
	}
	dirs := []string{filepath.Dir(pc.path)}
	if pc.prov == model.ProviderClaude {
		dirs = append(dirs, filepath.Join(strings.TrimSuffix(pc.path, ".jsonl"), "subagents"))
	}
	for _, a := range pc.agents {
		if !a.done {
			dirs = append(dirs, filepath.Dir(a.path))
		}
	}
	slices.Sort(dirs)
	dirs = slices.Compact(dirs)
	return dirs[:min(len(dirs), maxFolders)]
}

// guiSink is a window's subscription: it pushes each chat's deltas as
// ChatDeltas with Push. A window drops a chat it stops drawing itself.
type guiSink struct{ c *client }

func (g guiSink) Deltas(m proto.ChatDeltas) {
	m.Push = true
	g.c.queue(m)
}
func (guiSink) Removed(string) {}
func (guiSink) ListChanged()   {}

// chatSubscribe runs a window's ChatSubscribe: it queues the chat's base,
// as ChatRead After would answer, and adds the window to the chat's
// subscribers, both before any push, so the pushes after it apply on it.
func (d *Daemon) chatSubscribe(ctx context.Context, c *client, m proto.ChatSubscribe) {
	sink := guiSink{c}
	if m.Off {
		d.chatUnsubscribe(m.Chat, sink)
		return
	}
	read := proto.ChatRead{Chat: m.Chat, After: m.After}
	if ch, err := d.chat(m.Chat); err == nil {
		var rerr error
		if !ch.do(func() {
			var out proto.ChatDeltas
			if out, rerr = ch.read(read); rerr != nil {
				return
			}
			c.queue(out) // the read published what the subscribers lacked; the window's pushes start after it
			if ch.subs == nil {
				ch.subs = map[web.Sink]bool{}
			}
			ch.subs[sink] = true
		}) {
			rerr = chatError{"not-found", "chat " + m.Chat + " is closed"}
		}
		if rerr != nil {
			c.queue(chatResult(m.Chat, "", "", rerr))
		}
		return
	}
	d.mu.Lock()
	s, ok := d.paneStreamOf(m.Chat, sink, sinkOpts{pending: true})
	d.mu.Unlock()
	if !ok {
		c.queue(chatResult(m.Chat, "", "", chatError{"not-found", "no chat " + m.Chat}))
		return
	}
	s.mu.Lock()
	reply, _ := d.paneChatRequest(ctx, read)
	base, ok := reply.(proto.ChatDeltas)
	if !ok {
		s.mu.Unlock()
		d.paneUnsubscribe(m.Chat, sink)
		if reply == nil {
			reply = chatResult(m.Chat, "", "", chatError{"not-found", "no chat " + m.Chat})
		}
		c.queue(reply)
		return
	}
	c.queue(base)
	d.started(s, base.Seq+int64(len(base.Deltas)))
	s.smu.Lock()
	if _, ok := s.sinks[sink]; ok {
		s.sinks[sink] = sinkOpts{}
	}
	s.smu.Unlock()
	s.mu.Unlock()
}

// chatUnsubscribe drops sink from chat id's subscribers, a chat tab's or a
// pane chat's.
func (d *Daemon) chatUnsubscribe(id string, sink web.Sink) {
	if ch, err := d.chat(id); err == nil {
		ch.do(func() { delete(ch.subs, sink) })
		return
	}
	d.paneUnsubscribe(id, sink)
}

// redactDeltas is d with every string in its snapshot and deltas passed
// through logs.Redact, for a phone or browser.
func redactDeltas(d proto.ChatDeltas) proto.ChatDeltas {
	if d.Snapshot != nil {
		d.Snapshot = redactJSON(d.Snapshot)
	}
	if d.Item != nil {
		d.Item = redactJSON(d.Item)
	}
	if len(d.Deltas) > 0 {
		ds := make([][]byte, len(d.Deltas))
		for i, b := range d.Deltas {
			ds[i] = redactJSON(b)
		}
		d.Deltas = ds
	}
	return d
}

// redactJSON is b with logs.Redact run on every string value; b itself
// when it is not JSON.
func redactJSON(b []byte) []byte {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // seqs and revisions keep every digit
	var v any
	if dec.Decode(&v) != nil {
		return b
	}
	var walk func(any) any
	walk = func(v any) any {
		switch v := v.(type) {
		case string:
			return logs.Redact(v)
		case []any:
			for i := range v {
				v[i] = walk(v[i])
			}
		case map[string]any:
			for k := range v {
				v[k] = walk(v[k])
			}
		}
		return v
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if enc.Encode(walk(v)) != nil {
		return b
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n"))
}

// dropSubs ends client c's subscriptions, the window's when it goes.
func (d *Daemon) dropSubs(c *client) {
	sink := guiSink{c}
	d.mu.Lock()
	var panes []string
	for id := range d.paneStreams {
		panes = append(panes, id)
	}
	var chats []*chat
	for _, ch := range d.chats {
		chats = append(chats, ch)
	}
	d.mu.Unlock()
	for _, id := range panes {
		d.paneUnsubscribe(id, sink)
	}
	for _, ch := range chats {
		ch.do(func() { delete(ch.subs, sink) })
	}
}
