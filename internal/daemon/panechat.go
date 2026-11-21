package daemon

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/antigravity"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/session/codex"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// A pane chat is the desktop chat of a Claude Code or Codex TUI running in
// a pane, whatever started it: the chat id is the pane's id. Nothing
// starts or stops the agent for it. Its transcript (Codex's rollout),
// read as it grows, and the pane's hooks and screen make the snapshot;
// what the chat sends goes into the PTY as keys, as the phone's do. It
// lasts while the agent runs in the pane; then the window shows the pane
// again. Only a hook's transcript_path names the file, or, for a resumed
// pane, the session id in a file's name (agyResumed, codexResumed): until
// then the chat is empty, and it starts over on the file once it is named.

// paneChat is one pane chat's snapshot and how far it read the transcript.
type paneChat struct {
	mu    sync.Mutex
	id    string
	path  string              // the transcript
	off   int64               // bytes of it read, up to the last whole line
	start int64               // where the chat's reading of it starts (tailStart); the history before is paged
	pages map[string][2]int64 // the byte range of the page each item paged in came from
	// at is the transcript offset of the line each of the snapshot's items
	// came from, cur that of the line being read: window cuts the snapshot
	// at an item, and the page before it reads up to its line.
	at  []int64
	cur int64
	// tailOff and tailLen are pc.off and the snapshot's items when the tail
	// was last read: the chat reads it again only once it outgrows them.
	tailOff int64
	tailLen int
	prov    model.Provider
	tr      paneReader
	snap    session.Snapshot
	seq     int64
	recent  []session.Delta
	// recentSize is the bytes of recent's JSON (keepRecent).
	recentSize int
	footer     bool          // the TUI's footer gave the mode at least once
	tui        phoneSettings // the TUI's model and effort as last put in the snapshot
	conf       *string       // the effort in Claude Code's user settings, read once
	ctx        int64         // tokens in context at the last assistant message, 0 before one
	model      string        // that message's model
	moded      bool          // the chat showed a mode: a change after it gets a divider
	// replied: the running turn has Claude's own output, so a hook saying
	// the agent stopped can end it.
	replied bool
	// agents are the subagents' own transcripts, by their tool call's id,
	// and how far each was read.
	agents  map[string]*agentFile
	scanned time.Time // when subagents last looked for transcripts it lacked
	// lineAt is the offset of each line of the conversation read, by its
	// uuid, and tip the last one's: a prompt whose parent is not the tip
	// branches off, as after a rewind (branched).
	lineAt map[string]int64
	tip    string
	// limit is the pane's Claude plan limit, which /usage shows; nil for none.
	limit func() (flow.Limit, bool)
	lv    liveState // what the screen shows ahead of the transcript (live)
	slash slashList // the agent's / menu
}

// slashList is a pane chat's slash commands and what they were read for.
type slashList struct {
	conf, cwd string
	stamp     string    // the provider's CommandsStamp when they were read
	checked   time.Time // when the stamp was last taken
	cmds      []session.Command
}

// slashEvery is how often a pane chat stats Claude Code's command and
// skill files for one added, removed or edited; the chat reads every
// ChatPoll.
const slashEvery = 2 * time.Second

// commands puts the agent's / menu in the snapshot when it changed:
// Claude Code's from its config dir and the pane's project, agy's from
// its HOME and the project, each read again once their files or the
// folders change; Codex's built-ins, which are all its menu lists.
// Callers hold pc.mu.
func (pc *paneChat) commands(l paneLive, now time.Time) {
	s := &pc.slash
	var list func(dir, cwd string) []session.Command
	var stamp func(dir, cwd string) string
	var dir string
	switch pc.prov {
	case model.ProviderClaude:
		list, stamp, dir = claude.Commands, claude.CommandsStamp, l.conf
	case model.ProviderCodex:
		s.cmds = codex.Builtins
	case model.ProviderAntigravity:
		list, stamp, dir = antigravity.Commands, antigravity.CommandsStamp, l.home
	}
	if list != nil && (s.cmds == nil || s.conf != dir || s.cwd != l.cwd || now.Sub(s.checked) >= slashEvery) {
		s.conf, s.cwd, s.checked = dir, l.cwd, now
		if st := stamp(dir, l.cwd); st != s.stamp || s.cmds == nil {
			s.cmds, s.stamp = list(dir, l.cwd), st
		}
	}
	if !slices.Equal(s.cmds, pc.snap.Commands) {
		pc.reduce([]session.Event{{Type: session.EventCommands, Commands: slices.Clone(s.cmds)}})
	}
}

// transcript is a new reader of pc's transcript.
func (pc *paneChat) transcript() paneReader {
	switch pc.prov {
	case model.ProviderCodex:
		return codex.NewRollout()
	case model.ProviderAntigravity:
		return antigravity.NewTranscript()
	}
	t := claude.NewTranscript()
	t.Limit = pc.limit
	return t
}

// claudeLimit finds profile's Claude plan limit, for a pane chat's /usage.
// Callers hold d.mu.
func (d *Daemon) claudeLimit(profile string) func() (flow.Limit, bool) {
	limits := d.o.Limits
	if limits == nil {
		return nil
	}
	return func() (flow.Limit, bool) {
		for _, l := range limits() {
			if l.Provider == model.ProviderClaude && l.Name == "" && l.Profile == profile {
				return l, true
			}
		}
		return flow.Limit{}, false
	}
}

// paneReader turns a pane chat's transcript lines into events:
// claude.Transcript for Claude Code, codex.Rollout for Codex,
// antigravity.Transcript for Antigravity.
type paneReader interface {
	Line([]byte) []session.Event
	Running() bool
	End(state string) []session.Event
	Ask(id, detail string) []session.Event
	Resolve() []session.Event
	Subagents() map[string]bool
	Subagent(call string, line []byte) []session.Event
}

// markOf is prov's claude.MarkOf, which tailStart pages by.
func markOf(prov model.Provider) func([]byte) claude.Mark {
	switch prov {
	case model.ProviderCodex:
		return codex.MarkOf
	case model.ProviderAntigravity:
		return antigravity.MarkOf
	}
	return claude.MarkOf
}

// agentFile is a subagent's transcript and how far it was read; done once
// its subagent ended and the file was read to its end.
type agentFile struct {
	path string
	off  int64
	done bool
}

// paneLive is what a pane chat's catch-up reads of its pane under d.mu.
type paneLive struct {
	p    Pane
	path string
	cwd  string
	mode string         // the permission mode its hooks reported (Pane.AgentMode)
	act  model.Activity // zero State when the pane has none
	tui  phoneSettings  // the model and effort its screen showed (keepClaudeSettings)
	conf string         // Claude Code's config dir for it
	home string         // agy's HOME for it
	prov model.Provider // the agent its hooks named
	// queued is what the chat queued mid-turn (chatQueue), held whether
	// it waits for the user's Resume or Send, reason why it waits.
	queued []session.QueuedInput
	held   bool
	reason string
}

// paneChatOf is pane id's chat, made on first use, and its pane's state;
// false when the pane runs no Claude Code, Codex or Antigravity.
func (d *Daemon) paneChatOf(id string) (*paneChat, paneLive, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for k := range d.paneChats {
		if d.panes[k] == nil {
			delete(d.paneChats, k)
		}
	}
	i := slices.IndexFunc(d.st.Panes, func(p model.Pane) bool { return p.ID == id })
	if i < 0 || d.chats[id] != nil || !model.PaneChat(d.st.Panes[i]) || d.panes[id] == nil {
		delete(d.paneChats, id)
		return nil, paneLive{}, false
	}
	p := d.st.Panes[i]
	l := paneLive{p: d.panes[id], path: p.Transcript, cwd: p.Cwd, mode: p.AgentMode, tui: d.phoneSettings[id], conf: claudeConfigDir(p), prov: p.Provider}
	if ai := d.activityIndex(id); ai >= 0 {
		l.act = d.st.Activities[ai]
	}
	l.queued, l.held, l.reason = d.chatQueued(id)
	pc := d.paneChats[id]
	if p.Provider != model.ProviderClaude {
		l.conf = ""
	}
	if p.Provider == model.ProviderAntigravity {
		l.home = homeOf(p.Env)
	}
	if pc == nil || pc.path != l.path || pc.prov != p.Provider { // a new session (claude again, /clear, /resume) starts over
		mode := "default"
		if p.Provider == model.ProviderAntigravity {
			mode = "" // why: agy's mode is its footer's; until the chat has read it, the chip shows none rather than a guess
		}
		pc = &paneChat{id: id, path: l.path, prov: p.Provider,
			snap: session.Snapshot{SessionID: id, PaneID: id, WorkspaceID: p.WorkspaceID, Provider: p.Provider,
				SessionState: session.StateReady, Running: true, PermissionMode: mode, InteractionMode: "build"}}
		if w := d.workspace(p.WorkspaceID); w != nil {
			pc.limit = d.claudeLimit(w.Profile)
		} else {
			pc.limit = d.claudeLimit("")
		}
		pc.tr = pc.transcript()
		if d.paneChats == nil {
			d.paneChats = map[string]*paneChat{}
		}
		d.paneChats[id] = pc
	}
	return pc, l, true
}

// paneChatRequest handles a Chat* message for a pane chat; false when m is
// not one or names no pane chat.
func (d *Daemon) paneChatRequest(ctx context.Context, m any) (any, bool) {
	var id string
	switch m := m.(type) {
	case proto.ChatRead:
		id = m.Chat
	case proto.ChatSend:
		id = m.Chat
	case proto.ChatInterrupt:
		id = m.Chat
	case proto.ChatApprove:
		id = m.Chat
	case proto.ChatAnswer:
		id = m.Chat
	case proto.ChatSet:
		id = m.Chat
	case proto.ChatTerminal:
		id = m.Chat
	case proto.ChatQueue:
		id = m.Chat
	case proto.ChatRewind:
		id = m.Chat
	default:
		return nil, false
	}
	pc, l, ok := d.paneChatOf(id)
	if !ok {
		return nil, false
	}
	if r, ok := m.(proto.ChatRead); ok {
		switch {
		case r.Item != "":
			it, err := pc.item(r.Item)
			if err != nil {
				return chatResult(id, "", "", err), true
			}
			return proto.ChatDeltas{Chat: id, Item: itemJSON(it)}, true
		case r.Before > 0:
			s, older, err := pc.page(r.Before)
			if err != nil {
				log.Printf("pane chat %s: page before %d: %q", id, r.Before, err)
				return chatResult(id, "", "", err), true
			}
			return proto.ChatDeltas{Chat: id, Snapshot: wireSnapshot(s, 0), Page: r.Before, Older: older}, true
		}
		pc.mu.Lock()
		defer pc.mu.Unlock()
		if err := pc.catchUp(l); err != nil && pc.off == 0 {
			return chatResult(id, "", "", fmt.Errorf("Pitwall could not read the agent's transcript: %w", err)), true
		}
		return pc.read(r.After), true
	}
	var note string
	var err error
	switch m := m.(type) {
	case proto.ChatSend:
		note, err = d.paneSend(pc, l, m)
	case proto.ChatInterrupt:
		if busy(l.act.State) {
			d.mu.Lock()
			d.chatHold(id, true)
			d.mu.Unlock()
			err = d.paneKey(id, l.p, press(key.NameEscape, l.p.Modes()))
		}
	case proto.ChatApprove:
		err = d.paneApprove(id, l, m)
	case proto.ChatAnswer:
		err = d.paneAnswer(id, l, m)
	case proto.ChatSet:
		err = d.paneSet(id, l, m)
	case proto.ChatTerminal:
		// The window flips a pane chat's view itself; claude runs either way.
	case proto.ChatQueue:
		err = d.paneQueue(id, m)
	case proto.ChatRewind:
		err = d.paneRewind(pc, m)
	}
	return chatResult(id, "", note, err), true
}

// busy reports whether an agent in state is mid-turn.
func busy(s model.AgentState) bool {
	return s == model.StateWorking || waits(s)
}

// waits reports whether an agent in state waits on a prompt on its screen.
func waits(s model.AgentState) bool {
	return s == model.StatePendingApproval || s == model.StatePlanReady || s == model.StateAwaitingInput
}

// catchUp reads what the transcript gained, then brings the turn, the open
// request and the mode in line with the pane. It reports why it could not
// read the transcript; one not written yet reads as empty. Callers hold
// pc.mu.
func (pc *paneChat) catchUp(l paneLive) (rerr error) {
	// The subagents first: their last calls end before the line that ends
	// them reaches the session's transcript.
	pc.subagents()
	if pc.path != "" { // no hook named it yet: the chat stays empty, its chips and approvals still live
		f, err := os.Open(pc.path)
		if err == nil {
			err = pc.readOn(f, l.tui.model)
			f.Close()
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("pane chat %s: transcript: %q", pc.id, err)
			rerr = err
		}
	}
	a := l.act
	isClaude := pc.prov == model.ProviderClaude
	switch {
	case !isClaude && a.State == model.StatePendingApproval:
		// shortcut: Codex's and Antigravity's questions and plans stay in the terminal; only their approvals reach the chat.
		pc.reduce(pc.tr.Ask(strconv.FormatInt(a.UpdatedAt.UnixNano(), 10), a.Detail))
	case isClaude && waits(a.State) && (a.State != model.StateAwaitingInput || agent.ReadQuestion(model.ProviderClaude, l.p.Snapshot()) != nil):
		pc.reduce(pc.tr.Ask(strconv.FormatInt(a.UpdatedAt.UnixNano(), 10), a.Detail))
	default:
		pc.reduce(pc.tr.Resolve())
	}
	pc.offers(l)
	// Claude Code marks a turn's end in the transcript; a hook saying the
	// agent stopped after the turn began ends one it did not mark.
	// why: a background task's end marks the agent done just as the turn its notification wakes begins.
	if pc.tr.Running() && pc.replied && a.State != "" && !busy(a.State) && a.UpdatedAt.After(pc.turnStart()) {
		pc.reduce(pc.tr.End(session.TurnCompleted))
	}
	// The footer says the mode the TUI runs in now; the hooks' mode stands
	// in only until it does, as AgentMode keeps bypass across Shift+Tab.
	mode := ""
	switch {
	case isClaude:
		mode = agent.ClaudeMode(l.p.Snapshot())
	case pc.prov == model.ProviderAntigravity:
		mode = agyMode(l.p.Snapshot())
	}
	if mode != "" {
		pc.footer = true
	} else if !pc.footer && isClaude {
		mode = l.mode
	}
	if mode != "" && mode != pc.snap.PermissionMode {
		pc.reduce([]session.Event{{Type: session.EventSettings, Mode: mode}})
		if pc.moded {
			pc.reduce([]session.Event{{Type: session.EventContent, ItemID: newUUID(), Kind: session.KindSystem,
				Phase: session.PhaseSetting, Text: session.SettingText("", "", mode)}})
		}
	}
	pc.moded = pc.moded || mode != ""
	if r, ok := pc.tr.(interface{ Settings() (string, string) }); ok {
		l.tui.model, l.tui.effort = r.Settings()
	}
	// why: agy's footer says the model and effort the next turn takes; its transcript says only the last turn's.
	if _, mdl, eff, ok := agent.AgyFooter(l.p.Snapshot()); ok && pc.prov == model.ProviderAntigravity {
		l.tui.model, l.tui.effort = mdl, eff
	}
	pc.settings(l)
	pc.commands(l, time.Now())
	if !slices.EqualFunc(l.queued, pc.snap.QueuedInputs, func(a, b session.QueuedInput) bool {
		return a.ID == b.ID && a.Text == b.Text && a.Error == b.Error && a.Alone == b.Alone
	}) || l.held != pc.snap.QueuePaused || l.reason != pc.snap.QueueReason {
		set, _ := json.Marshal(map[string]any{"queuedInputs": append([]session.QueuedInput{}, l.queued...), "queuePaused": l.held, "queueReason": l.reason})
		pc.commit([]session.Delta{{Type: session.DeltaSessionPatch, Set: set}}, session.Stamp(time.Now()))
	}
	pc.live(l)
	return rerr
}

// offers marks the approval the pane waits on with what its prompt
// offers: one with no Allow for this session, as Claude Code's prompt for
// a hook that asks, has the card leave that button out. l.act.Buttons are
// the offers checkForms read off the screen. A hook's reason for asking,
// which Claude Code prints over the prompt, becomes the approval's detail,
// the card's line under the command. Callers hold pc.mu.
func (pc *paneChat) offers(l paneLive) {
	a := l.act
	if a.State != model.StatePendingApproval {
		return
	}
	no := !slices.Contains(a.Buttons, agent.AllowAlways)
	if pc.prov == model.ProviderAntigravity && l.p != nil {
		no = !agent.AgyAlwaysOffered(l.p.Snapshot())
	}
	reason := ""
	if pc.prov == model.ProviderClaude && l.p != nil {
		reason = agent.HookReason(l.p.Snapshot())
	}
	for _, ap := range pc.snap.PendingApprovals {
		detail := cmp.Or(reason, value(ap.Detail))
		if ap.RequestType == session.RequestPlan || ap.NoSession == no && value(ap.Detail) == detail {
			continue
		}
		pc.reduce([]session.Event{{Type: session.EventRequestOpened, RequestID: ap.ID, RequestType: ap.RequestType,
			Request: ap.Request, Detail: detail, NoSession: no}})
	}
}

// A pane chat reads its transcript from the end: tailBytes back, to the
// prompt of the turn with the tailItems-th chat line from the end or to
// the last compaction, and the history before that in pages of the same
// reach as the window scrolls up to it. Once the transcript outgrows what
// the chat read of its tail by retailBytes or retailItems, or gains more
// than tailBytes unread, the chat reads its tail again, so neither the
// daemon's memory nor a snapshot grows with the file. A tail that is
// bigger than these itself, one turn of thousands of calls, is read once.
const (
	tailBytes   = 4 << 20
	tailItems   = 200
	retailBytes = 16 << 20
	retailItems = 2000
)

// readOn reduces what f, the transcript, gained since pc.off, or reads
// its tail again (see tailBytes); tui is the model the TUI shows, for the
// context window. Callers hold pc.mu.
func (pc *paneChat) readOn(f *os.File, tui string) error {
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	end := fi.Size()
	// A transcript shorter than what was read was rewritten: a new chat.
	retail := pc.off == 0 || end < pc.off || end-pc.off > tailBytes || pc.off-pc.tailOff > retailBytes || len(pc.snap.Content)-pc.tailLen > retailItems
	if retail {
		start, err := tailStart(f, end, markOf(pc.prov))
		if err == nil && pc.prov != model.ProviderCodex {
			start, err = rewoundStart(f, start, end)
		}
		if err != nil {
			return err
		}
		s := pc.snap
		pc.tr, pc.snap = pc.transcript(), session.Snapshot{SessionID: s.SessionID, PaneID: s.PaneID, WorkspaceID: s.WorkspaceID,
			Provider: s.Provider, SessionState: session.StateReady, Running: true, PermissionMode: s.PermissionMode,
			InteractionMode: "build", Revision: s.Revision}
		pc.start, pc.off, pc.pages, pc.agents, pc.at, pc.lineAt, pc.tip = start, start, nil, nil, nil, nil, ""
		pc.tui = phoneSettings{}                             // the new snapshot lacks the TUI's model until settings puts it back
		defer func() { pc.recent, pc.recentSize = nil, 0 }() // no reader holds what this replays: each gets a snapshot
	}
	b := make([]byte, end-pc.off)
	n, err := f.ReadAt(b, pc.off)
	pc.off += int64(pc.lines(b[:n], tui))
	if retail {
		pc.tailOff, pc.tailLen = pc.off, len(pc.snap.Content)
	}
	if err == io.EOF {
		err = nil
	}
	return err
}

// lines reduces b's whole lines and reports the bytes they take; tui is
// the model the TUI shows, for the context window. Callers hold pc.mu.
func (pc *paneChat) lines(b []byte, tui string) int {
	pc.cur = pc.off // b starts at pc.off
	end := bytes.LastIndexByte(b, '\n')
	if end < 0 {
		return 0
	}
	next := pc.off
	for line := range bytes.SplitSeq(b[:end], []byte("\n")) {
		pc.cur, next = next, next+int64(len(line))+1
		if t, m, ok := contextUsage(line); ok {
			pc.ctx, pc.model = t, m
		}
		es := pc.tr.Line(line)
		pc.branched(line, es)
		for i := range es {
			switch {
			case es[i].Type == session.EventTurnStarted:
				pc.replied = false
			case es[i].Type == session.EventContent && es[i].Kind != session.KindSystem:
				// why: a background shell's end notice updates a call an earlier turn made; that is no reply in this one.
				pc.replied = pc.replied || es[i].Kind != session.KindToolCall || !slices.ContainsFunc(pc.snap.Content, func(it session.Item) bool { return it.ID == es[i].ItemID })
			}
			// A turn's usage report says the context as the meter reads it.
			if es[i].Type == session.EventUsage && pc.ctx > 0 {
				ctx, w := pc.ctx, contextWindow(pc.model, tui, pc.ctx)
				es[i].ContextTokens, es[i].MaxTokens = &ctx, &w
			}
		}
		pc.reduce(es)
	}
	pc.cur = next // what is added after the lines, as a mode change, comes after them
	return end + 1
}

// tailStart is where to read the transcript in r before offset end from
// (see tailBytes): a prompt line's offset; the first whole line when none
// is in reach; 0 when the reach takes in the whole file. Whole lines in
// reach that show nothing, as when one line is longer than it, widen it.
func tailStart(r io.ReaderAt, end int64, markOf func([]byte) claude.Mark) (int64, error) {
	for reach := int64(tailBytes); ; reach *= 2 {
		from := max(0, end-reach)
		b := make([]byte, end-from)
		if n, err := r.ReadAt(b, from); err != nil && !(err == io.EOF && int64(n) == end-from) {
			return 0, err
		}
		start, items := int64(-1), 0
		hi := bytes.LastIndexByte(b, '\n') // the newline that ends the line in hand
		for hi >= 0 {
			lo := bytes.LastIndexByte(b[:hi], '\n') + 1
			if lo == 0 && from > 0 { // b[:hi] may start mid-line
				break
			}
			switch markOf(b[lo:hi]) {
			case claude.MarkPrompt:
				start = from + int64(lo)
				if items >= tailItems {
					return start, nil
				}
			case claude.MarkCompact:
				if items > 0 {
					if start < 0 {
						// why: with no prompt after it, the boundary leads the tail, so its divider, summary and /compact read as one.
						start = from + int64(lo)
					}
					return start, nil
				}
			case claude.MarkItem:
				items++
			}
			hi = lo - 1
		}
		switch {
		case from == 0:
			return 0, nil
		case start >= 0:
			return start, nil
		case items > 0:
			return from + int64(hi) + 1, nil // the first whole line
		}
	}
}

// page is the history before cursor before, a ChatDeltas.Older, read as a
// chat of its own, and the cursor of the history before it: its newest
// items up to snapBytes, as a snapshot holds. A cursor inside the tail,
// where its window cut it, pages into the tail. It holds no lock while it
// reads: the reach it reads is as small as a tail's.
func (pc *paneChat) page(before int64) (session.Snapshot, int64, error) {
	pc.mu.Lock()
	ok := before <= pc.off
	pc.mu.Unlock()
	if !ok {
		return session.Snapshot{}, 0, chatError{"not-found", "the chat moved on; read it again"}
	}
	f, err := os.Open(pc.path)
	if err != nil {
		return session.Snapshot{}, 0, err
	}
	defer f.Close()
	start, err := tailStart(f, before, markOf(pc.prov))
	if err != nil {
		return session.Snapshot{}, 0, err
	}
	s, at, err := pc.reduceRange(f, start, before)
	if err != nil {
		return session.Snapshot{}, 0, err
	}
	pc.mu.Lock()
	defer pc.mu.Unlock()
	// A turn the tail holds its prompt of, cut before its end, is the tail's.
	s.Turns = slices.DeleteFunc(s.Turns, func(t session.TurnSummary) bool {
		return slices.ContainsFunc(pc.snap.Turns, func(u session.TurnSummary) bool { return u.ID == t.ID })
	})
	from, older := paneWindow(s.Content, at, start)
	s.Content = s.Content[from:]
	if pc.pages == nil {
		pc.pages = map[string][2]int64{}
	}
	for _, it := range s.Content {
		pc.pages[it.ID] = [2]int64{start, before}
	}
	return s, older, nil
}

// reduceRange is the transcript's bytes from from to to read as a chat of
// their own, its last turn ended, and the offsets of its items' lines.
func (pc *paneChat) reduceRange(f io.ReaderAt, from, to int64) (session.Snapshot, []int64, error) {
	b := make([]byte, to-from)
	n, err := f.ReadAt(b, from)
	if err != nil && err != io.EOF {
		return session.Snapshot{}, nil, err
	}
	p := &paneChat{id: pc.id, off: from, prov: pc.prov, tr: pc.transcript(), snap: session.Snapshot{SessionID: pc.id, Provider: pc.prov}}
	p.lines(b[:n], "") // a transcript that shrank since gives fewer
	// The turns a rewind left, when the prompt that branched off comes after to.
	p.cutAfter(pc.nextParent(f, to))
	p.reduce(p.tr.End(session.TurnCompleted))
	return p.snap, p.at, nil
}

// item is item id whole: from the chat's tail, else read again from the
// page it came in.
func (pc *paneChat) item(id string) (session.Item, error) {
	pc.mu.Lock()
	i := slices.IndexFunc(pc.snap.Content, func(it session.Item) bool { return it.ID == id })
	if i >= 0 {
		defer pc.mu.Unlock()
		return pc.snap.Content[i], nil
	}
	r, ok := pc.pages[id]
	pc.mu.Unlock()
	if ok {
		f, err := os.Open(pc.path)
		if err != nil {
			return session.Item{}, err
		}
		defer f.Close()
		s, _, err := pc.reduceRange(f, r[0], r[1])
		if err != nil {
			return session.Item{}, err
		}
		if i := slices.IndexFunc(s.Content, func(it session.Item) bool { return it.ID == id }); i >= 0 {
			return s.Content[i], nil
		}
	}
	return session.Item{}, chatError{"not-found", "item " + id + " is gone from the chat"}
}

// fileLines are the whole lines file path gained since *off, which moves
// past them: a subagent's transcript, read whole.
func (pc *paneChat) fileLines(path string, off *int64) [][]byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	var b []byte
	if _, err = f.Seek(*off, io.SeekStart); err == nil {
		b, err = io.ReadAll(f)
	}
	f.Close()
	if err != nil {
		log.Printf("pane chat %s: transcript: %q", pc.id, err)
	}
	end := bytes.LastIndexByte(b, '\n')
	if end < 0 {
		return nil
	}
	*off += int64(end + 1)
	return bytes.Split(b[:end], []byte("\n"))
}

// subagents reads what the subagents' transcripts gained: Claude Code
// writes each to subagents/agent-<id>.jsonl beside the session's, and
// names its tool call in agent-<id>.meta.json. A subagent that ended is
// read to its end once, so a chat opened after it still shows its calls.
func (pc *paneChat) subagents() {
	calls := pc.tr.Subagents()
	if len(calls) == 0 {
		return
	}
	if pc.agents == nil {
		pc.agents = map[string]*agentFile{}
	}
	missing := false
	for c := range calls {
		missing = missing || pc.agents[c] == nil
	}
	// shortcut: a subagent with no file of its own (an older Claude Code
	// kept its lines in the session's) rescans the folder at most every 2s.
	if missing && time.Since(pc.scanned) > 2*time.Second {
		pc.scanned = time.Now()
		metas, _ := filepath.Glob(filepath.Join(strings.TrimSuffix(pc.path, ".jsonl"), "subagents", "agent-*.meta.json"))
		for _, m := range metas {
			var meta struct {
				ToolUseID string `json:"toolUseId"`
			}
			if b, err := os.ReadFile(m); err == nil && json.Unmarshal(b, &meta) == nil && meta.ToolUseID != "" && pc.agents[meta.ToolUseID] == nil {
				pc.agents[meta.ToolUseID] = &agentFile{path: strings.TrimSuffix(m, ".meta.json") + ".jsonl"}
			}
		}
	}
	for call, running := range calls {
		a := pc.agents[call]
		if a == nil || a.done {
			continue
		}
		for _, line := range pc.fileLines(a.path, &a.off) {
			pc.reduce(pc.tr.Subagent(call, line))
		}
		a.done = !running
	}
}

// settings puts the model and effort the TUI shows in the snapshot as
// they change; while it has shown no effort, the effort in Claude Code's
// user settings stands in. Callers hold pc.mu.
func (pc *paneChat) settings(l paneLive) {
	set := map[string]any{}
	if m := l.tui.model; m != "" && m != pc.tui.model {
		set["model"] = m
	}
	eff := l.tui.effort
	if eff == "" && l.conf != "" {
		if pc.conf == nil {
			_, e := claudeSettingsFile(filepath.Join(l.conf, "settings.json"))
			pc.conf = &e
		}
		eff = *pc.conf
	}
	if eff != "" && (pc.snap.Effort == nil || *pc.snap.Effort != eff) {
		set["effort"] = eff
	}
	pc.tui = l.tui
	if len(set) > 0 {
		b, _ := json.Marshal(set)
		pc.commit([]session.Delta{{Type: session.DeltaSessionPatch, Set: b}}, session.Stamp(time.Now()))
	}
	if pc.ctx == 0 {
		return
	}
	w := contextWindow(pc.model, l.tui.model, pc.ctx)
	if u := pc.snap.Usage; u == nil || u.ContextTokens == nil || *u.ContextTokens != pc.ctx || u.MaxTokens == nil || *u.MaxTokens != w {
		ctx := pc.ctx
		pc.reduce([]session.Event{{Type: session.EventUsage, ContextTokens: &ctx, MaxTokens: &w}})
	}
}

// contextUsage is the tokens in context at a main-thread assistant line of
// a Claude transcript, input and cache creation and cache reads, and the
// line's model; false for any other line.
func contextUsage(line []byte) (int64, string, bool) {
	var l struct {
		Type        string `json:"type"`
		IsSidechain bool   `json:"isSidechain"`
		Message     struct {
			Model string `json:"model"`
			Usage *struct {
				Input         int64 `json:"input_tokens"`
				CacheCreation int64 `json:"cache_creation_input_tokens"`
				CacheRead     int64 `json:"cache_read_input_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if !bytes.Contains(line, []byte(`"usage"`)) || json.Unmarshal(line, &l) != nil || l.Type != "assistant" || l.IsSidechain || l.Message.Usage == nil {
		return 0, "", false
	}
	u := l.Message.Usage
	t := u.Input + u.CacheCreation + u.CacheRead
	return t, l.Message.Model, t > 0
}

// contextWindow is a Claude model's context window, as Claude Code
// 2.1.330 reports it (modelUsage.contextWindow): 1M for every Claude 5
// model (Opus, Sonnet, Haiku 5.5, Fable 5.2, Opus 5), for a [1m] id and
// for a model the TUI names "… 1M"; 200K for an older model. used is the
// context in use: past 200K the window can only be the larger one.
// shortcut: a table of what Claude Code reports today; read the TUI's own total if a model's differs.
func contextWindow(model, tui string, used int64) int64 {
	m := strings.ToLower(model)
	if used > 200_000 || strings.HasSuffix(m, "[1m]") || strings.HasSuffix(tui, " 1M") {
		return 1_000_000
	}
	if g := claudeGeneration(m); g > 0 && g < 5 {
		return 200_000
	}
	if g := tuiGeneration(tui); g > 0 && g < 5 {
		return 200_000
	}
	return 1_000_000
}

var (
	claudeGen = regexp.MustCompile(`^claude-(?:[a-z]+-)?([0-9]+)`)
	tuiGen    = regexp.MustCompile(`\b([0-9]+)(?:\.[0-9]+)?\b`)
)

// claudeGeneration is the major version in a Claude model id, as 5 in
// claude-opus-5-5 or 3 in claude-3-5-sonnet-20241022; 0 when it has none.
func claudeGeneration(id string) int {
	m := claudeGen.FindStringSubmatch(id)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// tuiGeneration is the major version in the model name Claude Code's
// footer shows, as 4 in "Opus 4.6"; 0 when it has none.
func tuiGeneration(name string) int {
	m := tuiGen.FindStringSubmatch(name)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// turnStart is when the running turn started, zero for none.
func (pc *paneChat) turnStart() time.Time {
	for i := len(pc.snap.Turns) - 1; i >= 0; i-- {
		if t := pc.snap.Turns[i]; t.State == session.TurnRunning {
			at, _ := time.Parse(time.RFC3339Nano, t.StartedAt)
			return at
		}
	}
	return time.Time{}
}

// reduce commits events, each as one revision, as the chat loop does
// without its coalescing. Callers hold pc.mu.
func (pc *paneChat) reduce(es []session.Event) {
	for _, e := range es {
		if e.CreatedAt == "" {
			e.CreatedAt = session.Stamp(time.Now())
		}
		pc.commit(session.Reduce(pc.snap, e), e.CreatedAt)
	}
}

// commit numbers ds as one revision stamped at stamp and applies them.
// Callers hold pc.mu.
func (pc *paneChat) commit(ds []session.Delta, stamp string) {
	rev := pc.snap.Revision + 1
	for _, dl := range ds {
		dl.Version, dl.SessionID, dl.Seq, dl.Revision, dl.UpdatedAt = session.DeltaVersion, pc.id, pc.seq+1, rev, stamp
		if dl.Snapshot != nil {
			s := *dl.Snapshot
			s.Revision, s.UpdatedAt = rev, stamp
			dl.Snapshot = &s
		}
		n, err := session.Apply(&pc.snap, pc.seq, dl)
		if err != nil {
			log.Printf("pane chat %s: %s delta: %q", pc.id, dl.Type, err)
			continue
		}
		pc.snap, pc.seq = n, dl.Seq
		pc.recent, pc.recentSize = keepRecent(pc.recent, pc.recentSize, dl, 0)
	}
	pc.at = pc.at[:min(len(pc.at), len(pc.snap.Content))]
	for len(pc.at) < len(pc.snap.Content) {
		pc.at = append(pc.at, pc.cur)
	}
}

// read is the deltas after seq after, or the snapshot when those are gone
// or too big, as chat.read answers. Callers hold pc.mu.
func (pc *paneChat) read(after int64) proto.ChatDeltas {
	out := proto.ChatDeltas{Chat: pc.id, Seq: pc.seq}
	kept := pc.recent[max(0, len(pc.recent)-recentDeltas):]
	first := pc.seq - int64(len(kept))
	if after > 0 && after >= first && after <= pc.seq {
		if ds, ok := deltaJSON(kept[after-first:]); ok {
			out.Seq, out.Deltas = after, ds
			return out
		}
	}
	from, older := paneWindow(pc.snap.Content, pc.at, pc.start)
	out.Snapshot, out.Older = wireSnapshot(pc.snap, from), older
	return out
}

// paneWindow is the first of items, the transcript's lines from offset
// start on with at their lines' offsets (paneChat.at), that a snapshot or
// page holds (window), and the cursor of the history before it: start
// when it holds them all, else its first item's line. A line's items stay
// together.
func paneWindow(items []session.Item, at []int64, start int64) (int, int64) {
	from := window(items, len(items))
	if len(at) != len(items) {
		return 0, start
	}
	for from > 0 && at[from-1] == at[from] {
		from--
	}
	if from == 0 {
		return 0, start
	}
	return from, at[from]
}

// imageToken is a composer's [Image #N] token and the space after it.
var imageToken = regexp.MustCompile(`\[Image #[0-9]+\] ?`)

// paneSend types m into pc's pane: pasted text and image paths, with
// Enter. Mid-turn, it waits in the chat's queue (chatQueue) for the turn
// to end; one sent "now" goes in at once, and Claude Code takes it into
// the running turn. A send lets a held queue go.
func (d *Daemon) paneSend(pc *paneChat, l paneLive, m proto.ChatSend) (string, error) {
	// why: Claude Code's dialog for these needs Esc and writes nothing the chat could show.
	if dialogCommand(m) && pc.dialog(l, strings.TrimSpace(m.Text)) {
		return "", nil
	}
	sm, quoted, err := gather(l.cwd, session.Message{Text: m.Text, Attachments: m.Attachments})
	if err != nil {
		return "", err
	}
	// why: Claude Code numbers each pasted image path itself, and the TUI takes attachments' texts as pastes, not aide's evidence line.
	sm.Text = strings.TrimSpace(imageToken.ReplaceAllString(m.Text, ""))
	var texts []string
	for _, q := range quoted {
		switch {
		case q.Kind == "workspace-file":
			texts = append(texts, q.Path+":\n"+q.Content)
		case q.Content != "":
			texts = append(texts, q.Content)
		}
	}
	if m.Skill != nil {
		sm.Text = "/" + m.Skill.Name + " " + sm.Text
	}
	var paths []string
	for _, img := range sm.Images {
		r := d.pasteImage(proto.PasteImage{Pane: pc.id, Data: img.Data})
		if r.Err != "" {
			return "", errors.New(r.Err)
		}
		paths = append(paths, r.Path)
	}
	d.mu.Lock()
	d.chatHold(pc.id, false)
	d.mu.Unlock()
	if !busy(l.act.State) || l.act.Background || m.How == "now" || m.How == "steer" {
		pc.mu.Lock()
		pc.catchUp(l) // the echo goes once a prompt after the last one read lands
		pc.echoPrompt(sm.Text)
		pc.mu.Unlock()
		_, err := d.phoneSendPaths(pc.id, sm.Text, paths, texts)
		if err != nil {
			pc.mu.Lock()
			pc.unecho()
			pc.mu.Unlock()
		}
		return "", err
	}
	if remote.Clean(sm.Text) == "" && len(paths) == 0 {
		return "", errors.New("the message is empty")
	}
	d.mu.Lock()
	if d.panes[pc.id] == nil {
		d.mu.Unlock()
		return "", fmt.Errorf("no running pane %s", pc.id)
	}
	d.chatEnqueue(pc.id, session.QueuedInput{ID: newUUID(), Text: m.Text, CreatedAt: session.Stamp(time.Now()),
		Attachments: nonNil(m.Attachments), Recipients: []string{}},
		letter{text: sm.Text, images: paths, quoted: texts, plain: true, window: true, owner: l.prov})
	d.mu.Unlock()
	return "queued", nil
}

// dialogCommand reports whether m is /usage, /cost or /status alone, which
// a pane chat answers itself.
func dialogCommand(m proto.ChatSend) bool {
	switch strings.TrimSpace(m.Text) {
	case "/usage", "/cost", "/status":
		return len(m.Attachments) == 0 && m.Skill == nil
	}
	return false
}

// dialog adds cmd's chip and card to pc; false when the transcript knows
// nothing to show.
func (pc *paneChat) dialog(l paneLive, cmd string) bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if pc.catchUp(l) != nil {
		return false
	}
	t, ok := pc.tr.(*claude.Transcript) // Codex's commands stay in its TUI
	if !ok {
		return false
	}
	es := t.Dialog(cmd, time.Now())
	pc.reduce(es)
	return es != nil
}

// paneKey presses data into pane id, as the user's own key.
func (d *Daemon) paneKey(id string, p Pane, data []byte) error {
	d.noteUserKey(id, nil)
	return d.handle(context.Background(), proto.Input{Pane: id, Data: data})
}

// paneApprove answers the permission prompt or plan on screen: Allow is
// its first Yes, Allow for the session its don't-ask-again, a plan's
// Allow its manual-approve Yes, and Deny Esc, or with a reason the
// prompt's own field for one (pressFeedback).
func (d *Daemon) paneApprove(id string, l paneLive, m proto.ChatApprove) error {
	at, err := strconv.ParseInt(m.Request, 10, 64)
	if err != nil {
		return errResolved
	}
	agy := l.prov == model.ProviderAntigravity
	switch {
	case agy && m.Decision == "deny" && strings.TrimSpace(m.Reason) != "":
		return chatError{"unsupported", "Antigravity's prompt takes no reason; deny it, then send the reason as a message"}
	case agy && m.Decision == "allow-session":
		// why: agy's second option allows the command's prefix for this conversation.
		p, a, _, err := d.waiting(id, at)
		if err != nil {
			return err
		}
		if g := p.Snapshot(); agent.StateOf(a.Provider, g) != model.StatePendingApproval || !agent.AgyAlwaysOffered(g) {
			return errors.New("the pane's prompt offers no Allow for this conversation; answer it in its tab")
		}
		return d.paneKey(id, p, press("2", p.Modes()))
	case m.Decision == "deny" && strings.TrimSpace(m.Reason) != "":
		return d.pressFeedback(id, at, m.Reason)
	case m.Decision == "deny":
		p, _, _, err := d.waiting(id, at)
		if err != nil {
			return err
		}
		return d.paneKey(id, p, press(key.NameEscape, p.Modes()))
	case m.Decision != "allow" && m.Decision != "allow-session":
		return invalid("unknown decision %q: want allow, allow-session or deny", m.Decision)
	case l.act.State == model.StatePlanReady:
		return d.pressOffer(id, at, agent.ApprovePlan)
	case m.Decision == "allow-session":
		return d.pressOffer(id, at, agent.AllowAlways)
	}
	return d.remoteAnswer(id, at, true)
}

// paneAnswer picks each question's answers on Claude Code's question
// pages in turn: an option by its label, else Other with the text.
func (d *Daemon) paneAnswer(id string, l paneLive, m proto.ChatAnswer) error {
	pc, _, ok := d.paneChatOf(id)
	if !ok {
		return errResolved
	}
	pc.mu.Lock()
	var qs []session.Question
	for _, in := range pc.snap.PendingUserInputs {
		if in.ID == m.Request {
			qs = in.Questions
		}
	}
	pc.mu.Unlock()
	if qs == nil {
		return errResolved
	}
	for _, sq := range qs {
		q := agent.ReadQuestion(model.ProviderClaude, l.p.Snapshot())
		if q == nil {
			return errors.New("Claude Code shows no question to answer")
		}
		for _, v := range m.Answers[sq.ID] {
			i := slices.IndexFunc(q.Options, func(o agent.QuestionOption) bool { return o.Label == v })
			text := ""
			if i < 0 {
				i, text = -2, v
			}
			if err := d.phoneQuestion(id, q.ID, i, text); err != nil {
				return err
			}
		}
		if q.Multi {
			if err := d.phoneQuestion(id, q.ID, -1, ""); err != nil {
				return err
			}
		}
		// The next page draws after the answer.
		for deadline := time.Now().Add(menuWait.get()); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if n := agent.ReadQuestion(model.ProviderClaude, l.p.Snapshot()); n == nil || n.ID != q.ID {
				break
			}
		}
	}
	return nil
}

// paneSet sets the model or effort through Claude Code's own pickers, for
// this session only, and the mode through Shift+Tab (paneMode).
func (d *Daemon) paneSet(id string, l paneLive, m proto.ChatSet) error {
	switch l.prov {
	case model.ProviderCodex:
		return chatError{"unsupported", "set Codex's model, effort and mode in its terminal (Alt+T)"}
	case model.ProviderAntigravity:
		return d.agySet(id, l.p, m)
	}
	if m.Provider != "" && m.Provider != model.ProviderClaude {
		return chatError{"unsupported", "this chat is Claude Code in a pane; it cannot switch provider"}
	}
	if m.Model != "" {
		if err := d.phoneCommand(id, "/model "+m.Model); err != nil {
			return err
		}
	}
	if m.Effort != "" {
		if err := d.phoneCommand(id, "/effort "+m.Effort); err != nil {
			return err
		}
	}
	if m.Mode != "" {
		return d.paneMode(id, l.p, m.Mode, agent.ClaudeMode)
	}
	return nil
}

// paneMode presses Shift+Tab in pane id until the TUI's footer reads mode,
// each press waiting for the footer to move: the TUI's own cycle decides
// the order, bypass included when the session opted in. With no footer to
// read, as while a prompt covers it, it presses once.
func (d *Daemon) paneMode(id string, p Pane, mode string, read func(vt.Grid) string) error {
	defer d.paneCommandLock(id)()
	shiftTab := input.Key(key.Event{Name: key.NameTab, Modifiers: key.ModShift, State: key.Press}, p.Modes())
	cur := read(p.Snapshot())
	if cur == "" {
		return d.paneKey(id, p, shiftTab)
	}
	for seen := map[string]bool{}; cur != mode; {
		if seen[cur] {
			return chatError{"unsupported", "Shift+Tab in this session never reaches " + mode}
		}
		seen[cur] = true
		if err := d.paneKey(id, p, shiftTab); err != nil {
			return err
		}
		prev := cur
		for deadline := time.Now().Add(menuWait.get()); cur == prev && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
			if cur = read(p.Snapshot()); cur == "" {
				cur = prev
			}
		}
		if cur == prev {
			return errors.New("the agent's mode did not change")
		}
	}
	return nil
}
