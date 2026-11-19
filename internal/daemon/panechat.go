package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// A pane chat is the desktop chat of a Claude Code TUI running in a pane,
// whatever started it: the chat id is the pane's id. Nothing starts or
// stops claude for it. Its transcript, read as it grows, and the pane's
// hooks and screen make the snapshot; what the chat sends goes into the
// PTY as keys, as the phone's do. It lasts while the pane's hooks name
// Claude and its transcript; then the window shows the pane again.

// paneChat is one pane chat's snapshot and how far it read the transcript.
type paneChat struct {
	mu     sync.Mutex
	id     string
	path   string // the transcript
	off    int64  // bytes of it read, up to the last whole line
	tr     *claude.Transcript
	snap   session.Snapshot
	seq    int64
	recent []session.Delta
	// queued are the messages the chat typed mid-turn, which Claude Code
	// holds until the turn ends; the next prompt line takes them.
	queued []session.QueuedInput
	footer bool // the TUI's footer gave the mode at least once
}

// paneLive is what a pane chat's catch-up reads of its pane under d.mu.
type paneLive struct {
	p    Pane
	path string
	cwd  string
	mode string         // the permission mode its hooks reported (Pane.AgentMode)
	act  model.Activity // zero State when the pane has none
}

// paneChatOf is pane id's chat, made on first use, and its pane's state;
// false when the pane runs no Claude Code with a transcript.
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
	l := paneLive{p: d.panes[id], path: p.Transcript, cwd: p.Cwd, mode: p.AgentMode}
	if ai := d.activityIndex(id); ai >= 0 {
		l.act = d.st.Activities[ai]
	}
	pc := d.paneChats[id]
	if pc == nil || pc.path != l.path { // a new session (claude again, /clear, /resume) starts over
		pc = &paneChat{id: id, path: l.path, tr: claude.NewTranscript(),
			snap: session.Snapshot{SessionID: id, PaneID: id, WorkspaceID: p.WorkspaceID, Provider: model.ProviderClaude,
				SessionState: session.StateReady, Running: true, PermissionMode: "default", InteractionMode: "build"}}
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
	default:
		return nil, false
	}
	pc, l, ok := d.paneChatOf(id)
	if !ok {
		return nil, false
	}
	if r, ok := m.(proto.ChatRead); ok {
		pc.mu.Lock()
		defer pc.mu.Unlock()
		pc.catchUp(l)
		return pc.read(r.After), true
	}
	var note string
	var err error
	switch m := m.(type) {
	case proto.ChatSend:
		note, err = d.paneSend(pc, l, m)
	case proto.ChatInterrupt:
		if busy(l.act.State) {
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
		err = chatError{"unsupported", "Claude Code holds a pane's queue; edit it in the terminal view"}
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
// request and the mode in line with the pane. Callers hold pc.mu.
func (pc *paneChat) catchUp(l paneLive) {
	if f, err := os.Open(pc.path); err == nil {
		var b []byte
		if _, err = f.Seek(pc.off, io.SeekStart); err == nil {
			b, err = io.ReadAll(f)
		}
		f.Close()
		if err != nil {
			log.Printf("pane chat %s: transcript: %q", pc.id, err)
		}
		if end := bytes.LastIndexByte(b, '\n'); end >= 0 {
			for _, line := range bytes.Split(b[:end], []byte("\n")) {
				es := pc.tr.Line(line)
				if slices.ContainsFunc(es, func(e session.Event) bool { return e.Type == session.EventTurnStarted }) {
					pc.queued = nil
				}
				pc.reduce(es)
			}
			pc.off += int64(end + 1)
		}
	}
	a := l.act
	switch {
	case waits(a.State) && (a.State != model.StateAwaitingInput || agent.ReadQuestion(model.ProviderClaude, l.p.Snapshot()) != nil):
		pc.reduce(pc.tr.Ask(strconv.FormatInt(a.UpdatedAt.UnixNano(), 10), a.Detail))
	default:
		pc.reduce(pc.tr.Resolve())
	}
	// Claude Code marks a turn's end in the transcript; a hook saying the
	// agent stopped after the turn began ends one it did not mark.
	if pc.tr.Running() && a.State != "" && !busy(a.State) && a.UpdatedAt.After(pc.turnStart()) {
		pc.reduce(pc.tr.End(session.TurnCompleted))
	}
	// The footer says the mode the TUI runs in now; the hooks' mode stands
	// in only until it does, as AgentMode keeps bypass across Shift+Tab.
	mode := agent.ClaudeMode(l.p.Snapshot())
	if mode != "" {
		pc.footer = true
	} else if !pc.footer {
		mode = l.mode
	}
	if mode != "" && mode != pc.snap.PermissionMode {
		pc.reduce([]session.Event{{Type: session.EventSettings, Mode: mode}})
	}
	if a.State != "" && !busy(a.State) && !pc.tr.Running() {
		pc.queued = nil // the agent stopped without taking them: an interrupt dropped them back in its box
	}
	if !slices.EqualFunc(pc.queued, pc.snap.QueuedInputs, func(a, b session.QueuedInput) bool { return a.ID == b.ID }) {
		set, _ := json.Marshal(map[string]any{"queuedInputs": append([]session.QueuedInput{}, pc.queued...)})
		pc.commit([]session.Delta{{Type: session.DeltaSessionPatch, Set: set}}, session.Stamp(time.Now()))
	}
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
		pc.recent = append(pc.recent, dl)
	}
	if len(pc.recent) > 2*recentDeltas {
		pc.recent = slices.Clone(pc.recent[len(pc.recent)-recentDeltas:])
	}
}

// read is the deltas after seq after, or the snapshot when those are gone,
// as chat.read answers. Callers hold pc.mu.
func (pc *paneChat) read(after int64) proto.ChatDeltas {
	out := proto.ChatDeltas{Chat: pc.id, Seq: pc.seq}
	kept := pc.recent[max(0, len(pc.recent)-recentDeltas):]
	first := pc.seq - int64(len(kept))
	if after > 0 && after >= first && after <= pc.seq {
		out.Seq = after
		for _, dl := range kept[after-first:] {
			b, _ := json.Marshal(dl)
			out.Deltas = append(out.Deltas, b)
		}
		return out
	}
	out.Snapshot, _ = json.Marshal(pc.snap)
	return out
}

// paneSend types m into pc's pane: pasted text and image paths, with
// Enter, mid-turn too, where Claude Code queues it and so does the chat
// until the next prompt line.
func (d *Daemon) paneSend(pc *paneChat, l paneLive, m proto.ChatSend) (string, error) {
	sm, err := provide(l.cwd, session.Message{Text: m.Text, Attachments: m.Attachments})
	if err != nil {
		return "", err
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
	if _, err := d.phoneSendPaths(pc.id, sm.Text, paths); err != nil {
		return "", err
	}
	if !busy(l.act.State) {
		return "", nil
	}
	pc.mu.Lock()
	pc.queued = append(pc.queued, session.QueuedInput{ID: newUUID(), Text: m.Text, CreatedAt: session.Stamp(time.Now()),
		Attachments: nonNil(m.Attachments), Recipients: []string{}})
	pc.mu.Unlock()
	return "queued", nil
}

// paneKey presses data into pane id, as the user's own key.
func (d *Daemon) paneKey(id string, p Pane, data []byte) error {
	d.noteUserKey(id, nil)
	return d.handle(context.Background(), proto.Input{Pane: id, Data: data})
}

// paneApprove answers the permission prompt or plan on screen: Allow is
// its first Yes, Allow for the session its don't-ask-again, a plan's
// Allow its manual-approve Yes, and Deny Esc.
func (d *Daemon) paneApprove(id string, l paneLive, m proto.ChatApprove) error {
	at, err := strconv.ParseInt(m.Request, 10, 64)
	if err != nil {
		return errResolved
	}
	switch {
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
// this session only. A mode request presses Shift+Tab once, whatever mode
// it names: the TUI moves to the next of its own modes, bypass included
// when the session opted in, and the footer reports where it went.
func (d *Daemon) paneSet(id string, l paneLive, m proto.ChatSet) error {
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
		return d.paneKey(id, l.p, input.Key(key.Event{Name: key.NameTab, Modifiers: key.ModShift, State: key.Press}, l.p.Modes()))
	}
	return nil
}
