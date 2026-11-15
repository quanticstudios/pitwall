package web

// The control commands (docs/web-protocol.md, Control): what pitwall ls
// --json, read, scrollback and wait do, for programs on the daemon's own
// socket. Addresses resolve with the CLI's own code in internal/control;
// the request names its session, since the daemon's environment is not the
// caller's. The TCP listeners leave Control nil and serve none of them.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/model"
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
}

var controlCommands = []string{"listTabs", "subscribeTabs", "unsubscribeTabs", "readPane", "scrollback", "waitPane"}

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
		if !slices.Contains([]string{"done", "idle", "blocked", "exit"}, r.Until) || r.TimeoutMs < 0 || r.TimeoutMs > int64((1<<63-1)/time.Millisecond) {
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
			break
		}
		go func() {
			defer func() {
				s.mu.Lock()
				x.waits--
				s.mu.Unlock()
			}()
			s.waitPane(x.ctx, c, id, p.ID, r.Until, time.Duration(r.TimeoutMs)*time.Millisecond, st, watch)
		}()
	}
	return true
}

// ctlErr is a failed control command's result code and message.
type ctlErr struct{ code, msg string }

func (e ctlErr) Error() string { return e.msg }

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
	if addr == "" {
		return model.Pane{}, ctlErr{"invalid", "pane is required: #N, N, TAB.N, a title or @ and an id"}
	}
	if host := remoteHost(st, addr); host != "" {
		return model.Pane{}, ctlErr{"remote", fmt.Sprintf("%s is on %s: connect to that host's socket", addr, host)}
	}
	var id string
	if !strings.HasPrefix(addr, "@") {
		if name == "" && len(st.Sessions) > 1 {
			return model.Pane{}, ctlErr{"invalid", fmt.Sprintf("%d sessions are open: name one in session to resolve %s", len(st.Sessions), addr)}
		}
		s, err := pickSession(st, name, control.Caller{})
		if err != nil {
			return model.Pane{}, err
		}
		id = s.ID
	}
	_, p, err := control.ResolvePane(st, control.Caller{}, id, addr)
	return p, err
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
		o := w.Step(st, pane, func() (int, bool) {
			if i := slices.IndexFunc(st.Panes, func(p model.Pane) bool { return p.ID == pane }); i >= 0 {
				return st.Panes[i].ExitCode, st.Panes[i].Exited
			}
			watch.mu.Lock()
			defer watch.mu.Unlock()
			code, exited := watch.exits[pane]
			return code, exited
		})
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
