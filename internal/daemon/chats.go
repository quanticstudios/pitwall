package daemon

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/errlog"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/store"
)

// Chats: managed sessions the daemon drives through a session.Driver
// (docs/managed-sessions.md). Each chat has one loop goroutine, the only one
// that touches its config, snapshot and driver; commands reach it through
// do, and the driver's events wait in a queue the loop drains, so a driver
// that emits while a command waits on it never blocks.
//
// shortcut: chats live in memory only; a daemon restart drops them and
// their tabs. ENG-2680's event log makes them last.

// recentDeltas is how many deltas a chat keeps for ChatRead After.
const recentDeltas = 1024

// chatClose bounds Driver.Close when a chat or the daemon closes.
const chatClose = 5 * time.Second

type chat struct {
	id    string
	prov  session.Provider
	title string // the first prompt's title, for the tab; guarded by Daemon.mu

	cmds chan func()
	quit chan struct{}
	wake chan struct{}
	evMu sync.Mutex
	evs  []session.Event

	// Owned by the loop.
	cfg    session.Config
	snap   session.Snapshot
	seq    int64
	drv    session.Driver
	recent []session.Delta
	sum    model.Chat // the summary last put in State.Chats
	// limit is a plan limit that refused the running turn; its end
	// schedules the limit resume.
	limit *session.Limit
	// resumed marks the running turn as the limit resume's continue.
	resumed bool
}

// chatError is a command's failure with its ChatResult code.
type chatError struct{ code, msg string }

func (e chatError) Error() string { return e.msg }

var errResolved = chatError{"conflict", "the request is no longer pending: someone answered first, or it expired"}

func invalid(format string, a ...any) error { return chatError{"invalid", fmt.Sprintf(format, a...)} }

// chatCode is err's ChatResult code.
func chatCode(err error) string {
	var ce chatError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &ce):
		return ce.code
	case errors.Is(err, session.ErrBusy):
		return "busy"
	case errors.Is(err, session.ErrUnsupported):
		return "unsupported"
	}
	return "provider"
}

func chatResult(id, turn, note string, err error) proto.ChatResult {
	r := proto.ChatResult{Chat: id, Turn: turn, Code: chatCode(err), Message: note}
	if err != nil {
		r.Message = err.Error()
	}
	return r
}

// chatRequest handles a Chat* message and returns the reply for its client
// alone; false for any other message.
func (d *Daemon) chatRequest(ctx context.Context, m any) (any, bool) {
	var id string
	var run func(*chat) (turn, note string, err error)
	switch m := m.(type) {
	case proto.ChatNew:
		return d.chatNew(ctx, m), true
	case proto.ChatArchive:
		return chatResult(m.Chat, "", "", d.chatArchive(m)), true
	case proto.ChatRead:
		c, err := d.chat(m.Chat)
		if err != nil {
			return chatResult(m.Chat, "", "", err), true
		}
		var out proto.ChatDeltas
		if !c.do(func() { out = c.read(m.After) }) {
			return chatResult(m.Chat, "", "", chatError{"not-found", "chat " + m.Chat + " is closed"}), true
		}
		return out, true
	case proto.ChatSend:
		id = m.Chat
		run = func(c *chat) (string, string, error) {
			if len(m.Images) > 0 {
				return "", "", chatError{"unsupported", "images are not supported yet"}
			}
			return d.chatSend(ctx, c, session.Message{ID: newUUID(), Text: m.Text}, m.How)
		}
	case proto.ChatInterrupt:
		id = m.Chat
		run = func(c *chat) (string, string, error) {
			c.cfg.Held = true
			if c.drv == nil {
				return "", "", nil
			}
			return "", "", c.drv.Interrupt(ctx)
		}
	case proto.ChatApprove:
		id = m.Chat
		run = func(c *chat) (string, string, error) { return "", "", d.chatApprove(ctx, c, m) }
	case proto.ChatAnswer:
		id = m.Chat
		run = func(c *chat) (string, string, error) { return "", "", d.chatAnswer(ctx, c, m) }
	case proto.ChatSet:
		id = m.Chat
		run = func(c *chat) (string, string, error) { return "", "", c.set(ctx, m) }
	case proto.ChatQueue:
		id = m.Chat
		run = func(c *chat) (string, string, error) {
			for _, q := range m.Queue {
				if !slices.ContainsFunc(c.cfg.Queue, func(o session.QueuedMessage) bool { return o.ID == q.ID }) {
					return "", "", invalid("no queued message %s", q.ID)
				}
			}
			c.cfg.Queue = slices.Clone(m.Queue)
			d.chatNext(ctx, c)
			return "", "", nil
		}
	default:
		return nil, false
	}
	c, err := d.chat(id)
	if err != nil {
		return chatResult(id, "", "", err), true
	}
	var turn, note string
	if !c.do(func() { turn, note, err = run(c); d.chatChanged(c) }) {
		err = chatError{"not-found", "chat " + id + " is closed"}
	}
	return chatResult(c.id, turn, note, err), true
}

// chat is the live chat id; callers do not hold d.mu.
func (d *Daemon) chat(id string) (*chat, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c := d.chats[id]; c != nil {
		return c, nil
	}
	return nil, chatError{"not-found", "no chat " + id}
}

// chatNew opens the tab, as a Task's (startTask), with no pane, starts the
// provider and sends the prompt.
func (d *Daemon) chatNew(ctx context.Context, m proto.ChatNew) proto.ChatResult {
	m.Provider = cmp.Or(m.Provider, model.ProviderClaude)
	prov := d.o.Providers[m.Provider]
	if prov == nil {
		return chatResult("", "", "", chatError{"unsupported", fmt.Sprintf("pitwall cannot run %s chats", m.Provider)})
	}
	if len(m.Images) > 0 {
		return chatResult("", "", "", chatError{"unsupported", "images are not supported yet"})
	}
	dir := m.Dir
	var root, note string
	var ports func(*model.Workspace)
	if m.Worktree != "" || m.Branch != "" {
		var ok bool
		if root, ok = d.o.RepoRoot(ctx, dir); !ok {
			return chatResult("", "", "", invalid("%s is not in a git repository", dir))
		}
		from := model.WorktreeFrom{Ref: m.Base}
		if m.Branch != "" {
			from = model.WorktreeFrom{Kind: model.FromBranch, Ref: m.Branch}
		}
		path, _, wt, problems, err := d.worktree(ctx, root, m.Worktree, from)
		if err != nil {
			return chatResult("", "", "", invalid("%v", err))
		}
		if wt.Setup != "" {
			// shortcut: a chat tab has no shell to type setup into, so it does not run; add a runner and the repo's ask (docs/managed-sessions.md, attach).
			problems = append(problems, errors.New("setup not run: chats do not run worktree setup yet"))
		}
		if len(problems) > 0 {
			note = fmt.Sprintf("worktree %s is ready, but: %v", path, errors.Join(problems...))
		}
		dir = path
		ports = func(w *model.Workspace) {
			w.WorktreeRoot, w.Ports = root, portBlock(d.st.Workspaces, wt.PortBase, wt.PortStep)
		}
	}
	c := &chat{id: newUUID(), prov: prov, title: promptTitle(m.Prompt), cmds: make(chan func()), quit: make(chan struct{}), wake: make(chan struct{}, 1)}
	err := d.addSession(ctx, proto.NewSession{Name: m.Name, Cwd: dir, SessionID: m.SessionID, GroupID: m.GroupID, ParentPane: m.ParentPane}, "", nil, func(w *model.Workspace) {
		if ports != nil {
			ports(w)
		}
		w.Chat = c.id
		c.cfg = session.Config{Format: 1, ID: c.id, Provider: m.Provider, Workspace: w.ID, Cwd: w.Path, Model: m.Model, Effort: m.Effort, Mode: m.Mode, Created: time.Now(),
			Env: append(portEnv(w.Ports), "PITWALL_PANE="+c.id)}
		c.sum = model.Chat{ID: c.id, WorkspaceID: w.ID, Provider: m.Provider, Model: m.Model, Effort: m.Effort, Mode: m.Mode, Title: c.title, State: session.StateReady, UpdatedAt: time.Now()}
		d.chats[c.id] = c
		d.st.Chats = append(d.st.Chats, c.sum)
		go c.loop(d)
	})
	if err != nil {
		return chatResult("", "", "", invalid("%v", err))
	}
	var turn string
	c.do(func() {
		stamp := session.Stamp(time.Now())
		s := session.Snapshot{
			SessionID: c.id, PaneID: c.id, WorkspaceID: c.cfg.Workspace, Provider: c.cfg.Provider,
			SessionState: session.StateReady, PermissionMode: cmp.Or(m.Mode, "default"), InteractionMode: "build",
			Model: optional(m.Model), Effort: optional(m.Effort), UpdatedAt: stamp,
		}
		c.commit([]session.Delta{{Type: session.DeltaSnapshot, Snapshot: &s}}, stamp)
		if err = d.chatStart(c); err == nil && m.Prompt != "" {
			turn, _, err = d.chatSend(ctx, c, session.Message{ID: newUUID(), Text: m.Prompt}, "")
		}
		d.chatChanged(c)
	})
	return chatResult(c.id, turn, note, err)
}

// chatArchive closes the chat's tab as CloseTab does, which closes the
// chat (removeWorkspace). shortcut: archived chats are not kept until
// ENG-2680 stores them, so Delete changes nothing.
func (d *Daemon) chatArchive(m proto.ChatArchive) error {
	c, err := d.chat(m.Chat)
	if err != nil {
		return err
	}
	if err := d.killSession(proto.KillSession{WorkspaceID: c.cfg.Workspace}); err != nil {
		return chatError{"conflict", err.Error()}
	}
	return nil
}

// dropChat forgets a chat and closes it in the background. Callers hold
// d.mu.
func (d *Daemon) dropChat(id string) {
	c := d.chats[id]
	delete(d.chats, id)
	d.st.Chats = slices.DeleteFunc(d.st.Chats, func(s model.Chat) bool { return s.ID == id })
	d.dropPane(id) // its activity, attention, resume and decisions, as a pane's
	if c != nil {
		go c.close()
	}
}

// close ends the driver and the loop. It runs once per chat.
func (c *chat) close() {
	ctx, cancel := context.WithTimeout(context.Background(), chatClose)
	defer cancel()
	c.do(func() {
		if c.drv != nil {
			if err := c.drv.Close(ctx); err != nil {
				log.Printf("chat %s: close: %q", c.id, err)
			}
			c.drv = nil
		}
	})
	close(c.quit)
}

func (c *chat) loop(d *Daemon) {
	for {
		select {
		case f := <-c.cmds:
			f()
		case <-c.wake:
			c.evMu.Lock()
			evs := c.evs
			c.evs = nil
			c.evMu.Unlock()
			for _, e := range evs {
				d.chatEvent(c, e)
			}
			d.chatChanged(c)
		case <-c.quit:
			return
		}
	}
}

// do runs f on c's loop and waits for it; false when the chat is closed.
func (c *chat) do(f func()) bool {
	done := make(chan struct{})
	select {
	case c.cmds <- func() { f(); close(done) }:
		<-done
		return true
	case <-c.quit:
		return false
	}
}

// emit is the driver's event callback.
func (c *chat) emit(e session.Event) {
	c.evMu.Lock()
	c.evs = append(c.evs, e)
	c.evMu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (d *Daemon) chatEvent(c *chat, e session.Event) {
	switch e.Type {
	case session.EventResumeID:
		c.cfg.Resume = e.ResumeID
		return
	case session.EventLimit:
		d.chatLimit(c, *e.Limit)
		return
	case session.EventTurnCompleted:
		if e.Point != "" {
			if c.cfg.Points == nil {
				c.cfg.Points = map[string]string{}
			}
			c.cfg.Points[e.TurnID] = e.Point
		}
	case session.EventExited:
		c.drv = nil // the next command starts it again with Origin{Resume}
	}
	c.commit(session.Reduce(c.snap, e), e.CreatedAt)
	d.chatNext(context.Background(), c)
}

// commit numbers ds as one revision stamped at stamp and applies them.
func (c *chat) commit(ds []session.Delta, stamp string) {
	rev := c.snap.Revision + 1
	for _, dl := range ds {
		dl.Version, dl.SessionID, dl.Seq, dl.Revision, dl.UpdatedAt = session.DeltaVersion, c.id, c.seq+1, rev, stamp
		if dl.Snapshot != nil {
			s := *dl.Snapshot
			s.Revision, s.UpdatedAt = rev, stamp
			dl.Snapshot = &s
		}
		n, err := session.Apply(&c.snap, c.seq, dl)
		if err != nil {
			log.Printf("chat %s: %s delta: %q", c.id, dl.Type, err)
			continue
		}
		c.snap, c.seq = n, dl.Seq
		c.recent = append(c.recent, dl)
		if len(c.recent) > 2*recentDeltas {
			c.recent = slices.Clone(c.recent[len(c.recent)-recentDeltas:])
		}
	}
}

// reduce commits what event e does to c's snapshot, for the loop's own
// changes that have an event form.
func (c *chat) reduce(e session.Event) {
	e.CreatedAt = session.Stamp(time.Now())
	c.commit(session.Reduce(c.snap, e), e.CreatedAt)
}

// system adds a system line to the transcript.
func (c *chat) system(text string) {
	c.reduce(session.Event{Type: session.EventContent, ItemID: newUUID(), Kind: session.KindSystem, Text: text})
}

func (c *chat) read(after int64) proto.ChatDeltas {
	out := proto.ChatDeltas{Chat: c.id, Seq: c.seq}
	kept := c.recent[max(0, len(c.recent)-recentDeltas):]
	first := c.seq - int64(len(kept)) // the seq before the oldest kept delta
	if after > 0 && after >= first && after <= c.seq {
		out.Seq = after
		for _, dl := range kept[after-first:] {
			b, _ := json.Marshal(dl)
			out.Deltas = append(out.Deltas, b)
		}
		return out
	}
	out.Snapshot, _ = json.Marshal(c.snap)
	return out
}

// chatStart starts the provider when it is not running.
func (d *Daemon) chatStart(c *chat) error {
	if c.drv != nil {
		return nil
	}
	// The process outlives any one request: Close ends it.
	drv, err := c.prov.Start(context.Background(), c.cfg, session.Origin{Resume: c.cfg.Resume}, c.emit)
	if err != nil {
		c.reduce(session.Event{Type: session.EventError, Message: fmt.Sprintf("%s did not start: %v", c.cfg.Provider, err)})
		return err
	}
	c.drv = drv
	set, _ := json.Marshal(map[string]int{"runtimeGeneration": c.snap.RuntimeGeneration + 1})
	c.commit([]session.Delta{{Type: session.DeltaSessionPatch, Set: set}}, session.Stamp(time.Now()))
	return nil
}

// busy reports a turn running or about to: the loop sent one, the driver
// started one, or it waits on an answer.
func busy(s session.Snapshot) bool {
	return s.SessionState == session.StateRunning || s.SessionState == session.StateWaiting ||
		slices.ContainsFunc(s.Turns, func(t session.TurnSummary) bool { return t.State == session.TurnRunning })
}

// chatSend sends m as how asks: a turn when idle, else queued, or steered
// into the running turn.
func (d *Daemon) chatSend(ctx context.Context, c *chat, m session.Message, how string) (turn, note string, err error) {
	switch how {
	case "", "queue", "steer":
	default:
		return "", "", invalid("unknown send %q: want steer or queue", how)
	}
	if strings.TrimSpace(m.Text) == "" {
		return "", "", invalid("nothing to send")
	}
	c.cfg.Held = false
	if how == "steer" && busy(c.snap) && c.drv != nil {
		switch err := c.drv.Steer(ctx, m); {
		case err == nil:
			c.prompt(m, true)
			return "", "", nil
		case errors.Is(err, session.ErrUnsupported):
			note = "queued: the provider cannot steer a running turn"
		case !errors.Is(err, session.ErrIdle):
			return "", "", err
		}
	}
	if how != "" || busy(c.snap) {
		c.cfg.Queue = append(c.cfg.Queue, session.QueuedMessage{ID: m.ID, Text: m.Text})
		d.chatNext(ctx, c)
		return "", cmp.Or(note, "queued"), nil
	}
	return d.chatTurn(ctx, c, m)
}

// chatTurn starts a turn with m. The prompt joins the transcript after the
// driver took it, still before the events the driver emitted meanwhile,
// which wait for this command to end.
func (d *Daemon) chatTurn(ctx context.Context, c *chat, m session.Message) (string, string, error) {
	if err := d.chatStart(c); err != nil {
		return "", "", err
	}
	if d.o.Checkpoint != nil {
		d.mu.Lock()
		var dir string
		if w := d.workspace(c.cfg.Workspace); w != nil {
			dir = d.st.LivePath(*w)
		}
		d.mu.Unlock()
		if dir != "" {
			d.checkpointWait(dir, c.cfg.Workspace) // as a pane's prompt hook does: the turn's edits stay out of it
		}
	}
	c.resumed, c.limit = false, nil
	turn, err := c.drv.Send(ctx, m)
	if errors.Is(err, session.ErrBusy) {
		c.cfg.Queue = slices.Insert(c.cfg.Queue, 0, session.QueuedMessage{ID: m.ID, Text: m.Text})
		return "", "queued", nil
	}
	c.prompt(m, false)
	if err != nil {
		c.reduce(session.Event{Type: session.EventError, Message: "The turn did not start: " + err.Error()})
		return "", "", err
	}
	stamp := session.Stamp(time.Now())
	c.commit([]session.Delta{{Type: session.DeltaStatus, Status: "working", SessionState: session.StateRunning, Running: true, LastCompletedAt: c.snap.LastCompletedAt}}, stamp)
	return turn, "", nil
}

// prompt adds m to the transcript as a user-prompt item, aide's shape: no
// provider, and no turn until one starts, unless it steers the running
// one.
func (c *chat) prompt(m session.Message, steer bool) {
	stamp := session.Stamp(time.Now())
	it := session.Item{ID: m.ID, Kind: session.KindUserPrompt, Text: m.Text, UpdatedAt: stamp, Attachments: []session.Attachment{}}
	if i := slices.IndexFunc(c.snap.Turns, func(t session.TurnSummary) bool { return t.State == session.TurnRunning }); steer && i >= 0 {
		it.TurnID = optional(c.snap.Turns[i].ID)
	}
	c.commit([]session.Delta{{Type: session.DeltaItemAdd, Item: &it}}, stamp)
}

// chatNext starts the next queued message once the chat is idle, unless an
// interrupt holds the queue or the provider is not running.
func (d *Daemon) chatNext(ctx context.Context, c *chat) {
	if c.drv == nil || c.cfg.Held || len(c.cfg.Queue) == 0 || busy(c.snap) {
		return
	}
	q := c.cfg.Queue[0]
	c.cfg.Queue = c.cfg.Queue[1:]
	if _, _, err := d.chatTurn(ctx, c, session.Message{ID: q.ID, Text: q.Text}); err != nil {
		log.Printf("chat %s: queued message: %q", c.id, err)
	}
}

// chatApprove answers a pending approval; Request "" names the only one.
// First wins: the request leaves the snapshot before the driver hears.
func (d *Daemon) chatApprove(ctx context.Context, c *chat, m proto.ChatApprove) error {
	dec := session.Decision{Reason: m.Reason}
	switch m.Decision {
	case "allow":
		dec.Allow = true
	case "allow-session":
		dec.Allow, dec.ForSession = true, true
	case "deny":
	default:
		return invalid("unknown decision %q: want allow, allow-session or deny", m.Decision)
	}
	ids := make([]string, len(c.snap.PendingApprovals))
	for i, a := range c.snap.PendingApprovals {
		ids[i] = a.ID
	}
	id, err := pendingID(ids, m.Request, "approval")
	if err != nil {
		return err
	}
	c.reduce(session.Event{Type: session.EventRequestResolved, RequestID: id})
	return c.deliver(func() error { return c.drv.Approve(ctx, id, dec) })
}

func (d *Daemon) chatAnswer(ctx context.Context, c *chat, m proto.ChatAnswer) error {
	ids := make([]string, len(c.snap.PendingUserInputs))
	for i, in := range c.snap.PendingUserInputs {
		ids[i] = in.ID
	}
	id, err := pendingID(ids, m.Request, "input request")
	if err != nil {
		return err
	}
	c.reduce(session.Event{Type: session.EventInputResolved, RequestID: id})
	return c.deliver(func() error { return c.drv.Answer(ctx, id, m.Answers) })
}

// pendingID is want when it is pending, or the only pending id for "".
func pendingID(ids []string, want, what string) (string, error) {
	switch {
	case want != "" && slices.Contains(ids, want):
		return want, nil
	case want != "", len(ids) == 0:
		return "", errResolved
	case len(ids) > 1:
		return "", invalid("%d %ss are pending: name one of %s", len(ids), what, strings.Join(ids, ", "))
	}
	return ids[0], nil
}

// deliver runs an answer's driver call. The answer stands either way: a
// failure becomes a line in the transcript, and the provider settles the
// request when it withdraws it or the turn ends.
func (c *chat) deliver(call func() error) error {
	err := session.ErrClosed
	if c.drv != nil {
		err = call()
	}
	if err != nil {
		c.system("The answer did not reach " + string(c.cfg.Provider) + ": " + err.Error())
	}
	return err
}

// set changes the settings in the driver, when running, and the snapshot.
func (c *chat) set(ctx context.Context, m proto.ChatSet) error {
	if c.drv != nil {
		if err := c.drv.Set(ctx, session.Settings{Model: m.Model, Effort: m.Effort, Mode: m.Mode}); err != nil {
			return err
		}
	}
	patch := map[string]string{}
	for _, f := range []struct {
		value string
		cfg   *string
		key   string
	}{{m.Model, &c.cfg.Model, "model"}, {m.Effort, &c.cfg.Effort, "effort"}, {m.Mode, &c.cfg.Mode, "permissionMode"}} {
		if f.value != "" {
			*f.cfg, patch[f.key] = f.value, f.value
		}
	}
	if len(patch) == 0 {
		return invalid("nothing to set")
	}
	set, _ := json.Marshal(patch)
	c.commit([]session.Delta{{Type: session.DeltaSessionPatch, Set: set}}, session.Stamp(time.Now()))
	return nil
}

// chatChanged brings c's entry in State.Chats and its Activity up to
// date. Only a change of a field other than Seq pushes a new State, so
// streamed text does not.
func (d *Daemon) chatChanged(c *chat) {
	s := model.Chat{
		ID: c.id, WorkspaceID: c.cfg.Workspace, Provider: c.cfg.Provider,
		Model: c.cfg.Model, Effort: c.cfg.Effort, Mode: c.cfg.Mode,
		State: c.snap.SessionState, Pending: len(c.snap.PendingApprovals) + len(c.snap.PendingUserInputs),
		Queued: len(c.cfg.Queue), Turns: len(c.snap.Turns), Seq: c.seq,
		UpdatedAt: c.sum.UpdatedAt, Title: c.sum.Title,
	}
	same := s
	same.Seq = c.sum.Seq
	moved := same != c.sum
	if moved {
		s.UpdatedAt = time.Now()
	}
	c.sum = s
	d.mu.Lock()
	i := slices.IndexFunc(d.st.Chats, func(x model.Chat) bool { return x.ID == c.id })
	if i < 0 {
		d.mu.Unlock()
		return
	}
	s.Title = d.st.Chats[i].Title // retitle keeps it
	d.st.Chats[i] = s
	if moved {
		d.changed()
	}
	after := d.chatActivity(c)
	d.mu.Unlock()
	after()
}

// chatActivity brings chat c's Activity in line with its snapshot, as an
// agent pane's hooks do for its own, and returns what runs once d.mu is
// free: triage of the new state, and the limit resume of a turn a plan
// limit refused. Callers hold d.mu.
func (d *Daemon) chatActivity(c *chat) func() {
	state, detail, ok := chatState(c.snap, c.resumed)
	if !ok {
		return func() {}
	}
	secrets := d.dec.cur.Secrets
	if state == model.StateCompleted {
		detail = agent.Summary(detail, secrets...)
	} else {
		detail = decide.Redact(detail, secrets...)
	}
	i := d.activityIndex(c.id)
	// While a limit resume waits, the error limitStopped set stays, so the
	// resume still finds the turn it stopped.
	held := i >= 0 && d.lim.pending[c.id] != nil && !busy(c.snap)
	var job *decideJob
	if i < 0 || !held && (d.st.Activities[i].State != state || d.st.Activities[i].Detail != detail) {
		now := time.Now()
		a := model.Activity{PaneID: c.id, WorkspaceID: c.cfg.Workspace, Provider: c.cfg.Provider, SessionID: c.cfg.Resume, State: state, Detail: detail, UpdatedAt: now}
		if i < 0 {
			d.st.Activities = append(d.st.Activities, a)
			i = len(d.st.Activities) - 1
		} else {
			d.st.Activities[i] = a
		}
		switch state {
		case model.StateError:
			d.o.Errors.Add(errlog.Event{T: now, Pane: c.id, Provider: string(c.cfg.Provider), Kind: errlog.API, Output: detail, Cwd: c.cfg.Cwd}, secrets...)
		case model.StateCompleted:
			go d.refreshStats(context.Background(), c.cfg.Workspace)
		}
		if x := d.dec.cur; x.on() {
			job = &decideJob{pane: c.id, at: now, agent: string(c.cfg.Provider), c: d.client(), s: x.Settings}
			d.planTriage(&d.st.Activities[i], job)
			job = job.orNil()
		}
		if w := d.workspace(c.cfg.Workspace); w != nil {
			w.UpdatedAt = now
		}
		d.changed()
	}
	limit := func() {}
	if a := d.st.Activities[i]; c.limit != nil && !busy(c.snap) && stoppedState(a.State) {
		id, turn, resets := c.id, a.UpdatedAt, c.limit.Resets
		msg := cmp.Or(a.Detail, "Usage limit reached")
		c.limit = nil
		if d.resumeOn() {
			limit = func() { d.limitStopped(id, turn, msg, resets) }
		}
	}
	return func() {
		limit()
		d.runDecisions(context.Background(), job)
	}
}

// chatState is the Activity state and detail of a chat whose snapshot is
// s (docs/managed-sessions.md, Placement); ok is false before its first
// turn, which has none, as an agent pane at its first prompt. resumed
// marks a running turn as the limit resume's continue.
func chatState(s session.Snapshot, resumed bool) (state model.AgentState, detail string, ok bool) {
	if len(s.Turns) == 0 {
		return "", "", false
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	switch s.SessionState {
	case session.StateConnecting:
		return model.StateConnecting, "", true
	case session.StateWaiting:
		switch {
		case s.PlanApproval != nil:
			first, _, _ := strings.Cut(deref(s.PlanApproval.Detail), "\n")
			return model.StatePlanReady, first, true
		case len(s.PendingApprovals) > 0:
			return model.StatePendingApproval, deref(s.PendingApprovals[0].Detail), true
		case len(s.PendingUserInputs) > 0:
			in := s.PendingUserInputs[0]
			if len(in.Questions) > 0 {
				return model.StateAwaitingInput, in.Questions[0].Prompt, true
			}
			return model.StateAwaitingInput, deref(in.Detail), true
		}
		fallthrough
	case session.StateRunning:
		if resumed {
			return model.StateWorking, model.ResumedDetail, true
		}
		return model.StateWorking, "", true
	case session.StateStopped:
		return model.StateCompleted, "Interrupted", true
	case session.StateError:
		if s.Turns[len(s.Turns)-1].State == session.TurnInterrupted {
			return model.StateCompleted, "Interrupted", true
		}
		return model.StateError, deref(s.LastError), true
	}
	for i := len(s.Content) - 1; i >= 0; i-- {
		if s.Content[i].Kind == session.KindAssistantText {
			return model.StateCompleted, s.Content[i].Text, true
		}
	}
	return model.StateCompleted, "", true
}

// chatLimit takes plan limit l of chat c: Claude's goes where the limits
// view reads it (Codex's rollouts carry Codex's), and one that refused
// the turn schedules the limit resume when the turn ends.
func (d *Daemon) chatLimit(c *chat, l session.Limit) {
	if l.Rejected {
		c.limit = &l
	}
	if d.o.SaveLimit == nil || len(l.Windows) == 0 {
		return
	}
	fl := flow.Limit{Provider: c.cfg.Provider, Seen: time.Now()}
	d.mu.Lock()
	if w := d.workspace(c.cfg.Workspace); w != nil {
		fl.Profile = w.Profile
	}
	d.mu.Unlock()
	for _, w := range l.Windows {
		fl.Windows = append(fl.Windows, flow.Window{Minutes: w.Minutes, Used: w.Used, Resets: w.Resets})
	}
	d.o.SaveLimit(fl)
}

// saveLimit is Options.SaveLimit: a Claude limit goes in the file pitwall
// statusline writes, which internal/flow reads.
func saveLimit(l flow.Limit) {
	if l.Provider != model.ProviderClaude {
		return
	}
	data, err := json.Marshal(l)
	if err == nil {
		err = store.WriteAtomic(flow.ClaudeLimitsPath(config.StateDir(), l.Profile), data)
	}
	if err != nil {
		log.Printf("chat limit: %q", err)
	}
}

// chatResume sends text to chat id as the limit resume's turn.
func (d *Daemon) chatResume(id, text string) error {
	c, err := d.chat(id)
	if err != nil {
		return err
	}
	if !c.do(func() {
		var turn string
		turn, _, err = d.chatSend(context.Background(), c, session.Message{ID: newUUID(), Text: text}, "")
		c.resumed = err == nil && turn != ""
		d.chatChanged(c)
	}) {
		return session.ErrClosed
	}
	return err
}

// forLevel is s as a client of level sees it: below proto.ChatLevel, with
// no chat tabs and none of their activities.
func forLevel(s model.State, level int) model.State {
	if level >= proto.ChatLevel {
		return s
	}
	gone := map[string]bool{}
	for _, w := range s.Workspaces {
		if w.Chat != "" {
			gone[w.ID], gone[w.Chat] = true, true
		}
	}
	if len(gone) == 0 {
		return s
	}
	s.Workspaces = slices.DeleteFunc(slices.Clone(s.Workspaces), func(w model.Workspace) bool { return w.Chat != "" })
	s.Activities = slices.DeleteFunc(slices.Clone(s.Activities), func(a model.Activity) bool { return gone[a.PaneID] })
	s.Sessions = slices.Clone(s.Sessions)
	for i := range s.Sessions {
		s.Sessions[i].Order = slices.DeleteFunc(slices.Clone(s.Sessions[i].Order), func(id string) bool { return gone[id] })
	}
	return s
}

// closeChats closes every chat, for shutdown.
func (d *Daemon) closeChats() {
	d.mu.Lock()
	chats := slices.Collect(maps.Values(d.chats))
	d.chats = map[string]*chat{}
	d.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range chats {
		wg.Go(c.close)
	}
	wg.Wait()
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// newUUID is a random (version 4) UUID.
func newUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
