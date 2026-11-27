package remote

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/flow"
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
	Answer  bool   `json:"answer"` // Allow and Deny work
	Reply   bool   `json:"reply"`  // a reply works
	// Requests are a chat's pending approvals and input requests, which
	// the page answers by id; the daemon fills them from the snapshot.
	Requests []Request       `json:"requests,omitempty"`
	Question *agent.Question `json:"question,omitempty"`
}

// Request is one of a chat's pending approvals or input requests.
type Request struct {
	ID        string              `json:"id"`
	Kind      string              `json:"kind"`   // approval, plan, input
	Detail    string              `json:"detail"` // the approval's detail, the plan's text, the input's detail
	Tool      string              `json:"tool,omitempty"`
	Command   string              `json:"command,omitempty"`
	Path      string              `json:"path,omitempty"`
	Questions []session.Question  `json:"questions,omitempty"`
	NoSession bool                `json:"noSession,omitempty"` // the prompt offers no Allow for this session
	Step      *int                `json:"step,omitempty"`
	Review    bool                `json:"review,omitempty"`
	Done      []bool              `json:"done,omitempty"`
	Answered  map[string][]string `json:"answered,omitempty"`
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
		r := Request{ID: a.ID, Kind: "approval", Detail: deref(a.Detail), NoSession: a.NoSession}
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
		var ans map[string][]string
		if len(in.Answered) > 0 {
			ans = make(map[string][]string, len(in.Answered))
			for k, vs := range in.Answered {
				cp := make([]string, len(vs))
				for i, v := range vs {
					cp[i] = redact(v)
				}
				ans[k] = cp
			}
		}
		out = append(out, Request{
			ID: in.ID, Kind: "input", Detail: deref(in.Detail), Questions: qs,
			Step: in.Step, Review: in.Review, Done: slices.Clone(in.Done), Answered: ans,
		})
	}
	return out
}

// Replies reports whether a pane in state s takes a typed reply: a
// question, or an agent at its prompt after a turn.
func Replies(s model.AgentState) bool {
	return s == model.StateAwaitingInput || s == model.StateCompleted || s == model.StateError || s == model.StatePROpen
}

// TakesReply reports whether a reply typed for a goes in: its state takes
// one and its screen shows no menu, where Enter would pick an option.
func TakesReply(a model.Activity) bool { return Replies(a.State) && !a.Menu }

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
			Label: model.PillLabel(a), Detail: a.Detail, Reply: TakesReply(a)}
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
	// the chat's loop as the GUI's answers go.
	Approve     func(chat, request, decision, reason string) error
	AnswerInput func(chat, request string, answers map[string][]string) error
	// Web, when set, is the web client for paired devices: a device
	// starts a session with its token at POST /api/web, and the session
	// gets the bundle at /app/, its bootstrap at /_pitwall/bootstrap, and
	// the web protocol at WS, served at /ws.
	Web *web.Client
	WS  http.Handler

	// Version is the daemon's build, as pitwall version prints it; the
	// stream's hello names it.
	Version string

	// Tabs lists every tab for the chat page, and Changed is closed at the
	// daemon's next change. ChatEntries is a chat tab's transcript, or a
	// live agent pane's, and the model its latest turn used; ok is false
	// for no such chat. Watch has wake get a value at each change of that
	// chat until stop; a nil wake for none, whose transcript the page reads
	// itself (flow.Log) as it polls.
	Tabs        func() []Tab
	Changed     func() <-chan struct{}
	ChatEntries func(chat string) (entries []flow.Entry, model string, ok bool)
	Watch       func(chat string) (wake <-chan struct{}, stop func())
	// ChatMenu is the / menu of a chat ChatEntries reads, as the
	// window's composer lists it; the stream sends it as "commands".
	ChatMenu func(chat string) []session.Command
	// Send types text into pane's agent once it sits at its input box,
	// queued until then; id names the message in Tab.Dropped. Command sets
	// pane's model, effort or permission mode from "/model <id>",
	// "/effort <level>" or "/mode <mode>", a value its Tab offers; the
	// daemon refuses what its desktop chips would, as a model mid-turn.
	Send    func(pane, text string) (id uint64, err error)
	Command func(pane, command string) error
	// Queue sends pane's chat's queued message id now, or removes it.
	Queue       func(pane, id, action string) error
	OpenDesktop func(pane string) error
	Question    func(pane, id string, option int, text string) error
	// Screen is pane's screen as pitwall read --screen prints it, Key
	// presses one of Keys in pane as pitwall send-key does, refused on a
	// guarded tab, and Stop interrupts pane's agent's turn as the window's
	// Stop does. Nil serves none of them.
	Screen    func(pane string) (string, error)
	Key       func(pane, key string) error
	Stop      func(pane string) error
	Upload    func(pane string, data []byte) (string, error)
	SendImage func(pane, text, image string) (uint64, error)
	// Folder is tab's working directory and the folder, its repo's or
	// the one it runs in, that the files the phone reads for it must be
	// in; ok is false for no such tab. Nil serves no files.
	Folder func(tab string) (cwd, root string, ok bool)
	// Icons, when set, serves the repo icons Tab.Icon names.
	Icons *Icons

	// NewOptions is what the New form offers for the session of a name,
	// and Start opens the tab a checked NewTab describes, as pitwall new
	// or pitwall worktree new does. CloseTab closes a tab as the window's
	// close does, and Restart starts an exited pane's command again, its
	// agent's session resumed when one is known. Nil serves none of them.
	NewOptions func(session string) NewOptions
	Start      func(NewTab) (tab, pane string, err error)
	CloseTab   func(tab string) error
	Restart    func(pane string) error

	// Authorize, when set (on the built-in Tailscale listener only),
	// authenticates requests without a paired-device Bearer token using
	// the connection's Tailscale identity. code == 0 allows the request
	// as Device; a non-zero HTTP status code denies it with msg.
	Authorize func(*http.Request) (Device, int, string)
	// DeviceLive reports whether an already-authenticated Device may keep
	// an open event stream alive. When nil, Paired(s.Dir, d.ID) is used.
	DeviceLive func(Device) bool

	lmu  sync.Mutex
	logs map[string]*paneLog // by pane, the transcripts the page reads

	pmu  sync.Mutex
	pins map[string]pin // by tab, the folder its files are read from

	mu    sync.Mutex
	fails map[string][]time.Time // failed pairings and tokens per client IP in the last failWindow
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
	mux.HandleFunc("GET /app.css", file("app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /sw.js", file("sw.js", "text/javascript; charset=utf-8"))
	mux.HandleFunc("GET /manifest.webmanifest", file("manifest.webmanifest", "application/manifest+json"))
	for _, png := range []string{"icon-192.png", "icon-512.png", "apple-touch-icon.png"} {
		mux.HandleFunc("GET /"+png, file(png, "image/png"))
	}
	mux.Handle("POST /api/pair", sameOrigin(http.HandlerFunc(s.pair)))
	if s.Tabs != nil {
		mux.Handle("GET /api/stream", sameOrigin(s.authed(s.stream)))
		mux.Handle("GET /api/chat", sameOrigin(s.authed(s.older)))
		mux.Handle("POST /api/send", sameOrigin(s.authed(s.sendText)))
		mux.Handle("POST /api/command", sameOrigin(s.authed(s.command)))
		mux.Handle("POST /api/queue", sameOrigin(s.authed(s.queue)))
		if s.OpenDesktop != nil {
			mux.Handle("POST /api/open", sameOrigin(s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
				var req struct {
					Pane string `json:"pane"`
				}
				if !decode(w, r, &req) {
					return
				}
				if err := s.OpenDesktop(req.Pane); err != nil {
					http.Error(w, err.Error(), http.StatusConflict)
					return
				}
				writeJSON200(w, map[string]any{})
			})))
		}
		if s.Question != nil {
			mux.Handle("POST /api/question", sameOrigin(s.authed(s.question)))
		}
		if s.Screen != nil {
			mux.Handle("GET /api/screen", sameOrigin(s.authed(s.screen)))
		}
		if s.Key != nil {
			mux.Handle("POST /api/key", sameOrigin(s.authed(s.key)))
		}
		if s.Stop != nil {
			mux.Handle("POST /api/stop", sameOrigin(s.authed(s.stop)))
		}
		if s.Upload != nil {
			mux.Handle("POST /api/upload", sameOrigin(s.authed(s.upload)))
		}
	}
	if s.Start != nil {
		mux.Handle("GET /api/new", sameOrigin(s.authed(s.newOptions)))
		mux.Handle("POST /api/new", sameOrigin(s.authed(s.startTab)))
	}
	if s.CloseTab != nil {
		mux.Handle("POST /api/close", sameOrigin(s.authed(tabAction("tab", "close", s.CloseTab))))
	}
	if s.Restart != nil {
		mux.Handle("POST /api/restart", sameOrigin(s.authed(tabAction("pane", "restart", s.Restart))))
	}
	if s.Icons != nil {
		mux.Handle("GET /api/icon/{hash}", sameOrigin(s.authed(s.Icons.serve)))
	}
	if s.Folder != nil {
		mux.Handle("GET /api/file", sameOrigin(s.authed(s.file)))
	}
	mux.Handle("GET /api/items", sameOrigin(s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
		writeJSON200(w, map[string]any{"items": s.Items(), "device": d.Name, "web": s.Web != nil})
	})))
	if s.Web != nil {
		mux.Handle("POST /api/web", sameOrigin(s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
			s.Web.Start(w, r, d.ID)
			log.Printf("remote: %q opened the web client", d.Name)
			writeJSON200(w, map[string]any{"url": "/app/"})
		})))
		mux.Handle("GET /app/", http.StripPrefix("/app", s.Web.Bundle()))
		mux.Handle("GET /_pitwall/bootstrap", sameOrigin(http.HandlerFunc(s.Web.Bootstrap)))
		mux.Handle("GET /ws", sameOrigin(s.WS))
	}
	mux.Handle("GET /api/push", sameOrigin(s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
		key, err := VAPIDKey(s.Dir)
		if err != nil {
			log.Printf("remote: push key: %q", err)
			http.Error(w, "push is unavailable", http.StatusInternalServerError)
			return
		}
		writeJSON200(w, map[string]any{"key": VAPIDPublic(key), "subscribed": Subscribed(s.Dir, d.ID)})
	})))
	mux.Handle("POST /api/push", sameOrigin(s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
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
	})))
	mux.Handle("POST /api/answer", sameOrigin(s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
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
	})))
	mux.Handle("POST /api/reply", sameOrigin(s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
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
	})))
	mux.Handle("POST /api/approve", sameOrigin(s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
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
		if err := s.Approve(req.Chat, req.Request, req.Decision, req.Reason); err != nil {
			log.Printf("remote: %q: %s of chat %s refused: %q", d.Name, req.Decision, req.Chat, err)
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		log.Printf("remote: %q: %s sent to chat %s", d.Name, req.Decision, req.Chat) // never the reason
		writeJSON200(w, map[string]any{})
	})))
	mux.Handle("POST /api/input", sameOrigin(s.authed(func(w http.ResponseWriter, r *http.Request, d Device) {
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
	})))
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
		hd.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data: blob:; manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
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
		if !sameOriginRequest(r) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// sameOriginRequest reports whether r does not come from another origin.
// Non-browser clients omit Origin and Sec-Fetch-Site and pass.
func sameOriginRequest(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host || u.Scheme != "http" && u.Scheme != "https" {
			return false
		}
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		return false
	}
	return true
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

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

// limited reports whether attempts from r's client IP are refused now,
// dropping old failures. Callers hold s.mu.
func (s *Server) limited(r *http.Request) bool {
	ip := clientIP(r)
	now := s.now()
	fs := s.fails[ip]
	for len(fs) > 0 && now.Sub(fs[0]) >= failWindow {
		fs = fs[1:]
	}
	if len(fs) == 0 {
		delete(s.fails, ip)
		return false
	}
	s.fails[ip] = fs
	return len(fs) >= maxFails
}

// fail counts a failed attempt for r's client IP. Callers hold s.mu.
func (s *Server) fail(what string, r *http.Request) {
	ip := clientIP(r)
	now := s.now()
	if s.fails == nil {
		s.fails = map[string][]time.Time{}
	}
	if len(s.fails) >= 256 {
		for k, fs := range s.fails {
			for len(fs) > 0 && now.Sub(fs[0]) >= failWindow {
				fs = fs[1:]
			}
			if len(fs) == 0 {
				delete(s.fails, k)
			} else {
				s.fails[k] = fs
			}
		}
	}
	s.fails[ip] = append(s.fails[ip], now)
	if ok, held := failLog.Allow(what, now); ok {
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
		auth := r.Header.Get("Authorization")
		token, hasBearer := strings.CutPrefix(auth, "Bearer ")
		// Without a paired-device token, fall back to Tailscale WhoIs when configured.
		if (!hasBearer || token == "") && s.Authorize != nil {
			// why: Tailscale identity needs no Authorization header, so enforce same-origin to stop cross-site requests.
			if !sameOriginRequest(r) {
				http.Error(w, "forbidden origin", http.StatusForbidden)
				return
			}
			d, code, msg := s.Authorize(r)
			if code != 0 {
				http.Error(w, msg, code)
				return
			}
			h(w, r, d)
			return
		}
		s.mu.Lock()
		if s.limited(r) {
			s.mu.Unlock()
			http.Error(w, errLimited.Error(), http.StatusTooManyRequests)
			return
		}
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

func (s *Server) deviceLive(d Device) bool {
	if s.DeviceLive != nil {
		return s.DeviceLive(d)
	}
	return Paired(s.Dir, d.ID)
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
	if s.limited(r) {
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
