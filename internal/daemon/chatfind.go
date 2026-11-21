package daemon

import (
	"fmt"
	"strings"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// maxFindHits is the most matches a ChatFindResult carries: the newest
// items', so a one-letter query in a long chat stays a small reply.
const maxFindHits = 5000

// chatFind answers m with its query's matches in all of chat m.Chat: a
// chat tab's every item, a pane chat's transcript back to its start.
func (d *Daemon) chatFind(m proto.ChatFind) proto.ChatFindResult {
	out := proto.ChatFindResult{Chat: m.Chat, Query: m.Query}
	q := session.Fold(strings.TrimSpace(m.Query))
	if len(q) == 0 {
		return out
	}
	var err error
	if pc, l, ok := d.paneChatOf(m.Chat); ok {
		out.Hits, out.More, err = pc.find(l, q, maxFindHits)
	} else if c, e := d.chat(m.Chat); e != nil {
		err = e
	} else if !c.do(func() { out.Hits, out.More = session.Find(c.snap.Content, q, maxFindHits) }) {
		err = chatError{"not-found", "chat " + m.Chat + " is closed"}
	}
	if err != nil {
		out.Hits, out.More, out.Err = nil, false, err.Error()
	}
	return out
}

// find is q's matches in the whole transcript, at most about limit, the
// newest kept: the tail a snapshot holds, then each page before it, the
// pages a window reads, so each hit names an item the window gets to by
// reading pages.
//
// shortcut: each find reads the transcript before the tail again; keep
// each page's text by its offsets if finds in huge transcripts get slow.
func (pc *paneChat) find(l paneLive, q []rune, limit int) ([]session.FindHit, bool, error) {
	pc.mu.Lock()
	if err := pc.catchUp(l); err != nil && pc.off == 0 {
		pc.mu.Unlock()
		return nil, false, fmt.Errorf("Pitwall could not read the agent's transcript: %w", err)
	}
	from, older := paneWindow(pc.snap.Content, pc.at, pc.start)
	hits, more := session.Find(pc.snap.Content[from:], q, limit)
	pc.mu.Unlock()
	n := session.Matches(hits)
	for before := older; before > 0 && !more; {
		if n >= limit {
			return hits, true, nil
		}
		s, next, err := pc.page(before)
		if err != nil {
			return nil, false, err
		}
		h, m := session.Find(s.Content, q, limit-n)
		hits, more, n = append(h, hits...), m, n+session.Matches(h)
		if next >= before {
			break
		}
		before = next
	}
	return hits, more, nil
}
