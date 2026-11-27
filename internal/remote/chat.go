package remote

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

// Tab is one tab as the page's list shows it, in the sidebar's order. It
// holds what the sidebar shows, never a pane's screen: its one row from
// the screen is Spinner, the agent's status, redacted.
type Tab struct {
	Item
	ID     string `json:"id"`              // the tab's workspace id
	Group  string `json:"group,omitempty"` // its group's name
	Repo   string `json:"repo,omitempty"`  // its repo's or folder's base name
	Branch string `json:"branch,omitempty"`
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
	Chat   bool   `json:"chat,omitempty"` // a chat tab: pitwall chat drives it
	Send   bool   `json:"send"`           // the composer works: the pane runs an agent
	Attach bool   `json:"attach,omitempty"`
	// Picker is set for Claude Code in a terminal, for apps older than
	// Models: they show its chips with the hello's lists.
	Picker bool `json:"picker"`
	// Models, Efforts and Modes are what the composer's chips offer for the
	// tab's agent, nil for a chip it does not show; Mode is its permission
	// mode now. A daemon older than them sends none.
	Models  []string `json:"models,omitempty"`
	Efforts []string `json:"efforts,omitempty"`
	Modes   []string `json:"modes,omitempty"`
	Mode    string   `json:"mode,omitempty"`
	Queued  int      `json:"queued,omitempty"`
	Hold    string   `json:"hold,omitempty"`
	// HoldMenu says Hold is a menu open in the agent's terminal, which the
	// phone's Screen view clears.
	HoldMenu bool `json:"holdMenu,omitempty"`
	// Queue is what the tab's chat queued, in order, and QueueHold why it
	// waits for the user although the turn ended ("" while it waits for
	// the turn); POST /api/queue sends a row now, moves it or removes it.
	Queue        []QueueRow `json:"queue,omitempty"`
	QueueHold    string     `json:"queueHold,omitempty"`
	AllowBlocked string     `json:"allowBlocked,omitempty"`
	// Spinner is the agent's spinner row while its turn runs, verb and
	// detail, such as "Nesting… 9s · ↓ 232 tokens"; "" for none or from a
	// daemon too old to send it.
	Spinner     string `json:"spinner,omitempty"`
	Exited      bool   `json:"exited,omitempty"`
	ExitCode    int    `json:"exitCode,omitempty"`
	ExitUnknown bool   `json:"exitUnknown,omitempty"`
	// Dropped are the ids of phone messages the pane's agent left before
	// they went in, which the page shows as not sent.
	Dropped []uint64 `json:"dropped,omitempty"`
	// Parent is the tab this one hangs under in the sidebar, its top
	// ancestor (model.State.Root); "" for a tab the user opened.
	Parent string `json:"parent,omitempty"`
	// Icon is its repo's icon as GET /api/icon/{hash} serves it, NoIcon in
	// a repo without one, "" outside a repo; Dir is the folder it is in now.
	Icon string `json:"icon,omitempty"`
	Dir  string `json:"-"`
	// Away is the tab's chat's Since you left card while it shows, which
	// POST /api/away dismisses.
	Away *model.Away `json:"away,omitempty"`
	// Reports are the cards of the tab's workers (State.Reports).
	Reports []model.Report `json:"reports,omitempty"`
	// Inbox is what in this tab waits on the user (State.Inbox), which the
	// phone's Needs you section answers.
	Inbox []model.InboxItem `json:"inbox,omitempty"`
	// Workers are the tabs this one started, grouped for the chat's Work
	// sheet (model.State.WorkRail); nil for none or from an older daemon.
	Workers []model.Worker `json:"workers,omitempty"`
	// Transcript is the agent session's file; the page never sees it.
	Transcript string         `json:"-"`
	Provider   model.Provider `json:"-"`
}

// Models keeps Claude Code's family aliases so picks follow current releases.
// Models and Efforts are accepted by Claude Code 2.1.322's slash commands and flags.
var (
	Models  = []string{"default", "fable", "opus", "opus[1m]", "sonnet", "haiku"}
	Efforts = []string{"low", "medium", "high", "xhigh", "max"}
)

// AgyEfforts and AgyModes are Antigravity CLI 1.5.13's, as its TUI takes
// them (/effort, Shift+Tab) and its footer names them.
var (
	AgyEfforts = []string{"low", "medium", "high", "xhigh"}
	AgyModes   = []string{"default", "accept-edits", "auto", "plan"}
)

// ModesOf are the permission modes the phone offers for p's pane in mode,
// in Shift+Tab's order; nil for an agent pitwall cannot switch. Claude
// Code reaches bypass only in a session started with it, which mode shows.
//
// why: no Codex: pitwall cannot set Codex's mode in a terminal (paneSet).
func ModesOf(p model.Provider, mode string) []string {
	switch p {
	case model.ProviderClaude:
		if mode == "bypassPermissions" {
			return []string{"default", "acceptEdits", "plan", "bypassPermissions"}
		}
		return []string{"default", "acceptEdits", "plan"}
	case model.ProviderAntigravity:
		return AgyModes
	}
	return nil
}

// Tabs lists st's tabs as the sidebar shows them: each session in turn,
// its tabs in sidebar order, detached and Monitor tabs left out. A tab
// stands for its first agent pane, else its first pane. queued counts each
// pane's waiting messages.
func Tabs(st model.State, queued map[string]int) []Tab {
	var out []Tab
	for _, s := range st.Sessions {
		for _, w := range st.Ordered(s.ID) {
			if w.Detached || w.Monitor {
				continue
			}
			if t, ok := tabOf(st, s, w); ok {
				t.Queued = queued[t.Pane]
				out = append(out, t)
			}
		}
	}
	return out
}

func tabOf(st model.State, s model.Session, w model.Workspace) (Tab, bool) {
	t, exited := Tab{ID: w.ID, Branch: w.Branch, Parent: st.Root(w), Dir: st.LivePath(w)}, false
	t.Tab, t.Session = w.Label, s.Name
	if w.RepoRoot != "" {
		t.Repo = filepath.Base(w.RepoRoot)
	}
	if i := slices.IndexFunc(st.Projects, func(p model.Project) bool { return p.ID == w.ProjectID }); i >= 0 {
		t.Group = st.Projects[i].Name
	}
	if w.Chat != "" {
		i := slices.IndexFunc(st.Chats, func(c model.Chat) bool { return c.ID == w.Chat })
		if i < 0 {
			return t, false
		}
		t.Pane, t.Chat, t.Provider, t.Send = w.Chat, true, st.Chats[i].Provider, true
		t.Model, t.Effort = st.Chats[i].Model, st.Chats[i].Effort
	} else {
		var p *model.Pane
		if aps := st.AgentPanes(w); len(aps) > 0 {
			p = &aps[0].Pane
		} else if len(w.Tabs) > 0 {
			if ids := layout.Panes(w.Tabs[0].Layout); len(ids) > 0 {
				if i := slices.IndexFunc(st.Panes, func(sp model.Pane) bool { return sp.ID == ids[0] }); i >= 0 {
					p = &st.Panes[i]
				}
			}
		}
		if p == nil {
			return t, false
		}
		t.Pane, t.Provider, t.Transcript, exited = p.ID, p.Provider, p.Transcript, p.Exited
		t.Mode = p.AgentMode
		t.Exited, t.ExitCode, t.ExitUnknown = p.Exited, p.ExitCode, p.ExitUnknown
	}
	if i := slices.IndexFunc(st.Activities, func(a model.Activity) bool { return a.PaneID == t.Pane }); i >= 0 {
		a := st.AwaitPR(st.Activities[i])
		if t.Provider == "" {
			t.Provider = a.Provider
		}
		t.At, t.State, t.Label, t.Detail = a.UpdatedAt.UnixNano(), string(a.State), model.PillLabel(a), a.Detail
		if !t.Chat {
			t.Answer, t.Reply = Answerable(a), TakesReply(a)
		}
	}
	if exited {
		t.State, t.Label, t.Detail = "exited", fmt.Sprintf("Exited %d", t.ExitCode), ""
		if t.ExitUnknown {
			t.Label = "Exited, code unknown"
		}
		t.Answer, t.Reply, t.Mode = false, false, ""
	}
	if a, ok := st.Away[t.Pane]; ok {
		t.Away = &a
	}
	t.Reports = st.Reports[t.Pane]
	t.Agent = string(t.Provider)
	if !t.Chat && !exited {
		t.Send = model.IsAgent(t.Provider) && t.Provider != model.ProviderTerminal
		t.Picker = t.Provider == model.ProviderClaude
		t.Modes = ModesOf(t.Provider, t.Mode)
		switch t.Provider {
		case model.ProviderClaude:
			t.Models, t.Efforts = Models, Efforts
		case model.ProviderAntigravity:
			t.Efforts = AgyEfforts // its models are the daemon's to list
		}
	}
	return t, true
}

// Chat paging and streaming.
const (
	pageSize   = 60
	pollEvery  = 400 * time.Millisecond
	pingEvery  = 20 * time.Second
	logIdle    = 10 * time.Minute
	tabsSettle = 150 * time.Millisecond
)

// paneLog is a pane's transcript, shared by the streams that show it.
type paneLog struct {
	log  *flow.Log
	used time.Time
}

// logOf is pane's Log for provider's file at path: a new one when the path
// changed (a /clear, a resume, a fork). Callers hold s.lmu.
func (s *Server) logOf(pane string, provider model.Provider, path string) *flow.Log {
	now := time.Now()
	for id, pl := range s.logs {
		if now.Sub(pl.used) > logIdle {
			delete(s.logs, id)
		}
	}
	pl := s.logs[pane]
	if pl == nil || pl.log.Path != path || pl.log.Provider != provider {
		if s.logs == nil {
			s.logs = map[string]*paneLog{}
		}
		pl = &paneLog{log: flow.NewLog(provider, path)}
		s.logs[pane] = pl
	}
	pl.used = now
	return pl.log
}

// redacted is es with every text an agent wrote passed through logs.Redact.
func redacted(es []flow.Entry) []flow.Entry {
	for i := range es {
		es[i].Text, es[i].Arg, es[i].Out, es[i].Path = logs.Redact(es[i].Text), logs.Redact(es[i].Arg), logs.Redact(es[i].Out), logs.Redact(es[i].Path)
	}
	if es == nil {
		return []flow.Entry{}
	}
	return es
}

// chatView is what one stream knows it sent of a chat.
type chatView struct {
	log  *flow.Log
	gen  int64
	rev  int64
	sent map[int64]uint64 // a chat tab's entries by seq, as hashes
	none bool             // said that the tab has no transcript
	cmds []session.Command
	told bool // sent cmds
}

// stream serves the page's event stream: "hello" once, "tabs" whenever the
// list changes, and for the tab ?pane= names, "reset" with its latest
// entries, then "entries" as they are added or change. A chat the daemon
// pushes (Watch) is read when it changes; a transcript the page reads
// itself, at each poll.
//
// why: Server-Sent Events over fetch, not a WebSocket: the phone only
// listens here, since it sends with the POSTs the page already has, and
// fetch carries the Authorization header that EventSource cannot.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, d Device) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming is unavailable", http.StatusInternalServerError)
		return
	}
	pane := r.URL.Query().Get("pane")
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(event string, v any) bool {
		b, _ := json.Marshal(v)
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	if !send("hello", map[string]any{"device": d.Name, "version": s.Version, "web": s.Web != nil, "models": Models, "efforts": Efforts, "upload": s.Upload != nil, "files": s.Folder != nil,
		"start": s.Start != nil, "close": s.CloseTab != nil, "restart": s.Restart != nil,
		"screen": s.Screen != nil, "key": s.Key != nil, "stop": s.Stop != nil}) {
		return
	}
	log.Printf("remote: %q opened the stream", d.Name)
	var lastTabs []byte
	view := chatView{}
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	var wake <-chan struct{}
	var unwatch func()
	watch := func() {
		if pane != "" && wake == nil && s.Watch != nil {
			wake, unwatch = s.Watch(pane)
		}
	}
	defer func() {
		if unwatch != nil {
			unwatch()
		}
	}()
	watch()
	var tabs []Tab
	fresh, chat := true, true
	for {
		// why: revoking a device or removing a Tailscale allow rule stops its stream at once.
		if !s.deviceLive(d) {
			return
		}
		changed := s.Changed()
		if fresh {
			tabs = s.Tabs()
			b, _ := json.Marshal(tabs)
			if !bytes.Equal(b, lastTabs) {
				lastTabs, chat = b, true // a tab that exited or moved to another transcript
				if _, err := fmt.Fprintf(w, "event: tabs\ndata: %s\n\n", b); err != nil {
					return
				}
				fl.Flush()
			}
		}
		if pane != "" && chat && !s.chatEvents(&view, tabs, pane, send) {
			return
		}
		fresh, chat = false, false
		select {
		case <-r.Context().Done():
			return
		case <-changed:
			// why: a turn changes the state in bursts; one list for each burst.
			time.Sleep(tabsSettle)
			fresh = true
			watch() // a pane whose agent named its transcript since
		case <-wake:
			chat = true
		case <-poll.C:
			fresh = true // question pages can change without another activity hook
			chat = wake == nil || view.log != nil
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		}
	}
}

// chatEvents sends what changed in pane's chat since v, and reports
// whether the stream still works.
func (s *Server) chatEvents(v *chatView, tabs []Tab, pane string, send func(string, any) bool) bool {
	i := slices.IndexFunc(tabs, func(t Tab) bool { return t.Pane == pane })
	if i < 0 {
		v.log = nil
		if !v.none {
			v.none = true
			return send("reset", map[string]any{"entries": []flow.Entry{}, "gone": true})
		}
		return true
	}
	t := tabs[i]
	// A chat tab's, or a live agent pane's, as the window reads it.
	if s.ChatEntries != nil {
		if es, model, ok := s.ChatEntries(pane); ok {
			if v.log != nil {
				v.log, v.sent = nil, nil
			}
			return s.entryEvents(v, es, model, send) && s.commandsEvent(v, pane, send)
		}
	}
	if t.Chat || t.Provider == model.ProviderAntigravity { // why: flow.Log has no reader of agy's transcript
		return true
	}
	if t.Transcript == "" {
		v.log = nil
		if !v.none {
			v.none = true
			return send("reset", map[string]any{"entries": []flow.Entry{}, "none": true})
		}
		return true
	}
	s.lmu.Lock()
	l := s.logOf(pane, t.Provider, t.Transcript)
	l.Poll()
	var event string
	var es []flow.Entry
	more := false
	switch {
	case l != v.log || l.Gen != v.gen:
		event, es = "reset", l.Last(pageSize)
		more = len(es) > 0 && len(l.Before(es[0].Seq, 1)) > 0
	case l.Rev > v.rev:
		event, es = "entries", l.Since(v.rev)
	}
	model := l.Model
	v.log, v.gen, v.rev, v.none = l, l.Gen, l.Rev, false
	s.lmu.Unlock()
	if event == "" {
		return true
	}
	return send(event, map[string]any{"entries": redacted(es), "model": model, "more": more})
}

// entryEvents sends a chat's entries es that are new or changed since
// v.sent, or all of the latest when the chat started over.
func (s *Server) entryEvents(v *chatView, es []flow.Entry, model string, send func(string, any) bool) bool {
	hashes := make(map[int64]uint64, len(es))
	for _, e := range es {
		b, _ := json.Marshal(e)
		h := fnv.New64a()
		h.Write(b)
		hashes[e.Seq] = h.Sum64()
	}
	if v.sent == nil || len(hashes) < len(v.sent) {
		v.sent = hashes
		last := es[max(0, len(es)-pageSize):]
		return send("reset", map[string]any{"entries": redacted(slices.Clone(last)), "model": model, "more": len(last) < len(es)})
	}
	var changed []flow.Entry
	for _, e := range es {
		if old, seen := v.sent[e.Seq]; !seen || old != hashes[e.Seq] {
			changed = append(changed, e)
		}
	}
	v.sent = hashes
	if len(changed) == 0 {
		return true
	}
	return send("entries", map[string]any{"entries": redacted(changed), "model": model})
}

// commandsEvent sends pane's / menu as "commands" when it changed since
// v told it.
func (s *Server) commandsEvent(v *chatView, pane string, send func(string, any) bool) bool {
	if s.ChatMenu == nil {
		return true
	}
	cmds := s.ChatMenu(pane)
	if v.told && slices.EqualFunc(cmds, v.cmds, session.Command.Equal) {
		return true
	}
	v.cmds, v.told = cmds, true
	if cmds == nil {
		cmds = []session.Command{}
	}
	return send("commands", map[string]any{"commands": cmds})
}

// older serves ?pane='s entries before ?before=, a page at a time.
func (s *Server) older(w http.ResponseWriter, r *http.Request, _ Device) {
	pane := r.URL.Query().Get("pane")
	before, err := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	if pane == "" || err != nil {
		http.Error(w, "name the pane and the entry", http.StatusBadRequest)
		return
	}
	var es []flow.Entry
	more := false
	all, _, chat := []flow.Entry(nil), "", false
	if s.ChatEntries != nil {
		all, _, chat = s.ChatEntries(pane)
	}
	if chat {
		end := slices.IndexFunc(all, func(e flow.Entry) bool { return e.Seq >= before })
		if end < 0 {
			end = len(all)
		}
		es, more = slices.Clone(all[max(0, end-pageSize):end]), end > pageSize
	} else {
		s.lmu.Lock()
		if pl := s.logs[pane]; pl != nil {
			es = pl.log.Before(before, pageSize)
			more = len(es) > 0 && len(pl.log.Before(es[0].Seq, 1)) > 0
		}
		s.lmu.Unlock()
	}
	writeJSON200(w, map[string]any{"entries": redacted(es), "more": more})
}

// sendText types or queues the phone's message for a tab's agent.
func (s *Server) sendText(w http.ResponseWriter, r *http.Request, d Device) {
	var req struct {
		Pane  string `json:"pane"`
		Text  string `json:"text"`
		Image string `json:"image"`
	}
	if !decode(w, r, &req) {
		return
	}
	var id uint64
	var err error
	if req.Image != "" {
		if s.SendImage == nil {
			http.Error(w, "images are unavailable", http.StatusNotFound)
			return
		}
		id, err = s.SendImage(req.Pane, req.Text, req.Image)
	} else {
		id, err = s.Send(req.Pane, req.Text)
	}
	if err != nil {
		log.Printf("remote: %q: message to pane %s refused: %q", d.Name, req.Pane, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	log.Printf("remote: %q: message %d of %d bytes queued for pane %s", d.Name, id, len(req.Text), req.Pane) // never the text
	writeJSON200(w, map[string]any{"id": id})
}

// QueueRow is one queued message as the page lists it; Error says why
// it was not sent, and it waits for Send now.
type QueueRow struct {
	ID    string `json:"id"`
	Text  string `json:"text"`
	Error string `json:"error,omitempty"`
}

// queue runs a queue row's button: Send now, Remove, or a move to Index.
func (s *Server) queue(w http.ResponseWriter, r *http.Request, d Device) {
	var req struct {
		Pane   string `json:"pane"`
		ID     string `json:"id"`
		Action string `json:"action"` // now, remove or move
		Index  int    `json:"index"`  // move's place in the queue, 0 first
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Action != "now" && req.Action != "remove" && req.Action != "move" || req.ID == "" || s.Queue == nil {
		http.Error(w, "send a queued message now, move it or remove it", http.StatusBadRequest)
		return
	}
	if err := s.Queue(req.Pane, req.ID, req.Action, req.Index); err != nil {
		log.Printf("remote: %q: queue %s of pane %s refused: %q", d.Name, req.Action, req.Pane, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	log.Printf("remote: %q: queue %s in pane %s", d.Name, req.Action, req.Pane)
	writeJSON200(w, map[string]any{})
}

// command switches a pane's model, effort or permission mode to one its
// tab offers.
func (s *Server) command(w http.ResponseWriter, r *http.Request, d Device) {
	var req struct {
		Pane  string `json:"pane"`
		What  string `json:"what"` // model, effort or mode
		Value string `json:"value"`
	}
	if !decode(w, r, &req) {
		return
	}
	var offered []string
	if s.Tabs != nil {
		tabs := s.Tabs()
		if i := slices.IndexFunc(tabs, func(t Tab) bool { return t.Pane == req.Pane && !t.Chat }); i >= 0 {
			offered = map[string][]string{"model": tabs[i].Models, "effort": tabs[i].Efforts, "mode": tabs[i].Modes}[req.What]
		}
	}
	if !slices.Contains(offered, req.Value) {
		http.Error(w, "pick a model, effort or mode the page offers", http.StatusBadRequest)
		return
	}
	if err := s.Command(req.Pane, "/"+req.What+" "+req.Value); err != nil {
		log.Printf("remote: %q: /%s for pane %s refused: %q", d.Name, req.What, req.Pane, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	log.Printf("remote: %q: /%s %s sent to pane %s", d.Name, req.What, req.Value, req.Pane)
	writeJSON200(w, map[string]any{})
}
