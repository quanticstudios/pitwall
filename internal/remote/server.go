package remote

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
)

// Item is one pane that needs you, as the page shows it. It holds what
// the sidebar shows, never the pane's screen.
type Item struct {
	Pane string `json:"pane"`
	// At is the activity's UpdatedAt, which an answer names. JSON carries
	// it as a string: a JavaScript number cannot hold it exactly.
	At      int64  `json:"at,string"`
	Tab     string `json:"tab"`
	Session string `json:"session"`
	Agent   string `json:"agent"`
	State   string `json:"state"`
	Label   string `json:"label"`  // "Approval", "Input", "Done"
	Detail  string `json:"detail"` // the question, approval or error
	Risk    string `json:"risk"`   // what pitwall flags in a permission request, "sudo"
	Advice  string `json:"advice"` // the decision model's recommendation, "allow 96%"
	Answer  bool   `json:"answer"` // Allow and Deny work
	Reply   bool   `json:"reply"`  // a reply works
}

// Replies reports whether a pane in state s takes a typed reply: a
// question, or an agent at its prompt after a turn.
func Replies(s model.AgentState) bool {
	return s == model.StateAwaitingInput || s == model.StateCompleted || s == model.StateError
}

// Items lists st's panes that need you, most pressing first.
func Items(st model.State) []Item {
	acts := make([]model.Activity, 0, len(st.Activities))
	for _, a := range st.Activities {
		if model.NeedsYou(a.State) {
			acts = append(acts, a)
		}
	}
	model.SortActivities(acts)
	out := make([]Item, 0, len(acts))
	for _, a := range acts {
		it := Item{Pane: a.PaneID, At: a.UpdatedAt.UnixNano(), Agent: string(a.Provider), State: string(a.State),
			Label: model.PillLabel(a), Detail: a.Detail, Risk: a.AdviceRule, Reply: Replies(a.State)}
		if a.Advice != "" {
			it.Advice = fmt.Sprintf("%s %.0f%%", a.Advice, a.AdviceP*100)
		}
		_, known := answers[a.Provider]
		it.Answer = a.State == model.StatePendingApproval && known
		for _, w := range st.Workspaces {
			if w.ID == a.WorkspaceID {
				it.Tab = w.Label
				if s := st.Session(w.SessionID); s != nil {
					it.Session = s.Name
				}
			}
		}
		out = append(out, it)
	}
	return out
}

//go:embed web
var web embed.FS

// Server is the phone page and its API. Every API call but pairing needs
// a device token, as "Authorization: Bearer <token>"; a header, not a
// cookie, so another site cannot make a phone's browser send it.
type Server struct {
	Dir   string
	Items func() []Item
	// Answer presses Allow or Deny on pane's permission prompt, and Reply
	// types text into pane and presses Enter, both only while the pane
	// still shows the activity of UpdatedAt at.
	Answer func(pane string, at int64, allow bool) error
	Reply  func(pane string, at int64, text string) error

	mu    sync.Mutex
	fails []time.Time // failed pairings and tokens in the last failWindow
	now   func() time.Time
}

// maxFails failed pairings or tokens in failWindow refuse every attempt,
// good tokens too, until the oldest is a failWindow old: telling a good
// token from a guess would let guessing go on.
const (
	maxFails   = 10
	failWindow = time.Minute
)

// failLog keeps a flood of bad tokens to a log line every 10 seconds.
var failLog = logs.Limiter{Every: 10 * time.Second}

// Handler serves the page at / and the API under /api/.
func (s *Server) Handler() http.Handler {
	if s.now == nil {
		s.now = time.Now
	}
	mux := http.NewServeMux()
	file := func(name, ctype string) http.HandlerFunc {
		data, _ := web.ReadFile("web/" + name)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ctype)
			w.Write(data)
		}
	}
	mux.HandleFunc("GET /{$}", file("index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /app.js", file("app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("POST /api/pair", s.pair)
	mux.HandleFunc("GET /api/items", s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
		writeJSON200(w, map[string]any{"items": s.Items(), "device": d.Name})
	}))
	mux.HandleFunc("POST /api/answer", s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
		var req struct {
			Pane  string `json:"pane"`
			At    int64  `json:"at,string"`
			Allow bool   `json:"allow"`
		}
		if !decode(w, r, &req) {
			return
		}
		verb := "deny"
		if req.Allow {
			verb = "allow"
		}
		if err := s.Answer(req.Pane, req.At, req.Allow); err != nil {
			log.Printf("remote: %q: %s in pane %s refused: %q", d.Name, verb, req.Pane, err)
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Printf("remote: %q: %s sent to pane %s", d.Name, verb, req.Pane)
		writeJSON200(w, map[string]any{})
	}))
	mux.HandleFunc("POST /api/reply", s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
		var req struct {
			Pane string `json:"pane"`
			At   int64  `json:"at,string"`
			Text string `json:"text"`
		}
		if !decode(w, r, &req) {
			return
		}
		if err := s.Reply(req.Pane, req.At, req.Text); err != nil {
			log.Printf("remote: %q: reply to pane %s refused: %q", d.Name, req.Pane, err)
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Printf("remote: %q: reply sent to pane %s", d.Name, req.Pane) // never the text
		writeJSON200(w, map[string]any{})
	}))
	return headers(mux)
}

// headers keeps the page to its own script and out of frames, and every
// answer out of caches.
func headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(v); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON200(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// limited reports whether attempts are refused now, dropping old failures.
// Callers hold s.mu.
func (s *Server) limited() bool {
	now := s.now()
	for len(s.fails) > 0 && now.Sub(s.fails[0]) >= failWindow {
		s.fails = s.fails[1:]
	}
	return len(s.fails) >= maxFails
}

// fail counts a failed attempt. Callers hold s.mu.
func (s *Server) fail(what string, r *http.Request) {
	s.fails = append(s.fails, s.now())
	if ok, held := failLog.Allow(what, s.now()); ok {
		more := ""
		if held > 0 {
			more = fmt.Sprintf(" (%d more since the last line)", held)
		}
		log.Printf("remote: %s from %s refused%s", what, r.RemoteAddr, more)
	}
}

var errLimited = errors.New("too many failed attempts; try again in a minute")

func (s *Server) authed(h func(http.ResponseWriter, *http.Request, Device)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		if s.limited() {
			s.mu.Unlock()
			http.Error(w, errLimited.Error(), http.StatusTooManyRequests)
			return
		}
		token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		d, ok := check(s.Dir, token)
		if !ok {
			s.fail("a bad device token", r)
		}
		s.mu.Unlock()
		if !ok {
			http.Error(w, "this device is not paired", http.StatusUnauthorized)
			return
		}
		h(w, r, d)
	}
}

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.limited() {
		http.Error(w, errLimited.Error(), http.StatusTooManyRequests)
		return
	}
	token, d, err := redeem(s.Dir, req.Code, s.now())
	if err != nil {
		s.fail("a bad pairing code", r)
		http.Error(w, errBadCode.Error(), http.StatusUnauthorized)
		return
	}
	log.Printf("remote: paired %q (%s)", d.Name, d.ID)
	writeJSON200(w, map[string]any{"token": token, "device": d.Name})
}
