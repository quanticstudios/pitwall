package daemon

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/errlog"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/eventlog"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/web"
)

// Chats: managed sessions the daemon drives through a session.Driver
// (docs/managed-sessions.md). Each chat has one loop goroutine, the only one
// that touches its config, snapshot and driver; commands reach it through
// do, and the driver's events wait in a queue the loop drains, so a driver
// that emits while a command waits on it never blocks.
//
// With Options.Chats set, each chat keeps an eventlog under it. The daemon
// reads only config.json of each at start; the first command loads the
// rest, and one that needs the provider resumes its session (Resume).

// recentDeltas is how many deltas a chat keeps for ChatRead After.
const recentDeltas = 1024

// coalesce is how long an append delta takes more text for the same item
// before the loop publishes it (managed-sessions.md, Delta).
const coalesce = 16 * time.Millisecond

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
	evs  []chatEvent // each with the start it came from

	// Owned by the loop.
	cfg    session.Config
	snap   session.Snapshot
	seq    int64
	drv    session.Driver
	starts int // Provider.Start calls; an event of an earlier one is dropped
	recent []session.Delta
	sum    model.Chat // the summary last put in State.Chats
	// limit is a plan limit that refused the running turn; its end
	// schedules the limit resume.
	limit *session.Limit
	// resumed marks the running turn as the limit resume's continue.
	resumed bool
	// cut: loading the chat ended a turn a restart cut off; the loop may
	// continue it once the command that loaded it is done.
	cut bool
	// check is the checkpoint taken for the turn being sent; the turn's
	// summary gets it as checkpointId when it starts.
	check string

	dir    string        // the chat's eventlog folder; "" in memory only
	log    *eventlog.Log // nil until the first command loads the chat
	loaded bool
	saved  []byte // config.json as last written
	// resuming: the driver started from cfg.Resume and has not confirmed
	// it yet; sent is the message it got meanwhile, to send again on a
	// fallback. recap goes before the next message after a fallback.
	resuming bool
	sent     *session.Message
	recap    string

	published int64     // the seq of the last delta logged and sent to subs
	hold      time.Time // until when the last delta, an append, takes more text
	flush     *time.Timer
	subs      map[web.Sink]bool
}

// chatError is a command's failure with its ChatResult code.
type chatError struct{ code, msg string }

func (e chatError) Error() string { return e.msg }

// Code is the web protocol's result code.
func (e chatError) Code() string { return e.code }

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
	case proto.ChatProviders:
		return d.chatProviders(), true
	case proto.ChatNew:
		return d.chatNew(ctx, m), true
	case proto.ChatFork:
		return d.chatFork(ctx, m), true
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
	case proto.ChatRewind:
		id = m.Chat
		run = func(c *chat) (string, string, error) {
			note, err := d.chatRewind(ctx, c, m.Turn)
			return "", note, err
		}
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
	return d.chatCreate(ctx, m, nil)
}

// forkSeed is what a fork's new chat starts from.
type forkSeed struct {
	title  string           // the chat it forks
	snap   session.Snapshot // its transcript through the turn
	points map[string]string
	from   *session.Origin // nil: no provider point, so a text recap
}

// chatCreate is chatNew, or with seed the fork of a chat: the new chat's
// first snapshot holds the seed's transcript and its provider starts from
// the seed's point.
func (d *Daemon) chatCreate(ctx context.Context, m proto.ChatNew, seed *forkSeed) proto.ChatResult {
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
	title := promptTitle(m.Prompt)
	if seed != nil {
		title = seed.title
	}
	c := d.newChat(newUUID(), prov, title)
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
			SessionState: session.StateReady, PermissionMode: cmp.Or(m.Mode, "default"), InteractionMode: interactionMode(m.Mode),
			Model: optional(m.Model), Effort: optional(m.Effort), UpdatedAt: stamp,
		}
		if seed != nil {
			s.Content, s.Turns = seed.snap.Content, seed.snap.Turns
		}
		c.commit([]session.Delta{{Type: session.DeltaSnapshot, Snapshot: &s}}, stamp)
		if seed != nil {
			c.system("Forked from " + cmp.Or(seed.title, "a chat"))
			c.cfg.Points = seed.points
			if seed.from != nil {
				c.cfg.Resume, c.cfg.From = seed.from.Resume, seed.from
			} else {
				c.forget("the turn left no point the provider can continue from", true)
			}
		}
		if err = d.chatStart(c); err == nil && m.Prompt != "" {
			turn, _, err = d.chatSend(ctx, c, session.Message{ID: newUUID(), Text: m.Prompt}, "")
		}
		d.chatChanged(c)
	})
	return chatResult(c.id, turn, note, err)
}

// chatArchive closes the chat's tab as CloseTab does, which closes the
// chat (removeWorkspace) and archives its folder. shortcut: Delete changes
// nothing yet; ChatArchive{Delete} of an archived chat comes with
// ChatUnarchive.
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

// chatFork opens a chat that continues m.Chat from the end of m.Turn
// (docs/managed-sessions.md, fork and rewind): a tab beside the chat's, or
// a worktree made from its branch, with the chat's settings and its
// transcript through the turn. The chat does not change.
func (d *Daemon) chatFork(ctx context.Context, m proto.ChatFork) proto.ChatResult {
	src, err := d.chat(m.Chat)
	if err != nil {
		return chatResult(m.Chat, "", "", err)
	}
	var seed forkSeed
	var cfg session.Config
	if !src.do(func() {
		var i int
		if i, err = turnIndex(src.snap, m.Turn); err != nil {
			return
		}
		if src.snap.Turns[i].State == session.TurnRunning {
			err = chatError{"busy", fmt.Sprintf("turn %d is still running", i+1)}
			return
		}
		cfg, seed.snap = src.cfg, upTo(src.snap, i+1)
		seed.points = map[string]string{}
		for j := range seed.snap.Turns {
			t := &seed.snap.Turns[j]
			if p := src.cfg.Points[t.ID]; p != "" {
				seed.points[t.ID] = p
			}
			t.CheckpointID = "" // the source tab's: the fork's tab has its own
		}
		if p := src.cfg.Points[src.snap.Turns[i].ID]; p != "" && src.cfg.Resume != "" {
			// While the chat's own fork or rewind has not reported its new
			// id, Resume is still the session it forks, which holds p.
			seed.from = &session.Origin{Resume: src.cfg.Resume, At: p, Fork: true}
		}
	}) {
		err = chatError{"not-found", "chat " + m.Chat + " is closed"}
	}
	if err != nil {
		return chatResult(m.Chat, "", "", err)
	}
	n := proto.ChatNew{Dir: cfg.Cwd, Provider: cfg.Provider, Model: cfg.Model, Effort: cfg.Effort, Mode: cfg.Mode}
	d.mu.Lock()
	if w := d.workspace(cfg.Workspace); w != nil {
		n.SessionID = d.st.SessionOf(w.ID)
		if m.Worktree != "" {
			n.Dir, n.Worktree, n.Base = cmp.Or(w.WorktreeRoot, cfg.Cwd), m.Worktree, w.Branch
		}
	}
	if i := slices.IndexFunc(d.st.Chats, func(x model.Chat) bool { return x.ID == src.id }); i >= 0 {
		seed.title = d.st.Chats[i].Title
	}
	d.mu.Unlock()
	return d.chatCreate(ctx, n, &seed)
}

// chatRewind drops turn name and every turn after it: the tab's files go
// back to the turn's checkpoint, refused as Undo turn refuses, then the
// provider continues from the end of the turn before, through a fork of
// its session whose new id replaces the chat's.
func (d *Daemon) chatRewind(ctx context.Context, c *chat, name string) (string, error) {
	i, err := turnIndex(c.snap, name)
	if err != nil {
		return "", err
	}
	if busy(c.snap) {
		return "", session.ErrBusy
	}
	t := c.snap.Turns[i]
	if t.CheckpointID == "" {
		return "", chatError{"unsupported", fmt.Sprintf("turn %d has no file checkpoint to go back to", i+1)}
	}
	d.mu.Lock()
	var dir string
	if w := d.workspace(c.cfg.Workspace); w != nil {
		dir = d.st.LivePath(*w)
	}
	d.mu.Unlock()
	if err := gitstat.RewindTurn(ctx, dir, c.cfg.Workspace, t.CheckpointID); err != nil {
		return "", chatError{"conflict", "the files were not put back, so nothing was rewound: " + err.Error()}
	}
	if c.drv != nil {
		cctx, cancel := context.WithTimeout(ctx, chatClose)
		if err := c.drv.Close(cctx); err != nil {
			log.Printf("chat %s: close for rewind: %q", c.id, err)
		}
		cancel()
		c.drv = nil
		c.starts++ // what the closed process still emits is dropped
	}
	for _, gone := range c.snap.Turns[i:] {
		delete(c.cfg.Points, gone.ID)
	}
	s := upTo(c.snap, i)
	c.commit([]session.Delta{{Type: session.DeltaSnapshot, Snapshot: &s}}, session.Stamp(time.Now()))
	c.resuming, c.sent, c.recap = false, nil, ""
	// While an earlier fork or rewind has not reported its new id, Resume
	// is still the session it forks, which holds every kept point.
	switch {
	case i == 0:
		c.cfg.Resume, c.cfg.From, c.cfg.Points = "", nil, nil
	case c.cfg.Resume == "" || c.cfg.Points[s.Turns[i-1].ID] == "":
		c.forget(fmt.Sprintf("turn %d left no point the provider can continue from", i), true)
	default:
		c.cfg.From = &session.Origin{Resume: c.cfg.Resume, At: c.cfg.Points[s.Turns[i-1].ID], Fork: true}
	}
	c.system(fmt.Sprintf("Rewound to before turn %d; its files are back as they were when it started", i+1))
	return fmt.Sprintf("rewound to before turn %d", i+1), nil
}

// turnIndex is the index in s.Turns of the turn name: its id, or its
// number from 1.
func turnIndex(s session.Snapshot, name string) (int, error) {
	if i := slices.IndexFunc(s.Turns, func(t session.TurnSummary) bool { return t.ID == name }); i >= 0 {
		return i, nil
	}
	if n, err := strconv.Atoi(name); err == nil && n >= 1 && n <= len(s.Turns) {
		return n - 1, nil
	}
	return 0, chatError{"not-found", "no turn " + name}
}

// upTo is s cut after its first n turns: those turns, the items before the
// first item of a later turn, and nothing pending.
func upTo(s session.Snapshot, n int) session.Snapshot {
	later := map[string]bool{}
	for _, t := range s.Turns[n:] {
		later[t.ID] = true
	}
	end := slices.IndexFunc(s.Content, func(it session.Item) bool { return it.TurnID != nil && later[*it.TurnID] })
	if end < 0 {
		end = len(s.Content)
	}
	s.Content, s.Turns = slices.Clone(s.Content[:end]), slices.Clone(s.Turns[:n])
	s.PendingApprovals, s.PendingUserInputs, s.PlanApproval = nil, nil, nil
	return s
}

// dropChat forgets a chat and closes it in the background. Callers hold
// d.mu.
func (d *Daemon) dropChat(id string) {
	c := d.chats[id]
	delete(d.chats, id)
	title := ""
	if i := slices.IndexFunc(d.st.Chats, func(s model.Chat) bool { return s.ID == id }); i >= 0 {
		title = d.st.Chats[i].Title
	}
	d.st.Chats = slices.DeleteFunc(d.st.Chats, func(s model.Chat) bool { return s.ID == id })
	d.dropPane(id) // its activity, attention, resume and decisions, as a pane's
	if c != nil {
		go func() {
			// The tab that held the title goes; an archived chat lists from config.json.
			c.do(func() { c.cfg.Title, c.cfg.Updated = title, time.Now() })
			c.close()
			if c.dir != "" {
				if err := eventlog.Archive(d.o.Chats, c.id); err != nil {
					log.Printf("chat %s: archive: %q", c.id, err)
				}
			}
		}()
	}
}

// newChat is a chat whose loop runs, with nothing loaded.
func (d *Daemon) newChat(id string, prov session.Provider, title string) *chat {
	c := &chat{id: id, prov: prov, title: title, cmds: make(chan func()), quit: make(chan struct{}), wake: make(chan struct{}, 1)}
	if d.o.Chats != "" {
		c.dir = filepath.Join(d.o.Chats, id)
	}
	return c
}

// loadChats rebuilds State.Chats: it reads config.json of each stored chat
// whose tab came back, and nothing else, so no snapshot loads and no
// provider starts. Callers hold d.mu.
func (d *Daemon) loadChats() {
	d.st.Chats = nil // what a saved or handed-over state carries is stale
	if d.o.Chats == "" {
		return
	}
	entries, err := os.ReadDir(d.o.Chats)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("chats: %q", err)
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "archive" {
			continue
		}
		cfg, err := eventlog.ReadConfig(filepath.Join(d.o.Chats, e.Name()))
		if err != nil {
			log.Printf("chat %s: %q", e.Name(), err)
			continue
		}
		w := d.st.Workspace(cfg.Workspace)
		if cfg.ID != e.Name() || w == nil || w.Chat != cfg.ID {
			log.Printf("chat %s: its tab %s is gone", e.Name(), cfg.Workspace)
			continue
		}
		c := d.newChat(cfg.ID, d.o.Providers[cfg.Provider], w.Label)
		c.cfg = cfg
		c.saved, _ = json.Marshal(cfg)
		c.sum = model.Chat{ID: c.id, WorkspaceID: w.ID, Provider: cfg.Provider, Model: cfg.Model, Effort: cfg.Effort, Mode: cfg.Mode,
			Title: w.Label, State: session.StateReady, Queued: len(cfg.Queue), UpdatedAt: time.Now()}
		d.chats[c.id] = c
		d.st.Chats = append(d.st.Chats, c.sum)
		go c.loop(d)
	}
}

// open loads the chat's log, once. A chat whose log does not open goes on
// in memory.
func (c *chat) open() {
	if c.loaded || c.dir == "" {
		return
	}
	c.loaded = true
	l, _, snap, seq, err := eventlog.Open(c.dir)
	if err != nil {
		log.Printf("chat %s: %q", c.id, err)
		return
	}
	c.log, c.snap, c.seq, c.published = l, snap, seq, seq
	c.cut = c.reconcile("pitwall restarted during this turn", "Expired: pitwall restarted before anyone answered")
}

// reconcile appends what the provider process, gone, left running or
// waiting (session.Reconcile), after the system line cut for a cut-off
// turn and expired for requests nobody can answer any more, both in the
// turn they are about; "" writes none. It reports a cut-off turn.
func (c *chat) reconcile(cut, expired string) bool {
	running := slices.ContainsFunc(c.snap.Turns, func(t session.TurnSummary) bool { return t.State == session.TurnRunning })
	if running && cut != "" {
		c.system(cut)
	}
	if len(c.snap.PendingApprovals)+len(c.snap.PendingUserInputs) > 0 && expired != "" {
		c.system(expired)
	}
	stamp := session.Stamp(time.Now())
	if ds := session.Reconcile(c.snap, stamp); ds != nil {
		c.commit(ds, stamp)
	}
	return running
}

// save writes config.json when the config changed.
func (c *chat) save() {
	if c.log == nil {
		return
	}
	b, _ := json.Marshal(c.cfg)
	if bytes.Equal(b, c.saved) {
		return
	}
	if err := c.log.WriteConfig(c.cfg); err != nil {
		log.Printf("chat %s: config: %q", c.id, err)
		return
	}
	c.saved = b
}

// close ends the driver, the log and the loop. It runs once per chat.
func (c *chat) close() {
	ctx, cancel := context.WithTimeout(context.Background(), chatClose)
	defer cancel()
	c.run(func() {
		if c.drv != nil {
			if err := c.drv.Close(ctx); err != nil {
				log.Printf("chat %s: close: %q", c.id, err)
			}
			c.drv = nil
		}
		c.hold = time.Time{}
		c.publish() // a held append, to the log and the subs
		if c.log != nil {
			c.save()
			if err := c.log.Compact(c.snap, c.seq); err != nil {
				log.Printf("chat %s: compact: %q", c.id, err)
			}
			c.log.Close()
			c.log = nil
		}
		for s := range c.subs {
			s.Removed(c.id)
		}
	})
	close(c.quit)
}

func (c *chat) loop(d *Daemon) {
	c.flush = time.NewTimer(coalesce)
	c.flush.Stop()
	for {
		select {
		case f := <-c.cmds:
			f()
			if c.cut {
				c.cut = false
				d.chatContinue(c)
				c.save()
				d.chatChanged(c)
			}
		case <-c.wake:
			c.evMu.Lock()
			evs := c.evs
			c.evs = nil
			c.evMu.Unlock()
			for _, e := range evs {
				if e.start == c.starts {
					d.chatEvent(c, e.Event)
				}
			}
			c.save()
			d.chatChanged(c)
		case <-c.flush.C: // the held append's time is up
		case <-c.quit:
			return
		}
		c.publish()
	}
}

// do runs f on c's loop, the chat loaded, and waits for it; false when the
// chat is closed.
func (c *chat) do(f func()) bool {
	return c.run(func() { c.open(); f(); c.save() })
}

// run is do without loading the chat.
func (c *chat) run(f func()) bool {
	done := make(chan struct{})
	select {
	case c.cmds <- func() { f(); close(done) }:
		<-done
		return true
	case <-c.quit:
		return false
	}
}

type chatEvent struct {
	session.Event
	start int
}

// emitter is the event callback of the driver Start number start returns.
// A driver that failed to start may still emit, and its session.exited
// must not end the one started after it.
func (c *chat) emitter(start int) func(session.Event) {
	return func(e session.Event) {
		c.evMu.Lock()
		c.evs = append(c.evs, chatEvent{e, start})
		c.evMu.Unlock()
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

// start runs c.prov.Start from `from`. The process outlives any one
// request: Close ends it.
func (c *chat) start(from session.Origin) (session.Driver, error) {
	c.starts++
	return c.prov.Start(context.Background(), c.cfg, from, c.emitter(c.starts))
}

func (d *Daemon) chatEvent(c *chat, e session.Event) {
	switch e.Type {
	case session.EventResumeID:
		c.cfg.Resume, c.cfg.From = e.ResumeID, nil
		c.resuming, c.sent = false, nil
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
	// A request the provider resolves while still pending was withdrawn: an
	// answer takes it out of the snapshot first.
	withdrawn := (e.Type == session.EventRequestResolved &&
		slices.ContainsFunc(c.snap.PendingApprovals, func(a session.Approval) bool { return a.ID == e.RequestID })) ||
		(e.Type == session.EventInputResolved &&
			slices.ContainsFunc(c.snap.PendingUserInputs, func(in session.InputRequest) bool { return in.ID == e.RequestID }))
	asked := len(c.snap.PendingApprovals)+len(c.snap.PendingUserInputs) > 0
	c.commit(session.Reduce(c.snap, e), e.CreatedAt)
	name := func() string { p := string(c.cfg.Provider); return strings.ToUpper(p[:1]) + p[1:] }
	if e.Type == session.EventTurnStarted && c.check != "" {
		if i := slices.IndexFunc(c.snap.Turns, func(t session.TurnSummary) bool { return t.ID == e.TurnID }); i >= 0 {
			t := c.snap.Turns[i]
			t.CheckpointID = c.check
			c.commit([]session.Delta{{Type: session.DeltaTurn, Turn: &t}}, e.CreatedAt)
		}
		c.check = ""
	}
	if withdrawn {
		c.system(name() + " withdrew this request")
	}
	if e.Type == session.EventExited {
		// Reduce ended the turn and the requests; tool calls are left.
		if asked {
			c.system("Expired: " + name() + " exited before anyone answered")
		}
		c.reconcile("", "")
	}
	if e.Type == session.EventExited && c.resuming {
		// Claude's --resume of a session it cannot find exits before
		// system/init.
		why := fmt.Sprintf("%s exited before it resumed the session", c.cfg.Provider)
		for _, t := range c.snap.Turns {
			if t.State == session.TurnRunning { // the message goes again below
				c.reduce(session.Event{Type: session.EventTurnCompleted, TurnID: t.ID, State: session.TurnFailed, ErrorMessage: why})
			}
		}
		c.forget(why, c.cfg.From != nil)
		if m := c.sent; m != nil {
			c.sent = nil
			if _, _, err := d.chatTurn(context.Background(), c, *m); err != nil {
				log.Printf("chat %s: send again without history: %q", c.id, err)
			}
			return
		}
	}
	d.chatNext(context.Background(), c)
}

// forget drops the provider session that did not resume, or a fork or
// rewind the provider cannot make (replay): the next start is a new one,
// and the next message carries a recap of the transcript. The points go
// with the session they were in.
func (c *chat) forget(why string, replay bool) {
	line := "Resumed without history: "
	if replay {
		line = "The provider continues from a text recap of this chat, not its history: "
	}
	c.resuming, c.cfg.Resume, c.cfg.From, c.cfg.Points = false, "", nil, nil
	c.recap = recap(c.snap)
	c.system(line + why)
}

// recapLimit bounds a recap's text, as docs/managed-sessions.md sets it.
const recapLimit = 2000

// recap is a short summary of s for a provider session that starts without
// its history: the last assistant text of each turn, the latest kept when
// they pass recapLimit. "" when there is none.
func recap(s session.Snapshot) string {
	var turns []string
	last := map[string]int{} // turn id to its index in turns
	for _, it := range s.Content {
		if it.Kind != session.KindAssistantText || strings.TrimSpace(it.Text) == "" {
			continue
		}
		turn := ""
		if it.TurnID != nil {
			turn = *it.TurnID
		}
		if i, ok := last[turn]; ok {
			turns[i] = it.Text
			continue
		}
		last[turn] = len(turns)
		turns = append(turns, it.Text)
	}
	text := []rune(strings.Join(turns, "\n\n"))
	if len(text) == 0 {
		return ""
	}
	if len(text) > recapLimit {
		text = text[len(text)-recapLimit:]
	}
	return "pitwall could not resume this chat's earlier session. What you said at the end of each earlier turn:\n\n" + string(text)
}

// commit numbers ds as one revision stamped at stamp and applies them. An
// append for the item the last delta appends to, within coalesce of it,
// joins that delta instead of taking a seq.
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
		appends := dl.Type == session.DeltaItemTextAppend || dl.Type == session.DeltaReasoningAppend || dl.Type == session.DeltaToolOutputAppend
		if last := len(c.recent) - 1; appends && time.Now().Before(c.hold) && c.recent[last].Type == dl.Type && c.recent[last].ItemID == dl.ItemID {
			m := &c.recent[last]
			m.Text += dl.Text
			m.Revision, m.UpdatedAt, m.ItemUpdatedAt = dl.Revision, dl.UpdatedAt, dl.ItemUpdatedAt
			c.snap = n
			continue
		}
		c.snap, c.seq = n, dl.Seq
		c.recent = append(c.recent, dl)
		if len(c.recent) > 2*recentDeltas {
			c.recent = slices.Clone(c.recent[len(c.recent)-max(recentDeltas, int(c.seq-c.published)):])
		}
		c.hold = time.Time{}
		if appends {
			c.hold = time.Now().Add(coalesce)
			c.flush.Reset(coalesce)
		}
	}
}

// publish writes the deltas numbered since the last publish to the log and
// hands them to subs, all but an append still within its coalesce time, so
// every consumer sees one stream. The log compacts only with nothing held.
func (c *chat) publish() {
	upto := c.seq
	if time.Now().Before(c.hold) {
		upto--
	}
	if upto <= c.published {
		return
	}
	ds := c.recent[len(c.recent)-int(c.seq-c.published) : len(c.recent)-int(c.seq-upto)]
	if c.log != nil {
		if err := c.log.Append(ds); err != nil {
			log.Printf("chat %s: log: %q", c.id, err)
		} else if upto == c.seq && c.log.Due() {
			if err := c.log.Compact(c.snap, c.seq); err != nil {
				log.Printf("chat %s: compact: %q", c.id, err)
			}
		}
	}
	if len(c.subs) > 0 {
		out := proto.ChatDeltas{Chat: c.id, Seq: c.published}
		for _, dl := range ds {
			b, _ := json.Marshal(dl)
			out.Deltas = append(out.Deltas, b)
		}
		for s := range c.subs {
			s.Deltas(out)
		}
	}
	c.published = upto
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

// read publishes what the loop holds back, so a subscriber's next deltas
// follow what it returns.
func (c *chat) read(after int64) proto.ChatDeltas {
	c.hold = time.Time{}
	c.publish()
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

// chatStart starts the provider when it is not running: resuming its
// session when the chat has one, else, or when that fails, a new one.
func (d *Daemon) chatStart(c *chat) error {
	if c.drv != nil {
		return nil
	}
	if c.prov == nil {
		return chatError{"unsupported", fmt.Sprintf("pitwall cannot run %s chats", c.cfg.Provider)}
	}
	from := session.Origin{Resume: c.cfg.Resume}
	if c.cfg.From != nil {
		from = *c.cfg.From
	}
	drv, err := c.start(from)
	switch {
	case err == nil:
		c.resuming = from.Resume != ""
	case from.Resume != "":
		// Forget the session only when a new one starts: a provider that
		// does not start at all says nothing about the session.
		if fresh, ferr := c.start(session.Origin{}); ferr == nil {
			c.forget(err.Error(), c.cfg.From != nil)
			drv, err = fresh, nil
		}
	}
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
			// shortcut: a checkpoint past turnWait leaves the turn without a checkpointId, so it cannot be rewound; hand it to the loop when it lands if that bites.
			c.check = d.checkpointWait(dir, c.cfg.Workspace) // as a pane's prompt hook does: the turn's edits stay out of it
		}
	}
	c.resumed, c.limit = false, nil
	sent := m
	if c.recap != "" {
		sent.Text = c.recap + "\n\n" + m.Text
	}
	turn, err := c.drv.Send(ctx, sent)
	if err == nil {
		c.recap = ""
		if c.resuming {
			c.sent = &m
		}
	}
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
	if slices.ContainsFunc(c.snap.Content, func(it session.Item) bool { return it.ID == m.ID }) {
		return // sent again after a fallback
	}
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
	plan := slices.ContainsFunc(c.snap.PendingApprovals, func(a session.Approval) bool {
		return a.ID == id && a.RequestType == session.RequestPlan
	})
	c.reduce(session.Event{Type: session.EventRequestResolved, RequestID: id})
	if err := c.deliver(func() error { return c.drv.Approve(ctx, id, dec) }); err != nil {
		return err
	}
	// Implement: the plan is approved, so the chat leaves plan mode. The
	// answer stands either way.
	if plan && dec.Allow && c.snap.PermissionMode == "plan" {
		if err := c.set(ctx, proto.ChatSet{Chat: c.id, Mode: "default"}); err != nil {
			c.system("The chat stays in plan mode: " + err.Error())
		}
	}
	return nil
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
	if m.Mode != "" {
		patch["interactionMode"] = interactionMode(m.Mode)
	}
	set, _ := json.Marshal(patch)
	c.commit([]session.Delta{{Type: session.DeltaSessionPatch, Set: set}}, session.Stamp(time.Now()))
	return nil
}

// interactionMode is aide's name for a chat in plan mode, which pitwall
// keeps as the permission mode.
func interactionMode(mode string) string {
	if mode == "plan" {
		return "plan"
	}
	return "build"
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

// restartContinue is what the loop sends a chat whose turn a restart cut
// off, with [agents] chat_continue_after_restart on, at most
// continueLimit times in continueWindow.
const (
	restartContinue = "Continue where you left off"
	continueLimit   = 3
	continueWindow  = 5 * time.Minute
)

// continueNow is the clock of the continue limit.
var continueNow = time.Now

// chatContinue resumes c's provider and sends restartContinue, when the
// setting is on, nothing started a turn meanwhile and the limit allows.
func (d *Daemon) chatContinue(c *chat) {
	if d.o.ChatContinue == nil || !d.o.ChatContinue() || busy(c.snap) {
		return
	}
	now := continueNow()
	c.cfg.Continued = slices.DeleteFunc(c.cfg.Continued, func(t time.Time) bool { return now.Sub(t) >= continueWindow })
	if len(c.cfg.Continued) >= continueLimit {
		c.system(fmt.Sprintf("Not continued: pitwall continued this chat %d times in the last %d minutes", continueLimit, int(continueWindow.Minutes())))
		return
	}
	c.cfg.Continued = append(c.cfg.Continued, now)
	if _, _, err := d.chatTurn(context.Background(), c, session.Message{ID: newUUID(), Text: restartContinue}); err != nil {
		log.Printf("chat %s: continue after the restart: %q", c.id, err)
	}
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
