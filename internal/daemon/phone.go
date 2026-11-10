package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/session"
)

// The phone page's chat: every tab in the sidebar's order, each agent's
// transcript, and messages and slash commands typed as the window types them.

// phoneTabs is every tab the phone lists, with each chat's pending requests
// read from its loop once d.mu is free.
func (d *Daemon) phoneTabs() []remote.Tab {
	d.mu.Lock()
	queued := make(map[string]int, len(d.mail.queue))
	for pane, q := range d.mail.queue {
		queued[pane] = len(q)
	}
	tabs := remote.Tabs(d.st, queued)
	chats := make([]*chat, len(tabs))
	for i, t := range tabs {
		if t.Chat {
			chats[i] = d.chats[t.Pane]
		}
	}
	d.mu.Unlock()
	for i, c := range chats {
		if c != nil {
			c.run(func() { tabs[i].Requests = remote.Requests(c.snap, logs.Redact) })
		}
	}
	return tabs
}

// phoneChanged is closed at the next change.
func (d *Daemon) phoneChanged() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.phoneTick == nil {
		d.phoneTick = make(chan struct{})
	}
	return d.phoneTick
}

// phoneChatEntries is chat's transcript as the phone shows it.
func (d *Daemon) phoneChatEntries(id string) ([]flow.Entry, string, bool) {
	d.mu.Lock()
	c := d.chats[id]
	d.mu.Unlock()
	if c == nil {
		return nil, "", false
	}
	var es []flow.Entry
	var mdl string
	ok := c.run(func() {
		mdl = c.cfg.Model
		for i, it := range c.snap.Content {
			e := flow.Entry{Seq: int64(i + 1), Text: it.Text}
			switch it.Kind {
			case session.KindUserPrompt:
				e.Kind = flow.EntryUser
			case session.KindAssistantText:
				e.Kind = flow.EntryAssistant
			case session.KindToolCall:
				if it.Tool == nil {
					continue
				}
				t := it.Tool
				e.Kind, e.Text, e.Tool, e.Arg = flow.EntryTool, "", t.ToolName, flow.ToolArg(*t)
				e.Out, e.Done, e.Failed = cutOut(t.Output), t.Status != "running", t.Status == "failed"
			default:
				continue
			}
			if it.Model != nil && *it.Model != "" {
				mdl = *it.Model
			}
			es = append(es, e)
		}
	})
	return es, mdl, ok
}

func cutOut(s string) string {
	if r := []rune(s); len(r) > 4000 {
		return string(r[:4000]) + "…"
	}
	return s
}

// phoneSend queues text for pane's agent as the window's send does: typed
// once the agent sits at its input box with no form on screen and no draft
// of the user's in it. A chat tab gets it as a turn, or queued behind one.
// A shell never gets it.
func (d *Daemon) phoneSend(pane, text string) error {
	d.mu.Lock()
	c := d.chats[pane]
	prov := d.paneProvider(pane)
	d.mu.Unlock()
	if c != nil {
		if remote.Clean(text) == "" {
			return errors.New("the message is empty")
		}
		return chatRefusal(d.chatRequest(context.Background(), proto.ChatSend{Chat: pane, Text: text}))
	}
	if !model.IsAgent(prov) || prov == model.ProviderTerminal {
		return errors.New("that tab runs no agent; type into a shell in its tab")
	}
	if d.paneExited(pane) {
		return errors.New("the agent in that tab has exited")
	}
	return d.windowDeliver(proto.Deliver{Pane: pane, Text: text})
}

// paneProvider is pane's agent, from its pane or its activity. Callers
// hold d.mu.
func (d *Daemon) paneProvider(pane string) model.Provider {
	if i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == pane }); i >= 0 && d.st.Panes[i].Provider != "" {
		return d.st.Panes[i].Provider
	}
	if i := d.activityIndex(pane); i >= 0 {
		return d.st.Activities[i].Provider
	}
	return ""
}

// paneExited reports whether pane's command has ended.
func (d *Daemon) paneExited(pane string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == pane })
	return i >= 0 && d.st.Panes[i].Exited
}

// phoneCommand types command, a slash command, into Claude Code's input box
// as keys, not a paste, so Claude Code runs it, and presses Enter. Only
// while the agent waits there: not during a turn, a question or a form, and
// not into the user's draft.
func (d *Daemon) phoneCommand(pane, command string) error {
	d.mu.Lock()
	prov := d.paneProvider(pane)
	d.mu.Unlock()
	if prov != model.ProviderClaude {
		return errors.New("only Claude Code takes the model and effort from here")
	}
	p, busy, err := d.ready(pane)
	if err != nil {
		if busy {
			return fmt.Errorf("%w; try again when it waits at its input box", err)
		}
		return err
	}
	if agent.FormUp(p.Snapshot(), "") {
		return errors.New("that pane shows a form; answer it in its tab")
	}
	d.noteUserKey(pane, nil)
	if err := d.handle(context.Background(), proto.Input{Pane: pane, Data: input.Text(remote.Clean(command))}); err != nil {
		return err
	}
	if err := d.pressEnter(pane, p, command); err != nil || !strings.HasPrefix(command, "/model ") {
		return err
	}
	// why: in a conversation Claude Code asks to confirm the switch, which the phone's pick already did.
	for deadline := time.Now().Add(switchWait.get()); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if agent.SwitchModelAsked(p.Snapshot()) {
			return d.handle(context.Background(), proto.Input{Pane: pane, Data: press("1", p.Modes())})
		}
	}
	return nil
}

// switchWait is how long after /model the confirm dialog may take to show.
var switchWait = newTiming(1500 * time.Millisecond)
