package web

// The control commands (docs/web-protocol.md, Control): what pitwall ls
// --json, read, scrollback, wait, new, split, send, prompt, the tab
// commands, worktree new, queue and adopt do, for programs on the daemon's
// own socket, for tabs on this machine only. Addresses resolve with the CLI's own code in internal/control;
// the request names its session, since the daemon's environment is not the
// caller's. The TCP listeners leave Control nil and serve none of them.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/progstatus"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// Control is what the control commands read; the daemon implements it.
type Control interface {
	// State is a copy of the daemon's state.
	State() model.State
	// Read is the reply pane's agent last finished, or with screen the
	// text on its screen, as pitwall read gives them.
	Read(pane string, screen bool) (string, error)
	// Scrollback is pane's history and screen, its last lines lines when
	// lines is above 0.
	Scrollback(pane string, lines int) (string, error)
	// Watch calls changed each time the state may have changed and
	// exited each time a pane's process ends, until stop. Both run under
	// the daemon's lock, so neither blocks; exited runs before the state
	// without the pane can be read.
	Watch(changed func(), exited func(pane string, code int)) (stop func())
	// Settings is the user's config: the presets and [agents].
	Settings() config.Settings
	// Do runs req, a request as a gob client sends it (internal/proto),
	// as the daemon runs it for that client. Its error is a
	// ControlError: refused when the guard stopped a start.
	Do(ctx context.Context, req any) error
	// OpenPane is Do for m, with env added to the new pane's
	// environment; it returns the pane's id.
	OpenPane(m proto.OpenPane, env []string) (string, error)
	// NewTab is Do for m, and returns the new tab's id.
	NewTab(ctx context.Context, m proto.NewTab) (string, error)
	// CheckStart is what would stop the worker m describes from starting
	// on this machine, or with Warn set, what it would start without.
	CheckStart(ctx context.Context, m proto.HostCheck) []proto.HostProblem
}

var controlCommands = []string{"listTabs", "subscribeTabs", "unsubscribeTabs", "readPane", "scrollback", "waitPane",
	"newTab", "splitPane", "sendPane", "promptPane", "killTab", "renameTab", "detachTab", "focusTab", "tabNew", "tabClose", "setStatus",
	"worktreeNew", "queueAdd", "queueDrop", "queueList", "hostCheck", "adopt"}

// The control limits. Vars for tests.
var (
	maxWaits           = 64 // pending waitPane per connection
	maxScrollbackLines = 10_000
)

// ctl is one connection's control state; Server.mu guards it.
type ctl struct {
	ctx      context.Context // done when the connection stops reading
	cancel   context.CancelFunc
	waits    int                // pending waitPane
	stopTabs context.CancelFunc // ends the tabs subscription, nil without one
}

// ctlOf is c's control state, made on first use.
func (s *Server) ctlOf(c *conn) *ctl {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctl == nil {
		s.ctl = map[*conn]*ctl{}
	}
	x := s.ctl[c]
	if x == nil {
		ctx, cancel := context.WithCancel(context.Background())
		x = &ctl{ctx: ctx, cancel: cancel}
		s.ctl[c] = x
	}
	return x
}

// endControl stops c's waits and its tabs subscription: a client that
// closes its sending side gets no result of a pending waitPane.
func (s *Server) endControl(c *conn) {
	s.mu.Lock()
	x := s.ctl[c]
	delete(s.ctl, c)
	s.mu.Unlock()
	if x != nil {
		x.cancel()
	}
}

// envelope is every request's type and id.
type envelope struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// decode reads msg into v, refusing keys v does not have, so a request
// cannot carry what a later version would act on.
func decode(msg []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(msg))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// control runs a control command; false for any other.
func (s *Server) control(c *conn, typ, id string, msg []byte) bool {
	if s.Control == nil || !slices.Contains(controlCommands, typ) {
		return false
	}
	bad := func(err error) bool {
		if err != nil {
			c.reply(result(id, "invalid", fmt.Sprintf("%s: %v", typ, err)))
		}
		return err != nil
	}
	switch typ {
	case "listTabs":
		var r struct {
			envelope
			Session  string `json:"session"`
			Children string `json:"children"`
			Orphans  bool   `json:"orphans"`
		}
		if bad(decode(msg, &r)) {
			break
		}
		if r.Children != "" && r.Orphans {
			c.reply(result(id, "invalid", "listTabs takes children or orphans, not both"))
			break
		}
		data, err := listTabs(s.Control.State(), r.Session, r.Children, r.Orphans)
		c.reply(answer(id, data, err))
	case "subscribeTabs":
		var r struct {
			envelope
			Session string `json:"session"`
		}
		if bad(decode(msg, &r)) {
			break
		}
		s.subscribeTabs(c, id, r.Session)
	case "unsubscribeTabs":
		var r envelope
		if bad(decode(msg, &r)) {
			break
		}
		x := s.ctlOf(c)
		s.mu.Lock()
		if x.stopTabs != nil {
			x.stopTabs()
			x.stopTabs = nil
		}
		s.mu.Unlock()
		c.reply(result(id, "", ""))
	case "readPane":
		var r struct {
			envelope
			Pane    string `json:"pane"`
			Session string `json:"session"`
			Screen  bool   `json:"screen"`
		}
		if bad(decode(msg, &r)) {
			break
		}
		p, err := resolvePane(s.Control.State(), r.Session, r.Pane)
		if err == nil {
			var text string
			if text, err = s.Control.Read(p.ID, r.Screen); err != nil {
				err = ctlErr{"not-found", err.Error()}
			}
			c.reply(answer(id, map[string]any{"text": text}, err))
			break
		}
		c.reply(answer(id, nil, err))
	case "scrollback":
		var r struct {
			envelope
			Pane    string `json:"pane"`
			Session string `json:"session"`
			Lines   int    `json:"lines"`
		}
		if bad(decode(msg, &r)) {
			break
		}
		if r.Lines < 0 || r.Lines > maxScrollbackLines {
			c.reply(result(id, "invalid", fmt.Sprintf("lines is 0 for all, or at most %d", maxScrollbackLines)))
			break
		}
		p, err := resolvePane(s.Control.State(), r.Session, r.Pane)
		if err == nil {
			var text string
			if text, err = s.Control.Scrollback(p.ID, r.Lines); err != nil {
				err = ctlErr{"not-found", err.Error()}
			}
			c.reply(answer(id, map[string]any{"text": text}, err))
			break
		}
		c.reply(answer(id, nil, err))
	case "waitPane":
		var r struct {
			envelope
			Pane      string `json:"pane"`
			Session   string `json:"session"`
			Until     string `json:"until"`
			TimeoutMs int64  `json:"timeoutMs"`
		}
		if bad(decode(msg, &r)) {
			break
		}
		if !slices.Contains([]string{"done", "idle", "blocked", "exit"}, r.Until) || badTimeout(r.TimeoutMs) {
			c.reply(result(id, "invalid", "until is done, idle, blocked or exit, and timeoutMs must fit in a non-negative duration"))
			break
		}
		// Watch before taking the resolution snapshot: an exit between
		// that snapshot and the first step must retain its program code.
		watch := watchPane(s.Control)
		st := s.Control.State()
		p, err := resolvePane(st, r.Session, r.Pane)
		if err != nil {
			watch.stop()
			c.reply(answer(id, nil, err))
			break
		}
		watch.selectPane(p.ID)
		s.pending(c, id, watch, func(ctx context.Context) {
			s.waitPane(ctx, c, id, p.ID, r.Until, time.Duration(r.TimeoutMs)*time.Millisecond, st, watch)
		})
	default:
		s.drive(c, typ, id, msg)
	}
	return true
}

// badTimeout reports a timeoutMs that is negative or past a Duration.
func badTimeout(ms int64) bool { return ms < 0 || ms > int64((1<<63-1)/time.Millisecond) }

// pending runs wait in its own goroutine as one of c's pending waits, or
// answers busy when maxWaits are pending already.
func (s *Server) pending(c *conn, id string, watch *paneWatch, wait func(ctx context.Context)) {
	x := s.ctlOf(c)
	s.mu.Lock()
	full := x.waits >= maxWaits
	if !full {
		x.waits++
	}
	s.mu.Unlock()
	if full {
		watch.stop()
		c.reply(result(id, "busy", fmt.Sprintf("%d waits are pending on this connection", maxWaits)))
		return
	}
	go func() {
		defer func() {
			s.mu.Lock()
			x.waits--
			s.mu.Unlock()
		}()
		wait(x.ctx)
	}()
}

// ctlErr is a failed control command's result code and message.
type ctlErr struct{ code, msg string }

func (e ctlErr) Error() string { return e.msg }

// ControlError is a failed control command's result: code from the
// results table in docs/web-protocol.md, and msg for a person.
func ControlError(code, msg string) error { return ctlErr{code, msg} }

// answer is the result for data, or for err.
func answer(id string, data map[string]any, err error) map[string]any {
	if err == nil {
		return ok(id, "", data)
	}
	if e, isCtl := err.(ctlErr); isCtl {
		return result(id, e.code, e.msg)
	}
	code := "not-found"
	if strings.HasPrefix(err.Error(), "ambiguous") { // internal/control's words
		code = "ambiguous"
	}
	return result(id, code, err.Error())
}

// pickSession is the session a request names, by id, exact name or unique
// name prefix; with none, the one c's pane is in, else the most recently
// used one.
func pickSession(st model.State, name string, c control.Caller) (model.Session, error) {
	if s := st.Session(name); name != "" && s != nil {
		return *s, nil
	}
	c.Session = name
	return control.CurrentSession(st, c)
}

// listTabs is listTabs' data: the rows ls --json gives, then a stale row
// for each worker the daemon recorded on another host, whose state there
// it does not know.
func listTabs(st model.State, name, children string, orphans bool) (map[string]any, error) {
	if children != "" && !slices.ContainsFunc(st.Panes, func(p model.Pane) bool { return p.ID == children }) {
		return nil, ctlErr{"not-found", fmt.Sprintf("no pane %s", children)}
	}
	caller := control.Caller{Pane: children}
	sess, err := pickSession(st, name, caller)
	if err != nil && (name != "" || len(st.Sessions) > 0) {
		return nil, err
	}
	var only map[string]bool
	parent := func(model.RemoteWorker) bool { return true }
	switch {
	case children != "":
		if only, err = control.ChildTabs(st, caller); err != nil {
			return nil, err
		}
		tab := st.Panes[slices.IndexFunc(st.Panes, func(p model.Pane) bool { return p.ID == children })].WorkspaceID
		parent = func(w model.RemoteWorker) bool { return w.Parent == tab }
	case orphans:
		gone := func(id string) bool { return st.Workspace(id) == nil }
		only = map[string]bool{}
		for _, w := range st.Workspaces {
			if w.SessionID == sess.ID && !w.RemoteParent && w.Parent != "" && gone(w.Parent) {
				only[w.ID] = true
			}
		}
		parent = func(w model.RemoteWorker) bool { return gone(w.Parent) }
	}
	rows := control.Tabs(st, sess.ID, only)
	if orphans {
		for i := range rows {
			rows[i].Parent = st.Workspace(rows[i].ID).Parent // Tabs drops a parent that is gone
		}
	}
	for _, w := range sess.Workers {
		if w.Lost == "" && parent(w) {
			rows = append(rows, control.Tab{ID: w.ID, Parent: w.Parent, State: "unknown", Host: w.Host, Addr: "@" + w.ID, Stale: true, PaneList: []control.Pane{}})
		}
	}
	return map[string]any{"session": sessionData(sess), "tabs": rows}, nil
}

// sessionData names a session in a result, null for none.
func sessionData(s model.Session) any {
	if s.ID == "" {
		return nil
	}
	return map[string]string{"id": s.ID, "name": s.Name}
}

// resolvePane finds the pane addr names, as pitwall read does. A number
// or title needs session while more than one session is open; @ and an
// id needs none. A tab on another host is a remote error naming it.
func resolvePane(st model.State, name, addr string) (model.Pane, error) {
	id, err := addrSession(st, name, "pane", addr)
	if err != nil {
		return model.Pane{}, err
	}
	_, p, err := control.ResolvePane(st, control.Caller{}, id, addr)
	return p, err
}

// resolveTab finds the tab addr names, as pitwall kill does, under the
// rules of resolvePane.
func resolveTab(st model.State, name, addr string) (model.Workspace, error) {
	id, err := addrSession(st, name, "tab", addr)
	if err != nil {
		return model.Workspace{}, err
	}
	return control.ResolveTab(st, control.Caller{}, id, addr)
}

// addrSession is the id of the session to resolve addr, the field what
// of a request naming session name, in: "" for @ and an id.
func addrSession(st model.State, name, what, addr string) (string, error) {
	if addr == "" {
		return "", ctlErr{"invalid", what + " is required: an address such as #1, a title, or @ and an id"}
	}
	if host := remoteHost(st, addr); host != "" {
		return "", ctlErr{"remote", fmt.Sprintf("%s is on %s: connect to that host's socket", addr, host)}
	}
	if strings.HasPrefix(addr, "@") {
		return "", nil
	}
	if name == "" && len(st.Sessions) > 1 {
		return "", ctlErr{"invalid", fmt.Sprintf("%d sessions are open: name one in session to resolve %s", len(st.Sessions), addr)}
	}
	s, err := pickSession(st, name, control.Caller{})
	return s.ID, err
}

// remoteHost is the host of the tab addr names when it is a worker on
// another machine, @ and its id, or a host's tab, box:3; "" otherwise.
func remoteHost(st model.State, addr string) string {
	hosts := map[string]bool{}
	for _, s := range st.Sessions {
		for _, w := range s.Workers {
			if addr == "@"+w.ID {
				return w.Host
			}
			hosts[w.Host] = true
		}
		for _, r := range s.Remotes {
			hosts[r.Host] = true
		}
	}
	host, n, found := strings.Cut(addr, ":")
	if _, err := strconv.Atoi(strings.SplitN(n, ".", 2)[0]); found && err == nil && hosts[host] {
		return host
	}
	return ""
}

// paneWatch retains exits between resolution and the first wait step.
// Before resolution it records all exits; afterwards only its pane's.
type paneWatch struct {
	mu    sync.Mutex
	pane  string
	exits map[string]int
	wake  chan struct{}
	stop  func()
}

func watchPane(c Control) *paneWatch {
	w := &paneWatch{exits: map[string]int{}, wake: make(chan struct{}, 1)}
	poke := func() {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
	w.stop = c.Watch(poke, func(p string, code int) {
		w.mu.Lock()
		if w.pane == "" || w.pane == p {
			w.exits[p] = code
		}
		w.mu.Unlock()
		poke()
	})
	return w
}

func (w *paneWatch) selectPane(pane string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pane = pane
	for p := range w.exits {
		if p != pane {
			delete(w.exits, p)
		}
	}
}

// exit looks up pane's exit code in st, else among the exits w saw, as
// control.Wait.Step asks.
func (w *paneWatch) exit(st model.State, pane string) func() (int, bool) {
	return func() (int, bool) {
		if i := slices.IndexFunc(st.Panes, func(p model.Pane) bool { return p.ID == pane }); i >= 0 {
			return st.Panes[i].ExitCode, st.Panes[i].Exited
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		code, exited := w.exits[pane]
		return code, exited
	}
}

// waitPane answers id once pane gets to until, as pitwall wait does, or
// once timeout passes; nothing when ctx ends first.
func (s *Server) waitPane(ctx context.Context, c *conn, id, pane, until string, timeout time.Duration, st model.State, watch *paneWatch) {
	defer watch.stop()
	var expired <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expired = t.C
	}
	w := control.NewWait(until)
	for {
		if ctx.Err() != nil {
			return
		}
		o := w.Step(st, pane, watch.exit(st, pane))
		if o.Reason != "" {
			c.replyControl(ctx, ok(id, "", outcome(o)))
			return
		}
		select {
		case <-watch.wake:
		case <-expired:
			c.replyControl(ctx, result(id, "timeout", fmt.Sprintf("timed out after %s", timeout)))
			return
		case <-ctx.Done():
			return
		}
		st = s.Control.State()
	}
}

// outcome is waitPane's data for o.
func outcome(o control.Outcome) map[string]any {
	m := map[string]any{"outcome": o.Reason}
	switch {
	case o.Reason == control.Reached:
		m["state"] = o.State
	case o.Reason == control.Blocked:
		m["question"] = o.Question
	case o.Exit != nil:
		m["exit"] = map[string]any{"code": o.Exit.Code, "unknown": o.Exit.Unknown, "signal": o.Exit.Signal}
	}
	return m
}

// subscribeTabs answers id, then pushes session's tabs, as listTabs gives
// them, now and each time they change. A client that falls behind gets
// the latest list, not every one.
func (s *Server) subscribeTabs(c *conn, id, name string) {
	st := s.Control.State()
	sess, err := pickSession(st, name, control.Caller{})
	if err != nil {
		c.reply(answer(id, nil, err))
		return
	}
	x := s.ctlOf(c)
	ctx, stop := context.WithCancel(x.ctx)
	s.mu.Lock()
	if x.stopTabs != nil {
		x.stopTabs()
	}
	x.stopTabs = func() {
		stop()
		c.mu.Lock()
		c.dropTabs()
		c.mu.Unlock()
	}
	s.mu.Unlock()
	c.reply(result(id, "", ""))
	wake := make(chan struct{}, 1)
	wake <- struct{}{}
	unwatch := s.Control.Watch(func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}, func(string, int) {})
	go func() {
		defer unwatch()
		var last []byte
		for {
			select {
			case <-wake:
			case <-ctx.Done():
				return
			}
			st := s.Control.State()
			if cur := st.Session(sess.ID); cur != nil {
				sess = *cur // a rename shows
			}
			data, _ := listTabs(st, sess.ID, "", false)
			if data == nil {
				data = map[string]any{"session": sessionData(sess), "tabs": []control.Tab{}} // the session closed
			}
			data["type"] = "tabs"
			frame := mustJSON(data)
			if bytes.Equal(frame, last) {
				continue
			}
			if len(frame) > maxResultBytes {
				log.Printf("web: %q: a tabs list of %d bytes not sent", c.name, len(frame))
				continue
			}
			if !c.queueControl(ctx, frame, true) {
				return
			}
			last = frame
		}
	}()
}

// dropTabs removes the previous pending list. Callers hold c.mu.
func (c *conn) dropTabs() {
	c.q = slices.DeleteFunc(c.q, func(o out) bool {
		if !o.tabs {
			return false
		}
		c.frames--
		c.fbytes -= len(o.frame)
		c.signalDrained()
		return true
	})
}

func (c *conn) replyControl(ctx context.Context, v any) {
	if frame := c.replyFrame(v); frame != nil {
		c.queueControl(ctx, frame, false)
	}
}

// queueControl waits for capacity only while this wait or subscription
// is active. A tabs list replaces the pending list. Cancellation is
// checked under the queue lock, so stopped commands admit no old frames.
func (c *conn) queueControl(ctx context.Context, frame []byte, tabs bool) bool {
	for {
		c.mu.Lock()
		if ctx.Err() != nil || c.overloaded {
			c.mu.Unlock()
			return false
		}
		if tabs {
			c.dropTabs()
		}
		if c.room(len(frame)) {
			c.push(out{frame: frame, tabs: tabs})
			c.mu.Unlock()
			return true
		}
		drained := c.drained
		c.mu.Unlock()
		select {
		case <-drained:
		case <-c.dead:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// drive runs the control commands that start and drive tabs, as pitwall
// new, split, send, prompt, kill, rename, detach, attach, tab and status
// do. Each start goes through control.Launch and the daemon's guard gate.
func (s *Server) drive(c *conn, typ, id string, msg []byte) {
	ctx := s.ctlOf(c).ctx
	st := s.Control.State()
	var r struct {
		envelope
		Session    string   `json:"session"`
		Tab        string   `json:"tab"`
		Pane       string   `json:"pane"`
		Dir        string   `json:"dir"`
		Cmd        []string `json:"cmd"`
		Name       string   `json:"name"`
		Detached   bool     `json:"detached"`
		Preset     string   `json:"preset"`
		Model      string   `json:"model"`
		Guard      bool     `json:"guard"`
		ParentPane string   `json:"parentPane"`
		StartID    string   `json:"startId"`
		Down       bool     `json:"down"`
		Text       string   `json:"text"`
		FromPane   string   `json:"fromPane"`
		Wait       bool     `json:"wait"`
		TimeoutMs  int64    `json:"timeoutMs"`
		Title      string   `json:"title"`
		State      string   `json:"state"`
		App        string   `json:"app"`
		Progress   *int     `json:"progress"`
		Message    string   `json:"message"`
		Repo       string   `json:"repo"`
		From       string   `json:"from"`
		Branch     string   `json:"branch"`
		PR         int      `json:"pr"`
		Lane       string   `json:"lane"`
		Clone      string   `json:"clone"`
		At         string   `json:"at"`
		Every      string   `json:"every"`
		Worktree   string   `json:"worktree"`
		Task       string   `json:"task"`
		IDs        []string `json:"ids"`
		All        bool     `json:"all"`
	}
	err := decode(msg, &r)
	if err == nil {
		err = onlyFields(typ, msg)
	}
	if err != nil {
		c.reply(result(id, "invalid", fmt.Sprintf("%s: %v", typ, err)))
		return
	}
	var data map[string]any
	note := "" // a problem with a tab that did open
	switch typ {
	case "worktreeNew":
		data, note, err = s.worktreeNew(ctx, st, r.Repo, r.Name, r.Session, r.ParentPane, r.Preset, r.Lane, r.Cmd, r.Guard, r.From, r.Branch, r.PR)
	case "queueAdd":
		err = s.queueAdd(ctx, st, r.Session, r.Dir, r.Preset, r.Model, r.At, r.Every, r.Worktree, r.Cmd)
	case "queueList":
		var sess model.Session
		if sess, err = pickSession(st, r.Session, control.Caller{}); err == nil {
			data = map[string]any{"session": sessionData(sess), "tasks": queueRows(st, sess.ID)}
		}
	case "queueDrop":
		var t model.Task
		if t, err = queuedTask(st, r.Session, r.Task); err == nil {
			err = s.Control.Do(ctx, proto.DropTask{ID: t.ID})
		}
	case "hostCheck":
		data, err = s.hostCheck(ctx, r.Dir, r.Repo, r.Clone, r.Preset, r.Cmd, r.Guard)
	case "adopt":
		data, err = s.adopt(ctx, st, r.Session, r.Tab, r.IDs, r.All)
	case "newTab":
		data, err = s.newTab(ctx, st, r.Dir, r.Name, r.Session, r.Preset, r.Model, r.ParentPane, r.StartID, r.Cmd, r.Guard, r.Detached)
	case "splitPane":
		var w model.Workspace
		if w, err = resolveTab(st, r.Session, r.Tab); err != nil {
			break
		}
		open := proto.OpenPane{WorkspaceID: w.ID, TabID: w.ActiveTab, Dir: layout.Horizontal, Cmd: r.Cmd}
		if r.Down {
			open.Dir = layout.Vertical
		}
		for _, t := range w.Tabs {
			if panes := layout.Panes(t.Layout); t.ID == w.ActiveTab && len(panes) > 0 {
				open.Target = panes[len(panes)-1]
			}
		}
		var env []string
		if control.Guarded(st, w.ID) {
			env = []string{guard.Env + "=1"} // the gate refuses what the guard cannot hook
		}
		var pane string
		if pane, err = s.Control.OpenPane(open, env); err == nil {
			data = map[string]any{"pane": pane}
		}
	case "sendPane":
		var p model.Pane
		switch p, err = resolvePane(st, r.Session, r.Pane); {
		case err != nil:
		case strings.TrimSpace(r.Text) == "":
			err = ctlErr{"invalid", "text is empty"}
		case r.FromPane != "" && !slices.ContainsFunc(st.Panes, func(p model.Pane) bool { return p.ID == r.FromPane }):
			err = ctlErr{"not-found", fmt.Sprintf("no pane %s", r.FromPane)}
		default:
			err = s.Control.Do(ctx, proto.Send{Pane: p.ID, From: r.FromPane, Text: r.Text})
		}
	case "promptPane":
		if strings.TrimSpace(r.Text) == "" || badTimeout(r.TimeoutMs) {
			err = ctlErr{"invalid", "text must not be empty, and timeoutMs must fit in a non-negative duration"}
			break
		}
		watch := watchPane(s.Control)
		st = s.Control.State() // after the watch, as waitPane's
		p, err := resolvePane(st, r.Session, r.Pane)
		if err != nil {
			watch.stop()
			c.reply(answer(id, nil, err))
			return
		}
		watch.selectPane(p.ID)
		s.pending(c, id, watch, func(ctx context.Context) {
			s.promptPane(ctx, c, id, p.ID, r.Text, r.Wait, time.Duration(r.TimeoutMs)*time.Millisecond, st, watch)
		})
		return
	case "killTab", "renameTab", "detachTab", "focusTab":
		var w model.Workspace
		if w, err = resolveTab(st, r.Session, r.Tab); err != nil {
			break
		}
		req := map[string]any{
			"killTab":   proto.KillSession{WorkspaceID: w.ID},
			"renameTab": proto.RenameWorkspace{WorkspaceID: w.ID, Name: r.Title},
			"detachTab": proto.DetachSession{WorkspaceID: w.ID, Detached: true},
			"focusTab":  proto.FocusSession{WorkspaceID: w.ID},
		}[typ]
		if typ == "renameTab" && strings.TrimSpace(r.Title) == "" {
			err = ctlErr{"invalid", "title is empty"}
			break
		}
		err = s.Control.Do(ctx, req)
	case "tabNew", "tabClose":
		var p model.Pane
		switch p, err = resolvePane(st, r.Session, r.Pane); {
		case err != nil:
		case typ == "tabClose":
			err = s.Control.Do(ctx, proto.CloseTab{WorkspaceID: p.WorkspaceID})
		case control.Guarded(st, p.WorkspaceID):
			err = ctlErr{"refused", "the tab is guarded, and a new tab runs a shell, which the guard cannot cover"}
		default:
			var tab string
			if tab, err = s.Control.NewTab(ctx, proto.NewTab{FromPane: p.ID}); err == nil {
				data = map[string]any{"tab": tab}
			}
		}
	case "setStatus":
		var p model.Pane
		if p, err = resolvePane(st, r.Session, r.Pane); err != nil {
			break
		}
		if r.State == "" {
			err = s.Control.Do(ctx, proto.SetStatus{WorkspaceID: p.WorkspaceID, Text: r.Text})
			break
		}
		var payload []byte
		if payload, err = programPayload(r.State, r.App, r.Progress, r.Message); err == nil {
			err = s.Control.Do(ctx, proto.AgentEvent{Pane: p.ID, Provider: model.Provider("report"), Payload: payload})
		}
	}
	if err == nil && note != "" {
		c.reply(ok(id, note, data))
		return
	}
	c.reply(answer(id, data, err))
}

// driveFields are the keys each drive command takes besides type and id;
// decode refuses others for all of them, onlyFields for this one.
var driveFields = map[string][]string{
	"newTab":      {"dir", "cmd", "name", "detached", "preset", "model", "guard", "parentPane", "session", "startId"},
	"splitPane":   {"tab", "session", "down", "cmd"},
	"sendPane":    {"pane", "session", "text", "fromPane"},
	"promptPane":  {"pane", "session", "text", "wait", "timeoutMs"},
	"killTab":     {"tab", "session"},
	"renameTab":   {"tab", "session", "title"},
	"detachTab":   {"tab", "session"},
	"focusTab":    {"tab", "session"},
	"tabNew":      {"pane", "session"},
	"tabClose":    {"pane", "session"},
	"setStatus":   {"pane", "session", "text", "state", "app", "progress", "message"},
	"worktreeNew": {"repo", "name", "session", "parentPane", "from", "branch", "pr", "preset", "lane", "cmd", "guard"},
	"queueAdd":    {"session", "dir", "cmd", "preset", "model", "at", "every", "worktree"},
	"queueList":   {"session"},
	"queueDrop":   {"session", "task"},
	"hostCheck":   {"dir", "repo", "clone", "cmd", "preset", "guard"},
	"adopt":       {"tab", "session", "ids", "all"},
}

// onlyFields refuses a key of msg that command typ does not take.
func onlyFields(typ string, msg []byte) error {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(msg, &keys); err != nil {
		return err
	}
	for k := range keys {
		if k != "type" && k != "id" && !slices.Contains(driveFields[typ], k) {
			return fmt.Errorf("json: unknown field %q", k)
		}
	}
	return nil
}

// newTab opens a tab as pitwall new does and answers its id and its main
// pane's, or closed when a retry of startId finds the tab gone.
func (s *Server) newTab(ctx context.Context, st model.State, dir, name, session, preset, modelName, parentPane, startID string, cmd []string, guarded, detached bool) (map[string]any, error) {
	if !filepath.IsAbs(dir) {
		return nil, ctlErr{"invalid", "dir must be an absolute path"}
	}
	if startID == "" {
		startID = newStartID()
	}
	startID = strings.ToLower(startID) // one tab per id, whatever its case
	if _, err := hex.DecodeString(startID); err != nil || len(startID) != 16 {
		return nil, ctlErr{"invalid", "startId is 16 hex digits"}
	}
	if retry, err := matchingStart(st, session, parentPane, startID); retry || err != nil {
		if err != nil {
			return nil, err
		}
		return s.startResult(ctx, st, startID, detached)
	}
	if parentPane != "" && !slices.ContainsFunc(st.Panes, func(p model.Pane) bool { return p.ID == parentPane }) {
		return nil, ctlErr{"not-found", fmt.Sprintf("no pane %s", parentPane)}
	}
	caller := control.Caller{Pane: parentPane}
	sess, err := pickSession(st, session, caller)
	if err != nil && (session != "" || len(st.Sessions) > 0) {
		return nil, err // with no session open, the daemon makes one
	}
	settings := s.Control.Settings()
	cmd, vars, err := control.Launch(settings, preset, guarded || control.WorkerGuard(settings, caller, cmd, preset), cmd)
	if err == nil {
		cmd, err = control.WithModel(cmd, modelName)
	}
	if err != nil {
		return nil, ctlErr{"invalid", err.Error()}
	}
	// why: the program is found on the daemon's PATH when the pane starts.
	if err := s.Control.Do(ctx, proto.NewSession{Name: name, Cwd: dir, SessionID: sess.ID, Cmd: cmd, Env: vars, ParentPane: parentPane, ID: startID}); err != nil {
		return nil, err
	}
	return s.startResult(ctx, s.Control.State(), startID, detached)
}

func (s *Server) startResult(ctx context.Context, st model.State, startID string, detached bool) (map[string]any, error) {
	w := st.Workspace(startID)
	if w == nil {
		return map[string]any{"tab": startID, "closed": true}, nil
	}
	if detached && !w.Detached {
		if err := s.Control.Do(ctx, proto.DetachSession{WorkspaceID: w.ID, Detached: true}); err != nil {
			return nil, err
		}
	}
	data := map[string]any{"tab": w.ID}
	if p := control.MainPane(control.TabPanes(st, *w)); p != nil {
		data["pane"] = p.ID
	}
	return data, nil
}

// matchingStart matches a retry against its retained identity before
// resolving live-only inputs. The original session and parent can have
// closed too; their records still identify them. A live session with the
// same name is resolved normally, so it cannot claim the old session's id.
func matchingStart(st model.State, session, parentPane, id string) (bool, error) {
	closedTabs := st.Closed
	start, retained := st.Starts[id]
	if w := st.Workspace(id); w != nil {
		name := ""
		if sess := st.Session(w.SessionID); sess != nil {
			name = sess.Name
		}
		closedTabs = []model.ClosedTab{{Tab: *w, Session: name}}
	} else if retained && !slices.ContainsFunc(closedTabs, func(c model.ClosedTab) bool { return c.Tab.ID == id }) {
		closedTabs = []model.ClosedTab{{Tab: model.Workspace{ID: id, SessionID: start.SessionID, Parent: start.Parent}, Session: start.SessionName}}
	}
	for _, closed := range closedTabs {
		if closed.Tab.ID != id {
			continue
		}
		parent, parentSession := "", ""
		if parentPane != "" {
			for _, p := range st.Panes {
				if p.ID == parentPane {
					parent, parentSession = p.WorkspaceID, st.SessionOf(p.WorkspaceID)
				}
			}
			if parent == "" {
				for _, c := range st.Closed {
					if c.Pane.ID == parentPane {
						parent, parentSession = c.Tab.ID, c.Tab.SessionID
					}
				}
			}
			if parent == "" {
				if retained && start.ParentPane == parentPane && start.Parent == closed.Tab.Parent && start.SessionID == closed.Tab.SessionID {
					parent, parentSession = start.Parent, start.SessionID
				}
			}
			if parent == "" {
				return false, ctlErr{"not-found", fmt.Sprintf("no pane %s", parentPane)}
			}
		}
		sess, err := pickSession(st, session, control.Caller{Pane: parentPane})
		sessionID := sess.ID
		switch {
		case session == "" && parentSession != "":
			sessionID = parentSession
		case err != nil && (session == closed.Tab.SessionID || session == closed.Session || session == "" && len(st.Sessions) == 0):
			sessionID = closed.Tab.SessionID
		case err != nil:
			return false, err
		}
		// The daemon ignores a local parent from another session. Apply
		// that same rule before matching the closed request's identity.
		if parentSession != sessionID {
			parent = ""
		}
		if closed.Tab.SessionID != sessionID || closed.Tab.Parent != parent {
			return false, ctlErr{"conflict", fmt.Sprintf("tab id %s belongs to another tab", id)}
		}
		return true, nil
	}
	return false, nil
}

// newStartID is a startId for a newTab that names none.
func newStartID() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// programPayload is pitwall status <state>'s report-agent payload, checked
// as that command checks its flags.
func programPayload(state, app string, progress *int, msg string) ([]byte, error) {
	r := progstatus.Report{State: progstatus.State(state), App: app, Progress: progstatus.NoProgress, Msg: msg}
	if progress != nil {
		r.Progress = *progress
	}
	switch {
	case !slices.Contains([]progstatus.State{progstatus.Working, progstatus.Blocked, progstatus.Done, progstatus.Error, progstatus.Idle}, r.State):
		return nil, ctlErr{"invalid", "state is working, blocked, done, error or idle"}
	case r.App != "" && !progstatus.ValidApp(r.App):
		return nil, ctlErr{"invalid", fmt.Sprintf("app must be 1 to %d bytes of letters, digits and _ . + -", progstatus.MaxApp)}
	case progress != nil && (r.Progress < 0 || r.Progress > 100):
		return nil, ctlErr{"invalid", "progress must be 0 to 100"}
	case progress != nil && r.State != progstatus.Working && r.State != progstatus.Blocked:
		return nil, ctlErr{"invalid", "progress needs working or blocked"}
	case len(r.Msg) > progstatus.MaxMsgDecoded || !utf8.ValidString(r.Msg) || strings.ContainsFunc(r.Msg, progstatus.IsControl):
		return nil, ctlErr{"invalid", fmt.Sprintf("message must be at most %d bytes of UTF-8 without control characters", progstatus.MaxMsgDecoded)}
	}
	// No name, so a daemon without program status refuses it as an agent report.
	return json.Marshal(map[string]any{"name": "", "program": progstatus.Encode(r)})
}

// promptStartWait is how long a promptPane with wait waits for the agent
// to start the turn it typed, as a time.Duration; tests shorten it.
var promptStartWait atomic.Int64

func init() { promptStartWait.Store(int64(30 * time.Second)) }

// promptPane types text into pane's agent once it is idle, done or
// parked, as pitwall prompt does, and answers then, or with wait once
// that turn ends, with its reply. A blocked agent, or a pane that exits,
// ends it as waitPane's outcomes; timeout bounds the whole of it.
func (s *Server) promptPane(ctx context.Context, c *conn, id, pane, text string, wait bool, timeout time.Duration, st model.State, watch *paneWatch) {
	defer watch.stop()
	var expired, noStart <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expired = t.C
	}
	var sent time.Time
	started := false
	for {
		if ctx.Err() != nil {
			return
		}
		i := slices.IndexFunc(st.Panes, func(p model.Pane) bool { return p.ID == pane })
		if i < 0 || st.Panes[i].Exited {
			c.replyControl(ctx, ok(id, "", outcome(control.NewWait("exit").Step(st, pane, watch.exit(st, pane)))))
			return
		}
		agent, state, question := control.PaneState(st, st.Panes[i])
		if a := slices.IndexFunc(st.Activities, func(a model.Activity) bool { return a.PaneID == pane }); !sent.IsZero() && a >= 0 && st.Activities[a].UpdatedAt.After(sent) {
			started, noStart = true, nil
		}
		switch {
		case sent.IsZero() && agent == "":
			c.replyControl(ctx, result(id, "invalid", "the pane runs no agent"))
			return
		case (sent.IsZero() || started) && state == "blocked":
			c.replyControl(ctx, ok(id, "", map[string]any{"outcome": control.Blocked, "question": question}))
			return
		case sent.IsZero() && (state == "idle" || state == "done" || state == "parked"):
			// The daemon resumes a parked agent and types the prompt once it is back.
			sent = time.Now()
			if err := s.Control.Do(ctx, proto.Prompt{Pane: pane, Text: text}); err != nil {
				c.replyControl(ctx, answer(id, nil, err))
				return
			}
			if !wait {
				c.replyControl(ctx, ok(id, "", nil))
				return
			}
			t := time.NewTimer(time.Duration(promptStartWait.Load()))
			defer t.Stop()
			noStart = t.C
		case started && (state == "done" || state == "idle"):
			reply, err := s.Control.Read(pane, false)
			if err != nil {
				err = ctlErr{"not-found", "the turn ended, but: " + err.Error()}
			}
			c.replyControl(ctx, answer(id, map[string]any{"outcome": control.Reached, "state": state, "reply": reply}, err))
			return
		}
		select {
		case <-watch.wake:
		case <-expired:
			c.replyControl(ctx, result(id, "timeout", fmt.Sprintf("timed out after %s", timeout)))
			return
		case <-noStart:
			c.replyControl(ctx, result(id, "timeout", "the agent did not start a turn"))
			return
		case <-ctx.Done():
			return
		}
		st = s.Control.State()
	}
}

// worktreeNew opens a tab in a new worktree of the repo at repo, as
// pitwall worktree new run there does, and answers its ids. A lane whose
// workers start on another host is remote: this daemon starts nothing
// elsewhere. note is a setup problem of a tab that opened.
func (s *Server) worktreeNew(ctx context.Context, st model.State, repo, name, session, parentPane, preset, lane string, cmd []string, guarded bool, from, branch string, pr int) (data map[string]any, note string, err error) {
	switch {
	case !filepath.IsAbs(repo):
		return nil, "", ctlErr{"invalid", "repo must be an absolute path"}
	case strings.TrimSpace(name) == "":
		return nil, "", ctlErr{"invalid", "name is required: the worktree's folder and branch"}
	case parentPane != "" && !slices.ContainsFunc(st.Panes, func(p model.Pane) bool { return p.ID == parentPane }):
		return nil, "", ctlErr{"not-found", fmt.Sprintf("no pane %s", parentPane)}
	}
	settings := s.Control.Settings()
	if lane != "" {
		var host string
		if preset, cmd, host, err = control.Lane(settings, lane, preset, cmd); err != nil {
			return nil, "", ctlErr{"invalid", err.Error()}
		}
		if host != "" {
			return nil, "", ctlErr{"remote", fmt.Sprintf("lane %s starts its workers on %s: run pitwall worktree new --lane %s, or connect to that host's socket", lane, host, lane)}
		}
	}
	caller := control.Caller{Pane: parentPane}
	cmd, vars, err := control.Launch(settings, preset, guarded || control.WorkerGuard(settings, caller, cmd, preset), cmd)
	if err != nil {
		return nil, "", ctlErr{"invalid", err.Error()}
	}
	root, err := control.MainRoot(ctx, repo)
	if err != nil {
		return nil, "", ctlErr{"invalid", fmt.Sprintf("%s: %v", repo, err)}
	}
	f, err := control.WorktreeFrom(ctx, root, from, branch, pr)
	if err != nil {
		return nil, "", ctlErr{"invalid", err.Error()}
	}
	msg := proto.NewWorkspace{Name: name, From: f, Cmd: cmd, Env: vars, ParentPane: parentPane, Lane: lane, ID: newStartID()}
	if parentPane != "" {
		msg.Root = root // the worktree joins the parent's group, as from that pane
	} else {
		sess, err := pickSession(st, session, caller)
		if err != nil && (session != "" || len(st.Sessions) > 0) {
			return nil, "", err // with no session open, the daemon makes one
		}
		group := func(st model.State) *model.Project {
			for i, p := range st.Projects {
				if (sess.ID == "" || p.SessionID == sess.ID) && p.Kind == model.ProjectGit && p.Root == root {
					return &st.Projects[i]
				}
			}
			return nil
		}
		p := group(st)
		if p == nil {
			if err := s.Control.Do(ctx, proto.AddProject{Path: root, SessionID: sess.ID}); err != nil {
				return nil, "", err
			}
			if p = group(s.Control.State()); p == nil {
				return nil, "", ctlErr{"conflict", fmt.Sprintf("pitwall did not group %s as a git repository", root)}
			}
		}
		msg.ProjectID = p.ID
	}
	err = s.Control.Do(ctx, msg)
	st = s.Control.State()
	w := st.Workspace(msg.ID)
	if w == nil {
		if err == nil {
			err = ctlErr{"conflict", "the worktree's tab did not open"}
		}
		return nil, "", err
	}
	data = map[string]any{"tab": w.ID}
	if p := control.MainPane(control.TabPanes(st, *w)); p != nil {
		data["pane"] = p.ID
	}
	if err != nil {
		note = err.Error() // the tab opened; its setup had a problem
	}
	return data, note, nil
}

// queueAdd queues cmd, or a preset's agent with cmd, to run in dir, as
// pitwall queue add does.
func (s *Server) queueAdd(ctx context.Context, st model.State, session, dir, preset, modelName, at, every, worktree string, cmd []string) error {
	if !filepath.IsAbs(dir) {
		return ctlErr{"invalid", "dir must be an absolute path"}
	}
	next, repeat, err := model.ParseTaskSchedule(at, every, time.Now())
	if err != nil {
		return ctlErr{"invalid", err.Error()}
	}
	var vars []string
	if len(cmd) > 0 || preset != "" {
		if cmd, vars, err = control.Launch(s.Control.Settings(), preset, false, cmd); err != nil {
			return ctlErr{"invalid", err.Error()}
		}
	}
	if cmd, err = control.WithModel(cmd, modelName); err != nil {
		return ctlErr{"invalid", err.Error()}
	}
	if len(cmd) == 0 {
		return ctlErr{"invalid", "a task needs cmd or preset"}
	}
	sess, err := pickSession(st, session, control.Caller{})
	if err != nil {
		return err
	}
	return s.Control.Do(ctx, proto.NewTask{Task: model.Task{SessionID: sess.ID, Dir: dir, Cmd: cmd, Env: vars, NextStart: next, Repeat: repeat, Worktree: worktree, FreshWorktree: worktree != ""}, Queue: true})
}

// queueRows are session's queued tasks as pitwall queue ls lists them,
// with a title, never the command line or its environment.
func queueRows(st model.State, session string) []map[string]any {
	rows := []map[string]any{}
	for i, t := range control.Queued(st, session) {
		group := ""
		if p := slices.IndexFunc(st.Projects, func(p model.Project) bool { return p.ID == t.GroupID }); t.GroupID != "" && p >= 0 {
			group = st.Projects[p].Name
		}
		var next any
		if !t.NextStart.IsZero() {
			next = t.NextStart.UTC().Format(time.RFC3339)
		}
		rows = append(rows, map[string]any{
			"n": i + 1, "id": t.ID, "title": t.Title(), "dir": t.Dir, "group": group,
			"worktree": t.Worktree, "freshWorktree": t.FreshWorktree, "branch": t.Branch,
			"nextStart": next, "repeatMs": t.Repeat.Milliseconds(),
		})
	}
	return rows
}

// queuedTask is the queued task an id from queueList names, or its number
// there, which needs session while more than one session is open.
func queuedTask(st model.State, session, task string) (model.Task, error) {
	if i := slices.IndexFunc(st.Tasks, func(t model.Task) bool { return t.ID == task }); task != "" && i >= 0 {
		return st.Tasks[i], nil
	}
	if session == "" && len(st.Sessions) > 1 {
		return model.Task{}, ctlErr{"invalid", fmt.Sprintf("%d sessions are open: name one in session, or give the task's id", len(st.Sessions))}
	}
	sess, err := pickSession(st, session, control.Caller{})
	if err != nil {
		return model.Task{}, err
	}
	t, err := control.QueuedTask(control.Queued(st, sess.ID), task)
	if err != nil {
		return model.Task{}, ctlErr{"not-found", err.Error()}
	}
	return t, nil
}

// hostCheck answers what would stop a worker that newTab or worktreeNew
// starts with these fields from starting on this machine, as the check
// pitwall new --host asks a host for.
func (s *Server) hostCheck(ctx context.Context, dir, repo, clone, preset string, cmd []string, guarded bool) (map[string]any, error) {
	for _, p := range []string{dir, repo} {
		if p != "" && p != "~" && !filepath.IsAbs(p) && !strings.HasPrefix(p, "~/") {
			return nil, ctlErr{"invalid", "dir and repo are absolute paths, or start with ~/ for the daemon's home folder"}
		}
	}
	cmd, vars, err := control.Launch(s.Control.Settings(), preset, guarded, cmd)
	if err != nil {
		return nil, ctlErr{"invalid", err.Error()}
	}
	if len(cmd) == 0 && repo == "" {
		return nil, ctlErr{"invalid", "hostCheck needs cmd, preset or repo"}
	}
	problems := []map[string]any{}
	for _, p := range s.Control.CheckStart(ctx, proto.HostCheck{Cmd: cmd, Env: vars, Repo: repo, Clone: clone, Cwd: dir}) {
		problems = append(problems, map[string]any{"what": p.What, "fix": p.Fix, "warn": p.Warn})
	}
	return map[string]any{"problems": problems}, nil
}

// adopt makes tab the parent of orphans of its session on this machine,
// the ones ids names or all of them, as pitwall adopt run in that tab does.
// A worker on another host is remote, and all with one changes nothing.
func (s *Server) adopt(ctx context.Context, st model.State, session, addr string, ids []string, all bool) (map[string]any, error) {
	if (len(ids) > 0) == all {
		return nil, ctlErr{"invalid", "adopt takes ids or all"}
	}
	tab, err := resolveTab(st, session, addr)
	if err != nil {
		return nil, err
	}
	orphans := control.Orphans(st, tab.SessionID)
	var picked []string
	if all {
		if sess := st.Session(tab.SessionID); sess != nil {
			for _, w := range sess.Workers {
				if w.Lost == "" && st.Workspace(w.Parent) == nil {
					return nil, ctlErr{"remote", fmt.Sprintf("@%s is on %s: adopt it with pitwall adopt, or give ids of this machine's orphans", w.ID, w.Host)}
				}
			}
		}
		for _, o := range orphans {
			if o.ID != tab.ID && !st.AncestorOf(o.ID, tab.ID) { // the daemon refuses a loop
				picked = append(picked, o.ID)
			}
		}
	}
	for _, id := range ids {
		id = strings.TrimPrefix(id, "@")
		switch {
		case id == tab.ID:
			return nil, ctlErr{"invalid", "a tab cannot adopt itself"}
		case remoteHost(st, "@"+id) != "":
			return nil, ctlErr{"remote", fmt.Sprintf("@%s is on %s: adopt it with pitwall adopt", id, remoteHost(st, "@"+id))}
		case !slices.ContainsFunc(orphans, func(w model.Workspace) bool { return w.ID == id }):
			return nil, ctlErr{"not-found", fmt.Sprintf("@%s is not an orphan of this session (see listTabs with orphans)", id)}
		}
		picked = append(picked, id)
	}
	if len(picked) == 0 {
		return nil, ctlErr{"not-found", "no orphans to adopt (see listTabs with orphans)"}
	}
	for i, id := range picked {
		if err := s.Control.Do(ctx, proto.Reparent{WorkspaceID: id, ParentTab: tab.ID}); err != nil {
			if i > 0 {
				err = ctlErr{"conflict", fmt.Sprintf("adopted @%s, then @%s: %v", strings.Join(picked[:i], ", @"), id, err)}
			}
			return nil, err
		}
	}
	return map[string]any{"adopted": picked}, nil
}
