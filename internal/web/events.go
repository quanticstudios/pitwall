package web

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/control"
	"github.com/quanticstudios/pitwall/internal/model"
)

// ControlFormatter is an optional extension of Control for formatted screen
// and scrollback reads ("text", "ansi" or "html").
type ControlFormatter interface {
	ReadFormat(pane string, screen bool, format string) (string, error)
	ScrollbackFormat(pane string, lines int, format string) (string, error)
}

// ControlKeys is an optional extension of Control for sending named keys
// encoded against the pane's terminal modes.
type ControlKeys interface {
	SendKeys(ctx context.Context, pane string, keys []string) error
}

// ControlEvent is a direct event emitted by the daemon for subscribeEvents
// when snapshot diffing alone could coalesce or miss it (such as a queued
// message sent or a turn ending).
type ControlEvent struct {
	Event       string    `json:"event"`
	At          time.Time `json:"-"`
	Session     string    `json:"session,omitempty"`
	SessionName string    `json:"sessionName,omitempty"`
	Tab         string    `json:"tab,omitempty"`
	Pane        string    `json:"pane,omitempty"`
	Parent      string    `json:"parent,omitempty"`
	Title       string    `json:"title,omitempty"`
	Agent       string    `json:"agent,omitempty"`
	State       string    `json:"state,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	PRState     string    `json:"prState,omitempty"`
	PRReason    string    `json:"prReason,omitempty"`
	PRNumber    int       `json:"prNumber,omitempty"`
	PRDetail    string    `json:"prDetail,omitempty"`
	PRURL       string    `json:"prUrl,omitempty"`
	From        string    `json:"from,omitempty"`
	Queued      *int      `json:"queued,omitempty"`
	ExitCode    *int      `json:"exitCode,omitempty"`
}

// ControlEventSource is an optional extension of Control for direct daemon
// events (queued.sent, turn.end).
type ControlEventSource interface {
	WatchEvents(fn func(ControlEvent)) (stop func())
}

// eventFrame formats ev as a pushed "event" protocol frame.
func eventFrame(ev ControlEvent) []byte {
	at := ev.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	m := map[string]any{
		"type":  "event",
		"event": ev.Event,
		"at":    at.UTC().Format(time.RFC3339Nano),
	}
	if ev.Session != "" {
		m["session"] = ev.Session
	}
	if ev.SessionName != "" {
		m["sessionName"] = ev.SessionName
	}
	if ev.Tab != "" {
		m["tab"] = ev.Tab
	}
	if ev.Pane != "" {
		m["pane"] = ev.Pane
	}
	if ev.Parent != "" {
		m["parent"] = ev.Parent
	}
	if ev.Title != "" {
		m["title"] = ev.Title
	}
	if ev.Agent != "" {
		m["agent"] = ev.Agent
	}
	if ev.State != "" {
		m["state"] = ev.State
	}
	if ev.Detail != "" {
		m["detail"] = ev.Detail
	}
	if ev.PRState != "" {
		m["prState"] = ev.PRState
	}
	if ev.PRReason != "" {
		m["prReason"] = ev.PRReason
	}
	if ev.PRNumber != 0 {
		m["prNumber"] = ev.PRNumber
	}
	if ev.PRDetail != "" {
		m["prDetail"] = ev.PRDetail
	}
	if ev.PRURL != "" {
		m["prUrl"] = ev.PRURL
	}
	if ev.From != "" {
		m["from"] = ev.From
	}
	if ev.Queued != nil {
		m["queued"] = *ev.Queued
	}
	if ev.ExitCode != nil {
		m["exitCode"] = *ev.ExitCode
	}
	return mustJSON(m)
}

// subscribeEvents answers id, then pushes an "event" frame for each change
// matching sessionName and paneAddr. A second subscribeEvents replaces the
// first; unsubscribeEvents stops them.
func (s *Server) subscribeEvents(c *conn, id, sessionName, paneAddr string) {
	st := s.Control.State()
	filterSession := ""
	filterPane := ""
	filterTab := ""
	if paneAddr != "" {
		p, err := resolvePane(st, sessionName, paneAddr)
		if err != nil {
			c.reply(answer(id, nil, err))
			return
		}
		filterPane = p.ID
		filterTab = p.WorkspaceID
		filterSession = st.SessionOf(p.WorkspaceID)
	} else if sessionName != "" {
		sess, err := pickSession(st, sessionName, control.Caller{})
		if err != nil {
			c.reply(answer(id, nil, err))
			return
		}
		filterSession = sess.ID
	}

	x := s.ctlOf(c)
	ctx, stop := context.WithCancel(x.ctx)
	s.mu.Lock()
	if x.stopEvents != nil {
		x.stopEvents()
	}
	x.stopEvents = stop
	s.mu.Unlock()

	wake := make(chan struct{}, 1)
	direct := make(chan ControlEvent, 64)
	exits := make(chan paneExitEvent, 32)
	hasDirect := false

	unwatch := s.Control.Watch(func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}, func(pane string, code int) {
		select {
		case exits <- paneExitEvent{pane: pane, code: code, at: time.Now().UTC()}:
		default:
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	var unwatchEvents func()
	if src, ok := s.Control.(ControlEventSource); ok {
		hasDirect = true
		unwatchEvents = src.WatchEvents(func(ev ControlEvent) {
			select {
			case direct <- ev:
			default:
			}
		})
	}

	prev := snapshotEvents(s.Control.State())
	c.reply(result(id, "", ""))

	matches := func(ev ControlEvent) bool {
		if filterPane != "" {
			if ev.Pane != "" {
				return ev.Pane == filterPane
			}
			return ev.Tab == filterTab
		}
		if filterSession != "" {
			return ev.Session == filterSession
		}
		return true
	}

	emit := func(ev ControlEvent) bool {
		if !matches(ev) {
			return true
		}
		frame := eventFrame(ev)
		if len(frame) > maxResultBytes {
			log.Printf("web: %q: an event of %d bytes not sent", c.name, len(frame))
			return true
		}
		return c.queueControl(ctx, frame, false)
	}

	go func() {
		defer unwatch()
		if unwatchEvents != nil {
			defer unwatchEvents()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-direct:
				if !emit(ev) {
					return
				}
			case ex := <-exits:
				ev := buildExitEvent(prev, s.Control.State(), ex)
				if !emit(ev) {
					return
				}
			case <-wake:
				cur := snapshotEvents(s.Control.State())
				evs := diffEventSnapshots(prev, cur, !hasDirect)
				prev = cur
				for _, ev := range evs {
					if !emit(ev) {
						return
					}
				}
			}
		}
	}()
}

type paneExitEvent struct {
	pane string
	code int
	at   time.Time
}

func buildExitEvent(prev eventSnapshot, st model.State, ex paneExitEvent) ControlEvent {
	code := ex.code
	ev := ControlEvent{
		Event:    "pane.exited",
		At:       ex.at,
		Pane:     ex.pane,
		ExitCode: &code,
	}
	if p, ok := prev.panes[ex.pane]; ok {
		ev.Tab, ev.Session, ev.SessionName, ev.Agent = p.tab, p.session, p.sessionName, p.agent
		return ev
	}
	for _, p := range st.Panes {
		if p.ID == ex.pane {
			ev.Tab = p.WorkspaceID
			ev.Session = st.SessionOf(p.WorkspaceID)
			if s := st.Session(ev.Session); s != nil {
				ev.SessionName = s.Name
			}
			ev.Agent = control.AgentOf(p)
			break
		}
	}
	return ev
}

type tabSnap struct {
	id          string
	session     string
	sessionName string
	title       string
	parent      string
	mainPane    string
	pr          prSnap
}

type prSnap struct {
	present bool
	number  int
	state   string
	reason  string
	detail  string
	url     string
}

type paneSnap struct {
	id          string
	tab         string
	session     string
	sessionName string
	agent       string
	state       string
	detail      string
	title       string
	queued      int
}

type eventSnapshot struct {
	tabs  map[string]tabSnap
	panes map[string]paneSnap
}

func snapshotEvents(st model.State) eventSnapshot {
	sessName := make(map[string]string, len(st.Sessions))
	for _, s := range st.Sessions {
		sessName[s.ID] = s.Name
	}
	actByPane := make(map[string]model.Activity, len(st.Activities))
	for _, a := range st.Activities {
		actByPane[a.PaneID] = st.AwaitPR(a)
	}
	snap := eventSnapshot{
		tabs:  make(map[string]tabSnap, len(st.Workspaces)),
		panes: make(map[string]paneSnap, len(st.Panes)),
	}
	for _, w := range st.Workspaces {
		ts := tabSnap{
			id:          w.ID,
			session:     w.SessionID,
			sessionName: sessName[w.SessionID],
			title:       control.Title(w),
			parent:      w.Parent,
		}
		if p := control.MainPane(control.TabPanes(st, w)); p != nil {
			ts.mainPane = p.ID
		}
		if pr, ok := st.PRs[w.ID]; ok {
			ts.pr = prSnap{
				present: true,
				number:  pr.Number,
				state:   string(pr.State),
				reason:  string(pr.Reason),
				detail:  pr.Detail,
				url:     pr.URL,
			}
		}
		snap.tabs[w.ID] = ts
	}
	for _, p := range st.Panes {
		sess := st.SessionOf(p.WorkspaceID)
		agent, state, detail := eventPaneState(p, actByPane)
		snap.panes[p.ID] = paneSnap{
			id:          p.ID,
			tab:         p.WorkspaceID,
			session:     sess,
			sessionName: sessName[sess],
			agent:       agent,
			state:       state,
			detail:      detail,
			title:       p.Title,
			queued:      st.Queued[p.ID],
		}
	}
	return snap
}

// eventPaneState maps a pane and its activity to the granular event state and
// detail: working, pending-approval, awaiting-input, plan-ready, done, error,
// pr-open, parked, idle, running, exited, or "".
func eventPaneState(p model.Pane, acts map[string]model.Activity) (agent, state, detail string) {
	agent = control.AgentOf(p)
	if p.Exited {
		return agent, "exited", ""
	}
	if a, ok := acts[p.ID]; ok {
		return agent, EventAgentState(a.State), a.Detail
	}
	switch {
	case agent != "" && p.Provider == "":
		return agent, "", ""
	case agent != "":
		return agent, "idle", ""
	default:
		return agent, "running", ""
	}
}

// EventAgentState maps model.AgentState to the stable event state string.
func EventAgentState(s model.AgentState) string {
	switch s {
	case model.StateCompleted:
		return "done"
	case model.StateTerminalRunning:
		return "running"
	default:
		return string(s)
	}
}

func diffEventSnapshots(prev, cur eventSnapshot, synthesize bool) []ControlEvent {
	now := time.Now().UTC()
	var out []ControlEvent

	// 1. Tabs opened and closed, title changes, and PR state changes.
	curTabIDs := sortedKeysOf(cur.tabs)
	for _, id := range curTabIDs {
		t := cur.tabs[id]
		old, existed := prev.tabs[id]
		if !existed {
			out = append(out, ControlEvent{
				Event:       "tab.opened",
				At:          now,
				Session:     t.session,
				SessionName: t.sessionName,
				Tab:         t.id,
				Pane:        t.mainPane,
				Parent:      t.parent,
				Title:       t.title,
			})
			if t.pr.present {
				out = append(out, prChangeEvent(now, t))
			}
			continue
		}
		if t.title != old.title {
			out = append(out, ControlEvent{
				Event:       "title",
				At:          now,
				Session:     t.session,
				SessionName: t.sessionName,
				Tab:         t.id,
				Pane:        t.mainPane,
				Title:       t.title,
			})
		}
		if t.pr != old.pr {
			out = append(out, prChangeEvent(now, t))
		}
	}

	prevTabIDs := sortedKeysOf(prev.tabs)
	for _, id := range prevTabIDs {
		if _, ok := cur.tabs[id]; !ok {
			old := prev.tabs[id]
			out = append(out, ControlEvent{
				Event:       "tab.closed",
				At:          now,
				Session:     old.session,
				SessionName: old.sessionName,
				Tab:         old.id,
				Parent:      old.parent,
				Title:       old.title,
			})
		}
	}

	// 2. Pane state, pane-level title (when tab title didn't already report it),
	// and synthesized turn.end / queued.sent when ControlEventSource is absent.
	curPaneIDs := sortedKeysOf(cur.panes)
	for _, id := range curPaneIDs {
		p := cur.panes[id]
		old, existed := prev.panes[id]
		if !existed {
			if p.state != "" && p.state != "running" {
				out = append(out, ControlEvent{
					Event:       "pane.state",
					At:          now,
					Session:     p.session,
					SessionName: p.sessionName,
					Tab:         p.tab,
					Pane:        p.id,
					Agent:       p.agent,
					State:       p.state,
					Detail:      p.detail,
				})
			}
			continue
		}
		if p.state != old.state || p.detail != old.detail || p.agent != old.agent {
			out = append(out, ControlEvent{
				Event:       "pane.state",
				At:          now,
				Session:     p.session,
				SessionName: p.sessionName,
				Tab:         p.tab,
				Pane:        p.id,
				Agent:       p.agent,
				State:       p.state,
				Detail:      p.detail,
			})
			if synthesize && isTurnEndTransition(old.state, p.state) {
				out = append(out, ControlEvent{
					Event:       "turn.end",
					At:          now,
					Session:     p.session,
					SessionName: p.sessionName,
					Tab:         p.tab,
					Pane:        p.id,
					Agent:       p.agent,
					State:       p.state,
					Detail:      p.detail,
				})
			}
		}
		if p.title != old.title && p.title != "" {
			// Avoid duplicating a tab title event that already carried this title.
			tab := cur.tabs[p.tab]
			oldTab := prev.tabs[p.tab]
			if tab.title == oldTab.title || tab.mainPane != p.id {
				out = append(out, ControlEvent{
					Event:       "title",
					At:          now,
					Session:     p.session,
					SessionName: p.sessionName,
					Tab:         p.tab,
					Pane:        p.id,
					Title:       p.title,
				})
			}
		}
		if synthesize && p.queued < old.queued {
			rem := p.queued
			out = append(out, ControlEvent{
				Event:       "queued.sent",
				At:          now,
				Session:     p.session,
				SessionName: p.sessionName,
				Tab:         p.tab,
				Pane:        p.id,
				Queued:      &rem,
			})
		}
	}
	return out
}

func prChangeEvent(now time.Time, t tabSnap) ControlEvent {
	return ControlEvent{
		Event:       "pr.state",
		At:          now,
		Session:     t.session,
		SessionName: t.sessionName,
		Tab:         t.id,
		Parent:      t.parent,
		PRNumber:    t.pr.number,
		PRState:     t.pr.state,
		PRReason:    t.pr.reason,
		PRDetail:    t.pr.detail,
		PRURL:       t.pr.url,
	}
}

func isTurnEndTransition(from, to string) bool {
	if to != "done" && to != "error" && to != "pr-open" {
		return false
	}
	return from == "working" || from == "pending-approval" || from == "awaiting-input" || from == "plan-ready"
}

func sortedKeysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
