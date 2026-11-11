package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
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
	panes := make([]Pane, len(tabs))
	asked := make([]string, len(tabs))
	for i, t := range tabs {
		if t.Chat {
			chats[i] = d.chats[t.Pane]
		} else if t.Provider == model.ProviderClaude || t.Provider == model.ProviderCodex {
			panes[i] = d.panes[t.Pane]
		}
		if settings, ok := d.phoneSettings[t.Pane]; ok && !t.Chat {
			tabs[i].Model, tabs[i].Effort = settings.model, settings.effort
		}
		if ai := d.activityIndex(t.Pane); ai >= 0 {
			asked[i] = d.askedOf(d.st.Activities[ai])
		}
		tabs[i].Dropped = slices.Clone(d.phoneDropped[t.Pane])
	}
	d.mu.Unlock()
	for i, p := range panes {
		if p != nil {
			g := p.Snapshot()
			if tabs[i].Answer {
				a := model.Activity{Provider: tabs[i].Provider, State: model.AgentState(tabs[i].State)}
				if err := confirmApproval(a, asked[i], g); err != nil {
					tabs[i].AllowBlocked = "Allow is unavailable because pitwall cannot confirm this permission prompt. Deny or open it on the desktop."
				}
			}
			tabs[i].Question = agent.ReadQuestion(tabs[i].Provider, g)
			if q := tabs[i].Question; q != nil {
				q.Prompt = logs.Redact(q.Prompt)
				for j := range q.Options {
					q.Options[j].Label = logs.Redact(q.Options[j].Label)
				}
			}
			if q := tabs[i].Question; q != nil {
				tabs[i].State = "awaiting-input"
				tabs[i].Label = "Input"
				tabs[i].Detail = q.Prompt
				tabs[i].Answer = false
				tabs[i].Reply = false
			}
			tabs[i].Attach = tabs[i].Send
			m, e := agent.ClaudeSettings(g)
			d.mu.Lock()
			d.keepClaudeSettings(tabs[i].Pane, p, m, e)
			d.mu.Unlock()
			if m != "" {
				tabs[i].Model = m
			}
			if e != "" {
				tabs[i].Effort = e
			}
		}
	}
	for i, c := range chats {
		if c != nil {
			c.run(func() { tabs[i].Requests = remote.Requests(c.snap, logs.Redact) })
		}
	}
	for i := range tabs {
		if tabs[i].Queued > 0 && !tabs[i].Chat {
			if _, err := d.readyWindow(tabs[i].Pane, true); err != nil {
				tabs[i].Hold = err.Error()
				if errors.Is(err, errDraft) {
					tabs[i].Hold = "you are typing in this tab on the desktop"
				}
			} else {
				d.mu.Lock()
				if d.agentAway(tabs[i].Pane) {
					tabs[i].Hold = "the agent has another command in the foreground"
				} else if d.mail.drafting[tabs[i].Pane] && time.Since(d.mail.keyAt[tabs[i].Pane]) < sendQuiet.get() {
					tabs[i].Hold = "you are typing in this tab on the desktop"
				}
				d.mu.Unlock()
			}
		}
		tabs[i].Icon = d.phoneIcons.Of(tabs[i].Dir) // why: off d.mu, since it reads the disk
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
		c.open() // why: a chat restored after a restart has no snapshot until its log is read
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

// phoneSend types text into Claude Code or Codex even during a turn, holding
// for the user's draft, forms and approvals. Other agents wait for idle.
// It returns the message's id, which a tab lists in
// Dropped if the agent leaves the pane before the message goes in. A chat
// tab gets it as a turn, or queued behind one, with id 0. A shell never
// gets it.
func (d *Daemon) phoneSend(pane, text string) (uint64, error) {
	return d.phoneSendPaths(pane, text, nil)
}

func (d *Daemon) phoneSendPaths(pane, text string, images []string) (uint64, error) {
	d.mu.Lock()
	c := d.chats[pane]
	prov := d.paneProvider(pane)
	d.mu.Unlock()
	if c != nil {
		if remote.Clean(text) == "" {
			return 0, errors.New("the message is empty")
		}
		return 0, chatRefusal(d.chatRequest(context.Background(), proto.ChatSend{Chat: pane, Text: text}))
	}
	if !model.IsAgent(prov) || prov == model.ProviderTerminal {
		return 0, errors.New("that tab runs no agent; type into a shell in its tab")
	}
	if d.paneExited(pane) {
		return 0, errors.New("the agent in that tab has exited")
	}
	d.mu.Lock()
	d.phoneSeq++
	l := letter{text: text, images: images, plain: true, window: true, phone: d.phoneSeq, owner: prov}
	d.mu.Unlock()
	return l.phone, d.userDeliver(pane, l)
}

// agentGone reports whether the agent phone letter l was for has left
// pane: the pane exited or names another agent, or none, as checkForeground
// and detect decide. A tool in the foreground is not that.
// Callers hold d.mu.
func (d *Daemon) agentGone(pane string, l letter) bool {
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == pane })
	return i < 0 || d.st.Panes[i].Exited || d.paneProvider(pane) != l.owner
}

// agentAway reports whether another process group than the one pane's
// agent hooks named holds the foreground: a tool of the agent's, or what
// was left after it exited, until checkForeground decides. Nothing goes in
// then. Callers hold d.mu.
func (d *Daemon) agentAway(pane string) bool {
	f, ok := d.panes[pane].(foregrounder)
	if !ok || d.live.fg[pane] <= 0 {
		return false
	}
	pg := f.Foreground()
	return pg > 0 && pg != d.live.fg[pane]
}

// dropOrphanPhone drops pane's phone letters whose agent left the pane.
// Callers hold d.mu.
func (d *Daemon) dropOrphanPhone(pane string) {
	for i := len(d.mail.queue[pane]) - 1; i >= 0; i-- {
		if l := d.mail.queue[pane][i]; l.phone != 0 && d.agentGone(pane, l) {
			d.dropLetter(pane, i)
			d.notePhoneDropped(pane, l.phone)
			log.Printf("pane %s: phone message %d dropped: its agent left the pane", pane, l.phone)
		}
	}
}

// dropLetter takes letter i out of pane's queue. Callers hold d.mu.
func (d *Daemon) dropLetter(pane string, i int) {
	if q := d.mail.queue[pane]; len(q) == 1 {
		delete(d.mail.queue, pane)
	} else {
		d.mail.queue[pane] = slices.Delete(q, i, i+1)
		d.wake(pane, 0)
	}
	d.changed()
}

// maxDropped is how many dropped phone messages a pane keeps for its tab.
const maxDropped = 20

// notePhoneDropped lists phone message id among pane's dropped ones.
// Callers hold d.mu.
func (d *Daemon) notePhoneDropped(pane string, id uint64) {
	if d.phoneDropped == nil {
		d.phoneDropped = map[string][]uint64{}
	}
	ids := append(d.phoneDropped[pane], id)
	d.phoneDropped[pane] = ids[max(0, len(ids)-maxDropped):]
	d.changed()
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
// not into the user's draft. Commands to one pane take turns; sendMu, which
// every pane's delivery takes, is held only while keys go in.
func (d *Daemon) phoneCommand(pane, command string) error {
	defer d.paneCommandLock(pane)()
	p, err := d.typeCommand(pane, command)
	if err != nil || !strings.HasPrefix(command, "/model ") {
		return err
	}
	// why: in a conversation Claude Code asks to confirm the switch, which the phone's pick already did.
	for deadline := time.Now().Add(switchWait.get()); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if agent.SwitchModelAsked(p.Snapshot()) {
			d.sendMu.Lock()
			defer d.sendMu.Unlock()
			return d.handle(context.Background(), proto.Input{Pane: pane, Data: press("1", p.Modes())})
		}
	}
	return nil
}

// typeCommand checks pane is ready for command and types it with Enter,
// under sendMu, as deliver types a message.
func (d *Daemon) typeCommand(pane, command string) (Pane, error) {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	d.mu.Lock()
	prov := d.paneProvider(pane)
	d.mu.Unlock()
	if prov != model.ProviderClaude {
		return nil, errors.New("only Claude Code takes the model and effort from here")
	}
	p, busy, err := d.ready(pane)
	if err != nil {
		if busy {
			return nil, fmt.Errorf("%w; try again when it waits at its input box", err)
		}
		return nil, err
	}
	if agent.FormUp(p.Snapshot(), "") {
		return nil, errors.New("that pane shows a form; answer it in its tab")
	}
	d.noteUserKey(pane, nil)
	if err := d.handle(context.Background(), proto.Input{Pane: pane, Data: input.Text(remote.Clean(command))}); err != nil {
		return nil, err
	}
	return p, d.pressEnter(pane, p, command)
}

// paneCommandLock locks pane's slash commands and returns the unlock.
func (d *Daemon) paneCommandLock(pane string) func() {
	d.mu.Lock()
	if d.paneCmd == nil {
		d.paneCmd = map[string]*sync.Mutex{}
	}
	m := d.paneCmd[pane]
	if m == nil {
		m = &sync.Mutex{} // ponytail: one per pane that took a command, never freed; a few bytes each
		d.paneCmd[pane] = m
	}
	d.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// switchWait is how long after /model the confirm dialog may take to show.
var switchWait = newTiming(1500 * time.Millisecond)

type phoneSettings struct{ model, effort string }

// keepClaudeSettings updates the phone's settings cache. Callers hold d.mu.
func (d *Daemon) keepClaudeSettings(id string, p Pane, m, e string) {
	if d.panes[id] != p || d.paneProvider(id) != model.ProviderClaude {
		return
	}
	settings := d.phoneSettings[id]
	before := settings
	if m != "" {
		settings.model = m
	}
	if e != "" {
		settings.effort = e
	}
	if settings == before {
		return
	}
	if d.phoneSettings == nil {
		d.phoneSettings = map[string]phoneSettings{}
	}
	d.phoneSettings[id] = settings
	d.changed()
}
