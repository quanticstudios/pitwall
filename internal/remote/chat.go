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
)

// Tab is one tab as the page's list shows it, in the sidebar's order. It
// holds what the sidebar shows, never a pane's screen.
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
	Picker bool   `json:"picker"`         // model and effort: Claude Code in a terminal
	Queued int    `json:"queued,omitempty"`
	// Dropped are the ids of phone messages the pane's agent left before
	// they went in, which the page shows as not sent.
	Dropped []uint64 `json:"dropped,omitempty"`
	// Transcript is the agent session's file; the page never sees it.
	Transcript string         `json:"-"`
	Provider   model.Provider `json:"-"`
}

// Models and Efforts are what the model picker offers Claude Code, as
// claude --model and --effort take them.
var (
	Models  = []string{"default", "fable", "opus", "opus[1m]", "sonnet", "haiku"}
	Efforts = []string{"low", "medium", "high", "xhigh", "max"}
)

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
	t, exited := Tab{ID: w.ID, Branch: w.Branch}, false
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
	t.Agent = string(t.Provider)
	if !t.Chat && !exited {
		t.Send = model.IsAgent(t.Provider) && t.Provider != model.ProviderTerminal
		t.Picker = t.Provider == model.ProviderClaude
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
		es[i].Text, es[i].Arg, es[i].Out = logs.Redact(es[i].Text), logs.Redact(es[i].Arg), logs.Redact(es[i].Out)
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
}

// stream serves the page's event stream: "hello" once, "tabs" whenever the
// list changes, and for the tab ?pane= names, "reset" with its latest
// entries, then "entries" as they are added or change.
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
	if !send("hello", map[string]any{"device": d.Name, "web": s.Web != nil, "models": Models, "efforts": Efforts}) {
		return
	}
	log.Printf("remote: %q opened the stream", d.Name)
	var lastTabs []byte
	view := chatView{}
	poll := time.NewTicker(pollEvery)
	defer poll.Stop()
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	var tabs []Tab
	fresh := true
	for {
		// why: revoking a device stops its stream at once, as it stops its token.
		if !Paired(s.Dir, d.ID) {
			return
		}
		changed := s.Changed()
		if fresh {
			tabs = s.Tabs()
			b, _ := json.Marshal(tabs)
			if !bytes.Equal(b, lastTabs) {
				lastTabs = b
				if _, err := fmt.Fprintf(w, "event: tabs\ndata: %s\n\n", b); err != nil {
					return
				}
				fl.Flush()
			}
		}
		if pane != "" && !s.chatEvents(&view, tabs, pane, send) {
			return
		}
		fresh = false
		select {
		case <-r.Context().Done():
			return
		case <-changed:
			// why: a turn changes the state in bursts; one list for each burst.
			time.Sleep(tabsSettle)
			fresh = true
		case <-poll.C:
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
	if t.Chat {
		return s.chatTabEvents(v, pane, send)
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

// chatTabEvents sends a chat tab's entries that are new or changed since
// v.sent, or all of the latest when the chat started over.
func (s *Server) chatTabEvents(v *chatView, pane string, send func(string, any) bool) bool {
	if s.ChatEntries == nil {
		return true
	}
	es, model, ok := s.ChatEntries(pane)
	if !ok {
		return true
	}
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
	s.lmu.Lock()
	pl := s.logs[pane]
	if pl != nil {
		es = pl.log.Before(before, pageSize)
		more = len(es) > 0 && len(pl.log.Before(es[0].Seq, 1)) > 0
	}
	s.lmu.Unlock()
	if pl == nil && s.ChatEntries != nil {
		if all, _, ok := s.ChatEntries(pane); ok {
			end := slices.IndexFunc(all, func(e flow.Entry) bool { return e.Seq >= before })
			if end < 0 {
				end = len(all)
			}
			es, more = slices.Clone(all[max(0, end-pageSize):end]), end > pageSize
		}
	}
	writeJSON200(w, map[string]any{"entries": redacted(es), "more": more})
}

// sendText types or queues the phone's message for a tab's agent.
func (s *Server) sendText(w http.ResponseWriter, r *http.Request, d Device) {
	var req struct {
		Pane string `json:"pane"`
		Text string `json:"text"`
	}
	if !decode(w, r, &req) {
		return
	}
	id, err := s.Send(req.Pane, req.Text)
	if err != nil {
		log.Printf("remote: %q: message to pane %s refused: %q", d.Name, req.Pane, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	log.Printf("remote: %q: message %d of %d bytes queued for pane %s", d.Name, id, len(req.Text), req.Pane) // never the text
	writeJSON200(w, map[string]any{"id": id})
}

// command switches a Claude Code pane's model or effort.
func (s *Server) command(w http.ResponseWriter, r *http.Request, d Device) {
	var req struct {
		Pane  string `json:"pane"`
		What  string `json:"what"` // model or effort
		Value string `json:"value"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !slices.Contains(map[string][]string{"model": Models, "effort": Efforts}[req.What], req.Value) {
		http.Error(w, "pick a model or an effort level the page offers", http.StatusBadRequest)
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
