package remote

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/web"
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
	// Requests are a chat's pending approvals and input requests, which
	// the page answers by id; the daemon fills them from the snapshot.
	Requests []Request `json:"requests,omitempty"`
}

// Request is one of a chat's pending approvals or input requests.
type Request struct {
	ID        string             `json:"id"`
	Kind      string             `json:"kind"`   // approval, plan, input
	Detail    string             `json:"detail"` // the approval's detail, the plan's text, the input's detail
	Tool      string             `json:"tool,omitempty"`
	Command   string             `json:"command,omitempty"`
	Path      string             `json:"path,omitempty"`
	Questions []session.Question `json:"questions,omitempty"`
}

// Requests are s's pending approvals, then its input requests, with
// every text the agent wrote passed through redact.
func Requests(s session.Snapshot, redact func(string) string) []Request {
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return redact(*p)
	}
	var out []Request
	for _, a := range s.PendingApprovals {
		r := Request{ID: a.ID, Kind: "approval", Detail: deref(a.Detail)}
		if a.RequestType == session.RequestPlan {
			r.Kind = "plan"
		}
		if t := a.Request; t != nil {
			r.Tool, r.Command, r.Path = t.ToolName, redact(t.Command), t.FilePath
		}
		out = append(out, r)
	}
	for _, in := range s.PendingUserInputs {
		qs := slices.Clone(in.Questions)
		for i := range qs {
			qs[i].Prompt = redact(qs[i].Prompt)
		}
		out = append(out, Request{ID: in.ID, Kind: "input", Detail: deref(in.Detail), Questions: qs})
	}
	return out
}

// Replies reports whether a pane in state s takes a typed reply: a
// question, or an agent at its prompt after a turn.
func Replies(s model.AgentState) bool {
	return s == model.StateAwaitingInput || s == model.StateCompleted || s == model.StateError || s == model.StatePROpen
}

// Items lists st's panes that need you, most pressing first.
func Items(st model.State) []Item {
	acts := make([]model.Activity, 0, len(st.Activities))
	for _, a := range st.Activities {
		if model.NeedsYou(a.State) {
			acts = append(acts, st.AwaitPR(a))
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
		it.Answer = Answerable(a)
		if slices.ContainsFunc(st.Chats, func(c model.Chat) bool { return c.ID == a.PaneID }) {
			it.Answer, it.Reply = false, false // a chat is answered through its Requests
		}
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
var page embed.FS

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
	// Approve answers chat's pending approval request with allow,
	// allow-session or deny, and AnswerInput its input request, through
	// the chat's loop as the GUI's answers go; by is "phone:<device>".
	Approve     func(chat, request, decision, reason, by string) error
	AnswerInput func(chat, request string, answers map[string][]string) error
	// Web, when set, is the web client for paired devices: a device
	// starts a session with its token at POST /api/web, and the session
	// gets the bundle at /app/, its bootstrap at /_pitwall/bootstrap, and
	// the web protocol at WS, served at /ws.
	Web *web.Client
	WS  http.Handler

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
		data, _ := page.ReadFile("web/" + name)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", ctype)
			w.Write(data)
		}
	}
	mux.HandleFunc("GET /{$}", file("index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /app.js", file("app.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /sw.js", file("sw.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /manifest.webmanifest", file("manifest.webmanifest", "application/manifest+json"))
	for _, png := range []string{"icon-192.png", "icon-512.png", "apple-touch-icon.png"} {
		mux.HandleFunc("GET /"+png, file(png, "image/png"))
	}
	mux.HandleFunc("POST /api/pair", s.pair)
	mux.HandleFunc("GET /api/items", s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
		writeJSON200(w, map[string]any{"items": s.Items(), "device": d.Name, "web": s.Web != nil})
	}))
	if s.Web != nil {
		mux.HandleFunc("POST /api/web", s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
			s.Web.Start(w, r, d.ID)
			log.Printf("remote: %q opened the web client", d.Name)
			writeJSON200(w, map[string]any{"url": "/app/"})
		}))
		mux.Handle("GET /app/", http.StripPrefix("/app", s.Web.Bundle()))
		mux.HandleFunc("GET /_pitwall/bootstrap", s.Web.Bootstrap)
		mux.Handle("GET /ws", sameOrigin(s.WS))
	}
	mux.HandleFunc("GET /api/push", s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
		key, err := VAPIDKey(s.Dir)
		if err != nil {
			log.Printf("remote: push key: %q", err)
			http.Error(w, "push is unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON200(w, map[string]any{"key": VAPIDPublic(key), "subscribed": Subscribed(s.Dir, d.ID)})
	}))
	mux.HandleFunc("POST /api/push", s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
		var sub Subscription
		if !decode(w, r, &sub) {
			return
		}
		if err := Subscribe(s.Dir, d.ID, sub); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("remote: %q turned on push notifications", d.Name)
		writeJSON200(w, map[string]any{})
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
	mux.HandleFunc("POST /api/approve", s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
		var req struct {
			Chat     string `json:"chat"`
			Request  string `json:"request"`
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}
		// why: the request is named, so an answer never lands on one the phone has not shown.
		if !decode(w, r, &req) || !named(w, req.Chat, req.Request) {
			return
		}
		if err := s.Approve(req.Chat, req.Request, req.Decision, req.Reason, "phone:"+d.Name); err != nil {
			log.Printf("remote: %q: %s of chat %s refused: %q", d.Name, req.Decision, req.Chat, err)
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Printf("remote: %q: %s sent to chat %s", d.Name, req.Decision, req.Chat) // never the reason
		writeJSON200(w, map[string]any{})
	}))
	mux.HandleFunc("POST /api/input", s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
		var req struct {
			Chat    string              `json:"chat"`
			Request string              `json:"request"`
			Answers map[string][]string `json:"answers"`
		}
		if !decode(w, r, &req) || !named(w, req.Chat, req.Request) {
			return
		}
		if err := s.AnswerInput(req.Chat, req.Request, req.Answers); err != nil {
			log.Printf("remote: %q: answer to chat %s refused: %q", d.Name, req.Chat, err)
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Printf("remote: %q: answer sent to chat %s", d.Name, req.Chat) // never the answer
		writeJSON200(w, map[string]any{})
	}))
	return headers(mux)
}

// named refuses, with 400, an answer that names no chat or request.
func named(w http.ResponseWriter, chat, request string) bool {
	if chat == "" || request == "" {
		http.Error(w, "name the chat and the request", http.StatusBadRequest)
		return false
	}
	return true
}

// headers keeps the page to its own script and out of frames, and every
// answer out of caches.
func headers(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

// sameOrigin refuses, with 403, a request whose Origin is set and is not
// this page's host. hello's token stops another site's page anyway; this
// stops it before the upgrade.
func sameOrigin(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != r.Host || u.Scheme != "http" && u.Scheme != "https" {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
		}
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
