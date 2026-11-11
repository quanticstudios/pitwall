package phone

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// Chat is the open tab's entries, oldest first, and the messages sent
// from here that its transcript does not show yet.
type Chat struct {
	Pane    string
	Entries []flow.Entry
	More    bool   // older entries are on the server
	Model   string // the model the latest turn used
	None    bool   // the tab has no transcript
	Gone    bool   // the tab closed
	Pending []*Pending
}

// Pending is a message sent from here: Sending, then Queued or Sent, until
// the transcript shows it; or Not sent, with Err.
type Pending struct {
	Text  string
	Image bool
	// After is the newest entry's seq when the message went out: only a
	// later entry can be it, not an old prompt of the same words.
	After  int64
	ID     uint64
	Sent   bool // the server took it
	Queued bool // it waits for the agent's turn to end
	Err    string
	At     time.Time
}

// Label is what the message's line says.
func (p *Pending) Label() string {
	switch {
	case p.Err != "":
		return "Not sent: " + p.Err
	case !p.Sent:
		return "Sending"
	case p.Queued:
		return "Queued"
	}
	return "Sent"
}

// Reset starts the chat over with a "reset" event.
func (c *Chat) Reset(e Entries) {
	c.Entries, c.More, c.None, c.Gone = nil, e.More, e.None, e.Gone
	if e.Model != "" {
		c.Model = e.Model
	}
	c.Add(e.Entries)
}

// Add puts new entries in and replaces the ones it has by seq, such as a
// tool call that finished, and reports whether any was new.
func (c *Chat) Add(es []flow.Entry) (fresh bool) {
	for _, e := range es {
		i, found := slices.BinarySearchFunc(c.Entries, e.Seq, func(x flow.Entry, s int64) int { return cmpSeq(x.Seq, s) })
		if found {
			c.Entries[i] = e
		} else {
			c.Entries = slices.Insert(c.Entries, i, e)
			fresh = true
		}
		if e.Kind == flow.EntryUser {
			c.settle(e)
		}
	}
	return fresh
}

func cmpSeq(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Older puts in a page of earlier entries and returns how many were new.
func (c *Chat) Older(es []flow.Entry, more bool) int {
	n := len(c.Entries)
	for _, e := range es {
		if _, found := slices.BinarySearchFunc(c.Entries, e.Seq, func(x flow.Entry, s int64) int { return cmpSeq(x.Seq, s) }); !found {
			c.Add([]flow.Entry{e})
		}
	}
	c.More = more
	return len(c.Entries) - n
}

// First is the oldest entry's seq, 0 for none.
func (c *Chat) First() int64 {
	if len(c.Entries) == 0 {
		return 0
	}
	return c.Entries[0].Seq
}

func norm(s string) string { return strings.Join(strings.Fields(s), " ") }

// settle drops the message, sent or on its way, that e shows: the
// transcript can show a prompt before the send's answer comes.
func (c *Chat) settle(e flow.Entry) {
	if i := slices.IndexFunc(c.Pending, func(p *Pending) bool {
		return p.Err == "" && e.Seq > p.After && pendingText(p.Text, p.Image) == pendingText(e.Text, p.Image)
	}); i >= 0 {
		c.Pending = slices.Delete(c.Pending, i, i+1)
	}
}

// Last is the newest entry's seq, 0 for none.
func (c *Chat) Last() int64 {
	if len(c.Entries) == 0 {
		return 0
	}
	return c.Entries[len(c.Entries)-1].Seq
}

// Send adds a message on its way, before its request goes out, so an
// entry that comes before the answer settles it.
func (c *Chat) Send(text string) *Pending {
	p := &Pending{Text: text, After: c.Last()}
	c.Pending = append(c.Pending, p)
	return p
}

// Has reports whether p still waits for the transcript.
func (c *Chat) Has(p *Pending) bool { return slices.Contains(c.Pending, p) }

// Drop forgets p.
func (c *Chat) Drop(p *Pending) {
	c.Pending = slices.DeleteFunc(c.Pending, func(q *Pending) bool { return q == p })
}

// Busy reports whether t's agent is in a turn, so a message waits.
func Busy(t remote.Tab) bool {
	return t.Queued > 0 || !slices.Contains([]string{"", "completed", "error", "pr-open", "awaiting-input"}, t.State)
}

// Sent marks p as taken by the server with id, queued when t is busy.
func (c *Chat) Sent(p *Pending, id uint64, t remote.Tab, now time.Time) {
	p.ID, p.Sent, p.Queued, p.At = id, true, Busy(t), now
}

// Update moves the pending messages along with t, the tab as the list
// last showed it: a message the agent left before it went in is not sent,
// and a queued one is sent once nothing waits.
func (c *Chat) Update(t remote.Tab, now time.Time) {
	c.Pending = slices.DeleteFunc(c.Pending, func(p *Pending) bool {
		if !p.Sent || p.Err != "" {
			return false
		}
		if slices.Contains(t.Dropped, p.ID) {
			p.Err = "the agent exited"
			return false
		}
		if p.Queued && t.Queued == 0 {
			p.Queued = false
		}
		// why: a tab without a transcript never shows the message; let it go after a while.
		return !p.Queued && len(c.Entries) == 0 && now.Sub(p.At) > 8*time.Second
	})
}

// Working is the line under a running chat: "Working", with the call the
// tab names or else the last call still running.
func (c *Chat) Working(t remote.Tab) (string, bool) {
	if t.State != "working" && t.State != "connecting" {
		return "", false
	}
	what := strings.TrimSpace(t.Detail)
	if what == "" {
		for i := len(c.Entries) - 1; i >= 0; i-- {
			if e := c.Entries[i]; e.Kind == flow.EntryTool {
				if !e.Done {
					what = strings.TrimSpace(e.Tool + " " + e.Arg)
				}
				break
			}
		}
	}
	if what == "" {
		return "Working", true
	}
	return "Working: " + what, true
}

// ShortModel is a model's name without its "claude-" prefix.
func ShortModel(m string) string { return strings.TrimPrefix(m, "claude-") }

var imageMarker = regexp.MustCompile(`\[Image #\d+\]`)

func pendingText(s string, image bool) string {
	if image {
		s = imageMarker.ReplaceAllString(s, "")
	}
	return norm(s)
}

// Message is the draft's text, with a label for a picture sent alone.
func (p *Pending) Message() string {
	if strings.TrimSpace(p.Text) == "" && p.Image {
		return "Picture"
	}
	return p.Text
}
