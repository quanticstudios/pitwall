package app

import (
	"image"
	"slices"
	"strconv"
	"time"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Chatter is optionally implemented by a Backend: the transcript of the
// chat the window shows, as last read from the daemon, false before the
// first read comes. Each call may read what came since.
type Chatter interface {
	Chat(id string) (session.Snapshot, bool)
}

// ChatResulter is optionally implemented by a Backend: the daemon's
// answers to the window's ChatSend requests and its refusals of any other
// Chat* request on chat id, each once.
type ChatResulter interface {
	ChatResults(id string) []chatin.Result
}

// ChatLoader is optionally implemented by a Chatter: when the window
// first asked for chat id's transcript, and why none came, "" while one
// may; ChatRetry asks again.
type ChatLoader interface {
	ChatWait(id string) (since time.Time, why string)
	ChatRetry(id string)
}

// ChatPager is optionally implemented by a Chatter: ChatOlder reads the
// page of chat id's history before its oldest item and reports whether
// there is one; ChatItem reads an item the transcript has cut, whole.
type ChatPager interface {
	ChatOlder(id string) bool
	ChatItem(id, item string)
}

// ChatPoll is how often the window asks for a live chat's transcript
// while it shows it.
const ChatPoll = 150 * time.Millisecond

// layoutChat draws the chat tab ws: the chat's transcript with the
// composer under it, and sends the approvals answered on it and the
// composer's messages.
func (u *ui) layoutChat(gtx gl.Context, st *model.State, ws *model.Workspace) {
	ch, ok := u.b.(Chatter)
	if !ok {
		gl.Center.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
			return para(gtx, u.th, u.th.UIFont, u.th.Sp(theme.Body), u.th.Muted, "Chat: read it with pitwall chat read "+ws.Chat)
		})
		return
	}
	// The tab's frame sits pane_margin in, as a tab of one terminal's does.
	m := gtx.Dp(unit.Dp(u.cfg.PaneMargin))
	tui := u.tuiShown(st, ws.Chat, gtx.Now)
	if tui != nil { // over the chat: this defer runs after the others pop
		defer u.layoutTUI(gtx, st, ws.Chat, tui, image.Rect(m, m, gtx.Constraints.Max.X-m, gtx.Constraints.Max.Y-m))
	}
	paint.FillShape(gtx.Ops, u.th.Surface, clip.Rect{Max: gtx.Constraints.Max}.Op())
	defer op.Offset(image.Pt(m, m)).Push(gtx.Ops).Pop()
	size := image.Pt(max(0, gtx.Constraints.Max.X-2*m), max(0, gtx.Constraints.Max.Y-2*m))
	in, cl := u.chatFrame(gtx, size, !u.keysHeld(st) && u.nav.peek == "", m == 0)
	defer cl.Pop()
	defer op.Offset(in.Min).Push(gtx.Ops).Pop()
	gtx.Constraints = gl.Exact(in.Size())
	if u.keysHeld(st) || u.nav.peek != "" || tui != nil {
		u.chatHead.closeTools()
	}
	head := u.layoutChatHead(gtx, st, ws)
	defer op.Offset(image.Pt(0, head)).Push(gtx.Ops).Pop()
	gtx.Constraints.Max.Y = max(0, gtx.Constraints.Max.Y-head)
	gtx.Constraints.Min.Y = min(gtx.Constraints.Min.Y, gtx.Constraints.Max.Y)
	u.chatBody(gtx, st, ch, ws.Chat, ws, tui == nil && !u.chatHead.toolsOpen, func() { u.toggleChatTerminal(st, ws) })
}

// chatBody draws chat id's transcript with the composer under it, and
// sends the composer's messages and the requests answered on it. ws is
// its tab, whose folder the transcript's paths are in; focus is whether the composer may
// take the keys, as a pane chat in an unfocused split may not; term
// opens the chat's terminal view.
func (u *ui) chatBody(gtx gl.Context, st *model.State, ch Chatter, id string, ws *model.Workspace, focus bool, term func()) {
	dir := ws.Path
	s, ok := ch.Chat(id)
	// A pane chat's Live is what its agent's screen shows ahead of the
	// transcript; it draws as the transcript's newest items.
	s.Content = append(slices.Clip(s.Content), s.Live...)
	if ok {
		t := u.chatTranscript(st, id)
		t.Width, t.Copy, t.Dir = unit.Dp(u.cfg.Chat.Width), putClipboard, dir
		t.ToolCalls, t.ChatLayout, t.CodeScale = u.cfg.Chat.ToolCalls, u.cfg.Chat.Layout, u.codeScale()
		paneLimit(st, id, &s, gtx.Now)
		t.Limit = u.limitResume(st, id, ws, &s)
		t.CopyOnSelect = u.cfg.CopyOnSelect
		t.Copied = func(gtx gl.Context, s string, r image.Rectangle) {
			u.showNotice(gtx, copiedText(s), r.Sub(u.contentOrigin))
		}
		if p, ok := ch.(ChatPager); ok {
			t.Older = func() bool { return p.ChatOlder(id) }
			t.Full = func(item string) { p.ChatItem(id, item) }
		}
		t.PRs, t.Open, t.Invalidate = st.PRs, func(target string) { u.openChatTarget(st, id, dir, target) }, u.invalidate
		t.Run = func(cmd string) { u.runCode(st, id, cmd) }
		aside := u.chatTools(st, id, t)
		t.Servers = u.serverList(st, ws)
		u.serverStops(gtx, ws.ID, t)
		if focus {
			for {
				if _, ok := gtx.Event(key.Filter{Name: "F", Required: key.ModShortcut}); !ok {
					break
				}
				t.OpenFind()
			}
		}
		cmp := u.composer(st, id)
		if u.drawn == nil {
			u.drawn = map[string]bool{}
		}
		u.drawn[id] = true
		if f, ok := u.b.(ChatResulter); ok {
			for _, r := range f.ChatResults(id) {
				cmp.Result(r, gtx.Now)
				if r.Message == "" {
					continue
				}
				switch m := r.Req.(type) {
				case proto.ChatApprove:
					t.Refused(m.Request)
				case proto.ChatAnswer:
					t.Refused(m.Request)
				}
			}
		}
		env := u.chatEnv(st, id, &s, t)
		env.Focus = env.Focus && focus && !t.Zoomed() && !t.Typing() && !t.FindOpen()
		if env.Focus { // before the composer, whose field would take Ctrl+C
			t.CopyKey(gtx)
			u.adds.last = id // a terminal selection in a tab with no chat comes here
		}
		// A pane chat's Claude reads @ paths from its pane's folder.
		cwd := dir
		if p := findPane(st, id); p != nil && p.Cwd != "" {
			cwd = p.Cwd
		}
		env.Mentions, env.Branch = u.mentions(cwd), ws.Branch
		env.Suggest = u.suggestion(st, id, &s, cmp)
		env.Copy = func(s string) { putClipboard(gtx, s) }
		gtx.Metric = u.chatMetric(gtx.Metric)
		gl.Flex{Axis: gl.Vertical}.Layout(gtx,
			// Open in aide is the header's menu; the transcript's own copy
			// floated over the reply's top right.
			gl.Flexed(1, func(gtx gl.Context) gl.Dimensions { return t.Layout(gtx, u.th, &s, false) }),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return t.TodoStrip(gtx, u.th, &s) }),
			gl.Rigid(func(gtx gl.Context) gl.Dimensions { return cmp.Layout(gtx, u.th, env) }),
		)
		for _, c := range t.Cites() {
			cmp.AddQuote(c.Text, c.Src)
			gtx.Execute(op.InvalidateCmd{}) // the composer laid out before the quote came
		}
		if list, ok := cmp.StashChanged(); ok {
			u.stashes.save(stashKey(id), list)
		}
		// One selection at a time, the newest: the transcript's or the draft's.
		if t.Began() {
			cmp.Deselect()
		} else if cmp.Selected() {
			t.ClearSelection()
		}
		for _, m := range cmp.Messages() {
			if q, ok := asideOf(m); ok && aside {
				u.send(proto.ChatAside{Chat: id, ID: t.Ask(q), Question: q})
				gtx.Execute(op.InvalidateCmd{}) // the transcript laid out before its card
				continue
			}
			if s, ok := m.(proto.ChatSend); ok && u.terminalCommand(st, id, s, env.Snap.Commands, gtx.Now) {
				continue
			}
			if _, ok := m.(proto.ChatSend); ok {
				t.ToEnd()
				gtx.Execute(op.InvalidateCmd{}) // the transcript laid out before this
			}
			u.send(m)
		}
		for _, a := range t.ServerActions() {
			if a.Open != "" {
				openLink(a.Open)
				continue
			}
			u.send(proto.ServerStop{Workspace: ws.ID, PID: a.PID, Port: a.Port, Force: a.Force})
		}
		for _, a := range t.Answers() {
			u.send(proto.ChatApprove{Chat: id, Request: a.Request, Decision: a.Decision, Reason: a.Reason})
		}
		for _, a := range t.Inputs() {
			u.send(proto.ChatAnswer{Chat: id, Request: a.Request, Answers: a.Answers})
			gtx.Execute(op.InvalidateCmd{}) // a key in the composer answered after the card was drawn
		}
		if u.chatActions(id, t.Actions(), paneChat(st, id), cmp) {
			gtx.Execute(op.InvalidateCmd{}) // the composer laid out before the prompt came back
		}
	} else {
		u.chatLoading(gtx, ch, id, term)
	}
	if c := findChat(st, id); !ok || c != nil && chatLive(c.State) {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(ChatPoll)})
	}
}

// chatEnv is what chat id's composer needs this frame; t is the chat's
// transcript, which answers its pending request by the keys typed into
// an empty composer.
func (u *ui) chatEnv(st *model.State, id string, s *session.Snapshot, t *panel.Transcript) chatin.Env {
	width := unit.Dp(u.cfg.Chat.Width) // the transcript's column, so their edges line up
	if width == 0 {                    // full: the whole tab, less its sides
		width = 1 << 16
	}
	ledger := u.cfg.Chat.Layout == config.ChatLedger
	if ledger {
		width = panel.LedgerWidth(s.Provider)
	}
	env := chatin.Env{Chat: id, Snap: s, Width: width, Ledger: ledger,
		Focus:  !u.keysHeld(st) && u.nav.peek == "" && u.find.pane == "",
		Images: u.link().Level >= proto.ChatAttachLevel, ReadImage: readClipImage, Invalidate: u.invalidate,
		Now: u.link().Level >= proto.ChatNowLevel, Queue: u.link().Level >= proto.QueueLevel,
		Separate: u.link().Level >= proto.SmartQueueLevel, Steers: chatSteers(st, id, u.link().Level),
		Commands: u.link().Level >= proto.SlashLevel}
	env.Key, env.Esc, env.Answer, env.Reveal = t.Key, t.CloseFiles, t.Answer, t.Reveal
	env.TextFiles = u.link().Level >= proto.PastedFileLevel
	env.History, env.Full = func() []chatin.Sent { return panel.Prompts(s) }, t.Full
	env.Steer, env.CtrlEnter = u.cfg.Chat.Steer, u.cfg.Chat.CtrlEnter
	env.QueueEditMods = queueEditMods(u.nav.bind())
	return env
}

// queueEditMods are the modifiers that with Up edit a chat's newest
// queued message: Alt, or Alt+Shift where the window binds Alt+Up, as
// the aide preset's prev_tab does, since the window takes its keys first.
func queueEditMods(b *config.Bindings) key.Modifiers {
	if b.Action(key.Event{Name: key.NameUpArrow, Modifiers: key.ModAlt}) != "" {
		return key.ModAlt | key.ModShift
	}
	return key.ModAlt
}

// chatSteers reports whether Send now goes into chat id's running turn
// at daemon level: a pane chat's agent takes it (Claude Code, Codex,
// Antigravity CLI), a chat's Codex does, and its Claude from
// proto.SmartQueueLevel on; elsewhere Send now stops the turn first.
func chatSteers(st *model.State, id string, level int) bool {
	i := slices.IndexFunc(st.Chats, func(c model.Chat) bool { return c.ID == id })
	if i < 0 {
		return true
	}
	p := st.Chats[i].Provider
	return p == model.ProviderCodex || p == model.ProviderClaude && level >= proto.SmartQueueLevel
}

// composer is chat id's composer, kept in window state so its draft
// survives tab switches; composers of chats that closed go.
func (u *ui) composer(st *model.State, id string) *chatin.Composer {
	if u.composers == nil {
		u.composers = map[string]*chatin.Composer{}
	}
	for k := range u.composers {
		if k != id && findChat(st, k) == nil {
			delete(u.composers, k)
		}
	}
	c := u.composers[id]
	if c == nil {
		c = new(chatin.Composer)
		if u.stashes == nil {
			u.stashes = newStashFiles()
		}
		c.SetStashes(u.stashes.load(stashKey(id)))
		u.composers[id] = c
	}
	return c
}

// stashKey names chat id's stashed drafts: its host's, as two daemons'
// chats may share an id.
func stashKey(id string) string { return curHost() + "\x00" + id }

// chatLive is whether a chat in state may still add to its transcript
// without a change that pushes a new State.
func chatLive(state string) bool {
	return state == session.StateConnecting || state == session.StateRunning || state == session.StateWaiting
}

// chatTranscript is chat id's transcript, kept while the chat is in st,
// so a tab switch keeps its folds, scroll, picks and answered lines.
func (u *ui) chatTranscript(st *model.State, id string) *panel.Transcript {
	if u.transcripts == nil {
		u.transcripts = map[string]*panel.Transcript{}
	}
	for c := range u.transcripts {
		if c != id && findChat(st, c) == nil {
			delete(u.transcripts, c)
		}
	}
	t := u.transcripts[id]
	if t == nil {
		t = &panel.Transcript{Open: openLink}
		u.transcripts[id] = t
	}
	return t
}

// findChat is chat id: a chat tab's, or a pane chat's (paneChatOf).
func findChat(st *model.State, id string) *model.Chat {
	for i := range st.Chats {
		if st.Chats[i].ID == id {
			return &st.Chats[i]
		}
	}
	return paneChatOf(st, id)
}

// chatLoad is the buttons of a chat that did not load.
type chatLoad struct{ retry, term widget.Clickable }

// chatLoading is chat id while its transcript has not come: a line that
// counts the seconds it waits, or why it did not come, with Try again and
// Open terminal view, which term opens.
func (u *ui) chatLoading(gtx gl.Context, ch Chatter, id string, term func()) {
	var since time.Time
	why := ""
	l, ok := ch.(ChatLoader)
	if ok {
		since, why = l.ChatWait(id)
	}
	if u.load.retry.Clicked(gtx) && ok {
		l.ChatRetry(id)
	}
	if u.load.term.Clicked(gtx) {
		term()
	}
	gl.Center.Layout(gtx, func(gtx gl.Context) gl.Dimensions {
		if why != "" {
			return kit.Empty(gtx, u.th, nil, "The chat did not load. "+why, func(gtx gl.Context) gl.Dimensions {
				return gl.Flex{}.Layout(gtx,
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return kit.Button(gtx, u.th, &u.load.term, kit.Secondary, kit.Medium, "Open terminal view")
					}),
					gl.Rigid(gl.Spacer{Width: theme.SpaceS}.Layout),
					gl.Rigid(func(gtx gl.Context) gl.Dimensions {
						return kit.Button(gtx, u.th, &u.load.retry, kit.Primary, kit.Medium, "Try again")
					}),
				)
			})
		}
		line := "Reading the chat…"
		if w := gtx.Now.Sub(since); !since.IsZero() && w >= time.Second {
			line += " " + strconv.Itoa(int(w.Seconds())) + " s"
		}
		return kit.Loading(gtx, u.th, line)
	})
}
