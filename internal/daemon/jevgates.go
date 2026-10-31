package daemon

// aide's Jev gates on the chats aide starts (ENG-2934, decide/gates.go).
// A turn starts only from Jev's preparation of its prompt, which checks
// aide's consent list and key; a queued message is checked when queued;
// and with aide's tool gate on, Jev answers a tool approval it is sure of
// before the approval reaches the snapshot. Other chats get none of this.

import (
	"cmp"
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/session"
)

// aideClient is the hello name of aide's web client: the chats it starts
// get the gates, and only it hands over its Jev settings.
const aideClient = "aide"

// jevRefused is a gate's refusal as a command's failure.
func jevRefused(err error) error { return chatError{"jev", err.Error()} }

// jevPrepare is m with the text c's provider gets: Jev's preparation of it
// in a chat aide started, m itself in any other.
func (d *Daemon) jevPrepare(ctx context.Context, c *chat, m session.Message) (session.Message, error) {
	if !c.cfg.Aide {
		return m, nil
	}
	// shortcut: preparation runs on the chat's loop for up to about 5 s (two 1.5 s workspace reads and Jev's 2 s), so an interrupt waits for it; move it off the loop if that bites.
	text, err := d.o.Gates.Prepare(ctx, c.cfg.Cwd, m.Text, jevHistory(c.snap))
	if err != nil {
		return m, jevRefused(err)
	}
	m.Text = text
	return m, nil
}

// jevQueue checks consent and the key for a message an aide chat queues,
// as aide does when it queues one.
func (d *Daemon) jevQueue(c *chat) error {
	if !c.cfg.Aide {
		return nil
	}
	if err := d.o.Gates.Require(c.cfg.Cwd); err != nil {
		return jevRefused(err)
	}
	return nil
}

// jevHistory is aide's getSafeHistory: the prompts and replies so far, the
// latest 24,000 characters of them.
func jevHistory(s session.Snapshot) []decide.HistoryItem {
	var out []decide.HistoryItem
	for _, it := range s.Content {
		switch it.Kind {
		case session.KindUserPrompt:
			out = append(out, decide.HistoryItem{Role: "user", Text: it.Text})
		case session.KindAssistantText:
			var p *string
			if it.Provider != nil {
				s := string(*it.Provider)
				p = &s
			}
			out = append(out, decide.HistoryItem{Provider: p, Role: "assistant", Text: it.Text})
		}
	}
	left := 24_000
	for i := len(out) - 1; i >= 0; i-- {
		t := out[i].Text
		if n := utf8.RuneCountInString(t); n > left {
			t = string([]rune(t)[n-left:])
		}
		left -= utf8.RuneCountInString(t)
		out[i].Text = t
	}
	keep := out[:0]
	for _, h := range out {
		if h.Text != "" {
			keep = append(keep, h)
		}
	}
	return keep
}

// jevRecent is aide's recentConversation: the last four prompts and
// replies, 1,500 characters of each.
func jevRecent(s session.Snapshot) string {
	var lines []string
	for _, it := range s.Content {
		if it.Kind == session.KindUserPrompt || it.Kind == session.KindAssistantText {
			t := []rune(it.Text)
			lines = append(lines, fmt.Sprintf("%s: %s", it.Kind, string(t[:min(len(t), 1500)])))
		}
	}
	out := ""
	for i, l := range lines[max(0, len(lines)-4):] {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

// jevHold holds e, a tool approval of an aide chat with the tool gate on,
// out of the snapshot while Jev judges it, as aide does. Jev's allow or
// deny goes to the provider with a line in the transcript; a prompt, or no
// answer, lets the approval through to the user. False for any other
// event, which the caller applies.
func (d *Daemon) jevHold(c *chat, e session.Event) bool {
	if e.Type == session.EventRequestResolved && c.jevHeld[e.RequestID] {
		delete(c.jevHeld, e.RequestID) // withdrawn while Jev judged it
		return true
	}
	if !c.cfg.Aide || e.Type != session.EventRequestOpened || e.RequestType == session.RequestPlan {
		return false
	}
	s := d.o.Gates.ToolGate(c.cfg.Cwd)
	if !s.Enabled {
		return false
	}
	if c.jevHeld == nil {
		c.jevHeld = map[string]bool{}
	}
	c.jevHeld[e.RequestID] = true
	start := c.starts
	kind := string(cmp.Or(e.RequestType, session.RequestUnknown))
	r := decide.ToolRequest{Cwd: c.cfg.Cwd, Provider: string(c.cfg.Provider), RequestType: kind, Detail: e.Detail, Context: jevRecent(c.snap)}
	go func() {
		j := d.o.Gates.JudgeTool(context.Background(), r, s)
		c.do(func() {
			if !c.jevHeld[e.RequestID] || c.starts != start || c.drv == nil {
				return // withdrawn, or its provider is gone
			}
			delete(c.jevHeld, e.RequestID)
			defer d.chatChanged(c)
			if j.Decision == decide.GatePrompt {
				d.chatApply(c, e)
				return
			}
			verb := "allowed"
			if j.Decision == decide.GateDeny {
				verb = "denied"
			}
			c.system(fmt.Sprintf("Jev %s: %s. %s", verb, kind, j.Reason))
			if err := c.drv.Approve(context.Background(), e.RequestID, session.Decision{Allow: j.Decision == decide.GateAllow}); err != nil {
				c.system("Jev's answer did not reach " + string(c.cfg.Provider) + ": " + err.Error())
				d.chatApply(c, e) // the user answers instead
			}
		})
	}()
	return true
}
