package app

import (
	"strconv"
	"time"

	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ports"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
)

// ServerStopper is a backend that hands over the daemon's answers to the
// window's ServerStop messages for tab ws that came since the last call.
type ServerStopper interface {
	ServerStops(ws string) []proto.ServerStopResult
}

// Suggester is a backend that holds each chat's latest answer to
// ChatSuggest.
type Suggester interface {
	ChatSuggestion(id string) (proto.ChatSuggestResult, bool)
}

// srvStops is one frame's answers to a tab's ServerStops, so every chat of
// the tab drawn that frame gets them.
type srvStops struct {
	at  time.Time
	out []proto.ServerStopResult
}

// serverList is the Servers view of a chat in tab ws.
func (u *ui) serverList(st *model.State, ws *model.Workspace) panel.ServerList {
	l := panel.ServerList{List: st.Servers[ws.ID], Remote: curHost() != "" || u.hostOf(ws.ID) != ""}
	switch {
	case u.link().Level < proto.ServersLevel:
		l.Note = "This tab's pitwall daemon is older than this window and does not list servers. Restart pitwall to update it."
	case !ports.Supported && !l.Remote:
		l.Note = "Listing a tab's servers needs Linux or macOS."
	}
	return l
}

// serverStops hands t the answers to tab ws's ServerStops, once per frame
// from the backend and to every chat of the tab drawn in it.
func (u *ui) serverStops(gtx gl.Context, ws string, t *panel.Transcript) {
	sp, ok := u.b.(ServerStopper)
	if !ok {
		return
	}
	if u.srvStops == nil {
		u.srvStops = map[string]srvStops{}
	}
	f := u.srvStops[ws]
	if !f.at.Equal(gtx.Now) {
		f = srvStops{at: gtx.Now, out: sp.ServerStops(ws)}
		u.srvStops[ws] = f
	}
	for _, r := range f.out {
		t.ServerStopped(r.PID, r.Port, r.Force, r.Running, r.Err)
	}
}

// serversTip is the header button's tooltip: "Servers: 3000, 5173".
func serversTip(ss []model.Server) string {
	tip := "Servers:"
	n := 0
	for _, s := range ss {
		for _, p := range s.Ports {
			if n == 4 {
				return tip + " …"
			}
			if n > 0 {
				tip += ","
			}
			tip += " " + strconv.Itoa(p)
			n++
		}
	}
	return tip
}

// finishedTurn is s's newest turn's id once it completed, "" while one
// runs or none did: the turn a suggested next prompt follows.
func finishedTurn(s *session.Snapshot) string {
	if len(s.Turns) == 0 || s.Busy() {
		return ""
	}
	t := s.Turns[len(s.Turns)-1]
	if t.State != session.TurnCompleted {
		return ""
	}
	return t.ID
}

// suggestion is the predicted next prompt chat id's empty composer shows,
// "" for none. With [chat] suggest_next on, it asks the daemon once per
// finished turn, and only while the composer is empty: a draft already
// says what comes next, and each suggestion costs plan usage.
func (u *ui) suggestion(st *model.State, id string, s *session.Snapshot, c *chatin.Composer) string {
	sg, ok := u.b.(Suggester)
	if !ok || !u.cfg.Chat.SuggestNext || u.link().Level < proto.SuggestLevel {
		return ""
	}
	turn := finishedTurn(s)
	if turn == "" {
		return ""
	}
	if u.suggested == nil {
		u.suggested = map[string]suggestAsk{}
	}
	a := u.suggested[id]
	r, ok := sg.ChatSuggestion(id)
	// A pane chat's transcript can end the turn before its hook says the
	// agent stopped: a refusal for that runs nothing, so it is asked again.
	again := a.turn == turn && ok && r.Turn == turn && (r.Code == "busy" || r.Code == "stale") &&
		a.tries < suggestTries && time.Since(a.at) >= suggestRetry
	if (a.turn != turn || again) && c.Text() == "" {
		if a.turn != turn {
			a = suggestAsk{turn: turn}
		}
		a.at, a.tries = time.Now(), a.tries+1
		u.suggested[id] = a
		u.send(proto.ChatSuggest{Chat: id, Turn: turn})
	}
	if !ok || r.Turn != turn || r.Err != "" {
		return ""
	}
	return r.Text
}

// suggestAsk is the turn a chat's suggestion was asked for, when last and
// how many times.
type suggestAsk struct {
	turn  string
	at    time.Time
	tries int
}

// A refused ask (the agent still working, as the daemon sees it) is asked
// again suggestRetry later, up to suggestTries asks in all.
const (
	suggestRetry = 2 * time.Second
	suggestTries = 3
)
