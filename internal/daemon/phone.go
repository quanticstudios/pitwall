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

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/vt"
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
	for i, t := range tabs {
		if t.Chat {
			chats[i] = d.chats[t.Pane]
		} else if t.Provider == model.ProviderClaude || t.Provider == model.ProviderCodex {
			panes[i] = d.panes[t.Pane]
		}
		if settings, ok := d.phoneSettings[t.Pane]; ok && !t.Chat {
			tabs[i].Model, tabs[i].Effort = settings.model, settings.effort
		}
		tabs[i].Dropped = slices.Clone(d.phoneDropped[t.Pane])
	}
	d.mu.Unlock()
	for i, p := range panes {
		if p != nil {
			g := p.Snapshot()
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

// phoneCommand sets a Claude Code pane's model or effort for its running
// session only, from command, "/model <name>" or "/effort <level>". Claude
// Code saves a typed "/model <name>" as the default for every new session,
// so this opens the bare /model picker or /effort slider instead, moves to
// the pick, checks the screen shows it, and presses s, the key for this
// session only. Anything unexpected closes the menu with Esc and changes
// nothing. Only while the agent waits at its input box: not during a turn,
// a question or a form, and not into the user's draft. Commands to one
// pane take turns; sendMu, which every pane's delivery takes, is held only
// while keys go in. Before each key, under sendMu, the menu it is for must
// still show and nobody may have typed in the pane: desktop keys bypass
// sendMu, so the user can close the menu and start a draft while a key
// waits for it.
func (d *Daemon) phoneCommand(pane, command string) error {
	what, value, _ := strings.Cut(strings.TrimPrefix(command, "/"), " ")
	if what != "model" && what != "effort" {
		return fmt.Errorf("pitwall sets only the model and the effort, not %q", command)
	}
	defer d.paneCommandLock(pane)()
	p, since, err := d.typeCommand(pane, "/"+what)
	if err != nil {
		return err
	}
	m := menuKeys{d, pane, p, since}
	if err := m.pickForSession(what, value); err != nil || what != "model" {
		return err
	}
	// why: in a conversation Claude Code asks to confirm the switch, which the phone's pick already did.
	for deadline := time.Now().Add(switchWait.get()); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if agent.SwitchModelAsked(p.Snapshot()) {
			return m.press(agent.SwitchModelAsked, "1")
		}
	}
	return nil
}

// menuKeys presses keys into the menu a phone command opened in pane p at
// since, the time of the user's last key then.
type menuKeys struct {
	d     *Daemon
	pane  string
	p     Pane
	since time.Time
}

// pickForSession moves the open /model picker or /effort slider in p to
// value one step at a time, reading the screen after each, and presses s.
// A picker taller than its pane shows only some rows. While the pick is out
// of view it walks up to the first row, as Up there wraps to the picker's
// "More models" row, then down until the rows end.
func (m menuKeys) pickForSession(what, value string) error {
	back, forth, top := key.NameUpArrow, key.NameDownArrow, 1
	up := func(g vt.Grid) bool { _, _, ok := agent.ModelMenu(g); return ok }
	read := func() (target, at int, ok bool) {
		g := m.p.Snapshot()
		if what == "model" {
			rows, cursor, ok := agent.ModelMenu(g)
			return agent.ModelRow(rows, value), cursor, ok
		}
		levels, at, ok := agent.EffortMenu(g)
		return slices.Index(levels, value), at, ok
	}
	if what == "effort" {
		back, forth, top = key.NameLeftArrow, key.NameRightArrow, 0
		up = func(g vt.Grid) bool { _, _, ok := agent.EffortMenu(g); return ok }
	}
	target, at, ok := waitMenu(read, func(_, _ int, ok bool) bool { return ok })
	if !ok {
		err := fmt.Errorf("Claude Code's /%s menu did not open, so nothing changed; try again in a moment", what)
		if agent.SessionMenuUp(m.p.Snapshot()) {
			return m.cancel(err)
		}
		return err
	}
	offersNo := fmt.Errorf("Claude Code here offers no %s %s, so nothing changed", value, what)
	walk := back
	for range maxMenuSteps {
		if target == at {
			return m.press(up, "s")
		}
		looking := target < 0
		if looking && walk == back && at == top {
			walk = forth
		}
		step := walk
		switch {
		case target > at:
			step = forth
		case target >= 0:
			step = back
		}
		if err := m.press(up, step); err != nil {
			return err
		}
		from := at
		target, at, ok = waitMenu(read, func(_, at int, ok bool) bool { return ok && at != from })
		switch {
		case !ok && looking && step == forth:
			return m.cancel(offersNo) // past the last row
		case !ok:
			return m.cancel(fmt.Errorf("Claude Code's /%s menu changed, so nothing changed", what))
		case at != from:
		case looking && walk == back:
			walk = forth // a slider stops at its lowest level
		case looking:
			return m.cancel(offersNo)
		default:
			return m.cancel(fmt.Errorf("Claude Code's /%s menu did not move to %s, so nothing changed", what, value))
		}
	}
	return m.cancel(fmt.Errorf("Claude Code's /%s menu did not move to %s, so nothing changed", what, value))
}

// maxMenuSteps caps the keys pickForSession presses in one menu.
const maxMenuSteps = 40

// waitMenu reads the menu until done says so or menuWait passes, and
// returns the last read.
func waitMenu(read func() (int, int, bool), done func(int, int, bool) bool) (target, at int, ok bool) {
	for deadline := time.Now().Add(menuWait.get()); ; time.Sleep(20 * time.Millisecond) {
		target, at, ok = read()
		if done(target, at, ok) || time.Now().After(deadline) {
			return target, at, ok
		}
	}
}

// cancel closes the open menu with Esc, which saves nothing, and returns
// err.
func (m menuKeys) cancel(err error) error {
	if e := m.press(agent.SessionMenuUp, key.NameEscape); e != nil {
		return e
	}
	return err
}

// press presses k under sendMu, if the pane is still p, nobody typed in it
// since m.since, and up reads its screen as the menu k is for.
func (m menuKeys) press(up func(vt.Grid) bool, k key.Name) error {
	d := m.d
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	d.mu.Lock()
	same, typed := d.panes[m.pane] == m.p, d.mail.keyAt[m.pane].After(m.since)
	d.mu.Unlock()
	switch {
	case !same:
		return errors.New("that pane closed, so pitwall stopped")
	case typed:
		return errors.New("someone typed in that pane, so pitwall left Claude Code's menu to them")
	case !up(m.p.Snapshot()):
		return errors.New("Claude Code's menu closed, so pitwall stopped")
	}
	return d.handle(context.Background(), proto.Input{Pane: m.pane, Data: press(k, m.p.Modes())})
}

// typeCommand checks pane is ready for command and types it with Enter,
// under sendMu, as deliver types a message. since is the time it records as
// the user's last key in pane.
func (d *Daemon) typeCommand(pane, command string) (_ Pane, since time.Time, err error) {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	d.mu.Lock()
	prov := d.paneProvider(pane)
	d.mu.Unlock()
	if prov != model.ProviderClaude {
		return nil, since, errors.New("only Claude Code takes the model and effort from here")
	}
	p, busy, err := d.ready(pane)
	if err != nil {
		if busy {
			return nil, since, fmt.Errorf("%w; try again when it waits at its input box", err)
		}
		return nil, since, err
	}
	if agent.FormUp(p.Snapshot(), "") {
		return nil, since, errors.New("that pane shows a form; answer it in its tab")
	}
	d.noteUserKey(pane, nil)
	d.mu.Lock()
	since = d.mail.keyAt[pane]
	d.mu.Unlock()
	if err := d.handle(context.Background(), proto.Input{Pane: pane, Data: input.Text(remote.Clean(command))}); err != nil {
		return nil, since, err
	}
	return p, since, d.pressEnter(pane, p, command)
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

// menuWait is how long Claude Code may take to open or move its /model
// picker or /effort slider.
var menuWait = newTiming(3 * time.Second)

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
