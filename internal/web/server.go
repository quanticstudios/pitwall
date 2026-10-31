package web

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	// Protocol is the version of docs/web-protocol.md this server speaks.
	Protocol = 1
	// DeltaVersion is aide's SESSION_DELTA_VERSION, in every delta.
	DeltaVersion = 1
)

var helloTimeout = 10 * time.Second // a var for tests

// Server serves the web protocol at /ws: it checks Host and Origin before
// the upgrade, then the token in hello. Commands other than the
// subscriptions get unsupported until ENG-2696 adds them.
type Server struct {
	// Token is the token hello must carry. It is read on every hello, so
	// a rotated token works at once. nil takes only Client's tokens.
	Token   func() (string, error)
	Version string // pitwall's, for welcome
	// Client, when set, is the bundle Handler serves at /, and hello also
	// takes the tokens its sessions were given.
	Client *Client
	// OpenPath is the code file /_pitwall/open redeems for a session.
	OpenPath string
	Chats    Chats // what subscriptions stream; nil serves none
	// Usage, when set, answers readUsage with the plan limits and
	// the calls since since.
	Usage func(since time.Time) Usage

	mu     sync.Mutex
	conns  map[*conn]bool
	closed bool
}

// Handler serves /ws, and Client's bundle, behind the Host and Origin
// checks.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", s.ws)
	if s.Client != nil {
		mux.Handle("GET /", s.Client.Bundle())
		mux.HandleFunc("GET /_pitwall/bootstrap", s.Client.Bootstrap)
		mux.HandleFunc("GET /_pitwall/open", s.open)
	}
	return local(mux)
}

// WebSocket serves the web protocol without Handler's Host and Origin
// checks, for a server that makes its own, such as the phone page.
func (s *Server) WebSocket() http.Handler { return http.HandlerFunc(s.ws) }

// open trades the one-time code pitwall web open put in the URL for a
// session cookie, and sends the browser on to /, so the page it shows has
// no code in its URL. The code is never logged.
func (s *Server) open(w http.ResponseWriter, r *http.Request) {
	tok := ""
	if s.Token != nil {
		tok, _ = s.Token()
	}
	if tok == "" || redeem(s.OpenPath, r.URL.Query().Get("code"), time.Now()) != nil {
		log.Printf("web: a bad or used open code from %s refused", r.RemoteAddr)
		http.Error(w, "this link opens pitwall once, within a minute: run pitwall web open again", http.StatusUnauthorized)
		return
	}
	s.Client.Start(w, r, tok)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	log.Printf("web: browser session started from %s", r.RemoteAddr)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// local refuses, with 403, a request whose Host is not this listener's
// loopback address (DNS rebinding) or whose Origin is set and is not this
// listener (a page on another site). It sets no CORS headers.
func local(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		addr, _ := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		port := ""
		if addr != nil {
			_, port, _ = net.SplitHostPort(addr.String())
		}
		if port == "" || r.Host != "127.0.0.1:"+port && r.Host != "localhost:"+port {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://127.0.0.1:"+port && o != "http://localhost:"+port {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) ws(w http.ResponseWriter, r *http.Request) {
	c, err := upgrade(w, r)
	if err != nil {
		return
	}
	if !s.add(c) {
		c.close(1001, "daemon exiting")
		return
	}
	defer s.remove(c)
	name, err := s.hello(c)
	if err != nil {
		var ce closeErr
		if errors.As(err, &ce) {
			log.Printf("web: %s refused: %q", r.RemoteAddr, ce.reason)
			c.close(ce.code, ce.reason)
		}
		return
	}
	log.Printf("web: %q connected from %s", name, r.RemoteAddr)
	c.name = name
	done := make(chan struct{})
	go func() {
		err := s.writer(c, done)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			c.close(1008, "client too slow")
		} else if err != nil {
			c.nc.Close()
		}
	}()
	err = s.serve(c)
	close(done)
	if s.Chats != nil {
		s.unsubscribeAll(c)
	}
	log.Printf("web: %q disconnected: %v", name, err)
}

func (s *Server) add(c *conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if s.conns == nil {
		s.conns = map[*conn]bool{}
	}
	s.conns[c] = true
	return true
}

func (s *Server) remove(c *conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	c.nc.Close()
}

// Close ends every connection with 1001 and refuses new ones.
func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for c := range conns {
		c.close(1001, "daemon exiting")
	}
}

type hello struct {
	Type     string `json:"type"`
	Protocol int    `json:"protocol"`
	Token    string `json:"token"`
	Client   struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"client"`
}

// hello reads the client's hello, checks its token, then its protocol,
// and answers welcome. It returns the client's name.
func (s *Server) hello(c *conn) (string, error) {
	c.nc.SetReadDeadline(time.Now().Add(helloTimeout))
	msg, err := c.read()
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return "", closeErr{4003, "no hello within 10s"}
	}
	if err != nil {
		return "", err
	}
	var h hello
	if json.Unmarshal(msg, &h) != nil || h.Type != "hello" {
		return "", closeErr{4000, "expected hello"}
	}
	if !s.check(h.Token) {
		return "", closeErr{4001, "bad token: read web.json again"}
	}
	if h.Protocol != Protocol {
		return "", closeErr{4002, fmt.Sprintf("pitwall speaks protocol %d, client sent %d: update aide or pitwall", Protocol, h.Protocol)}
	}
	c.nc.SetReadDeadline(time.Time{})
	err = s.send(c, map[string]any{
		"type": "welcome", "protocol": Protocol, "deltaVersion": DeltaVersion,
		"server":       map[string]string{"name": "pitwall", "version": s.Version},
		"capabilities": s.capabilities(),
	})
	return h.Client.Name, err
}

// check reports whether hello's token is web.json's or a session's.
func (s *Server) check(token string) bool {
	if s.Client != nil && s.Client.Check(token) {
		return true
	}
	if s.Token == nil {
		return false
	}
	want, err := s.Token()
	if err != nil {
		log.Printf("web: token: %q", err)
	}
	return err == nil && want != "" && subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1
}

// capabilities are the commands this server accepts.
func (s *Server) capabilities() []string {
	out := []string{}
	if s.Chats != nil {
		out = append([]string{"subscribeSessions", "unsubscribeSessions", "subscribe", "unsubscribe"}, commands...)
	}
	if s.Usage != nil {
		out = append(out, "readUsage")
	}
	if _, ok := s.Chats.(Forge); ok {
		out = append(out, forgeCommands...)
	}
	return out
}

// serve reads commands until the connection ends; the writer sends what
// they answer.
func (s *Server) serve(c *conn) error {
	for {
		msg, err := c.read()
		var ce closeErr
		if errors.As(err, &ce) {
			c.close(ce.code, ce.reason)
			return err
		}
		if err != nil {
			return err
		}
		var f struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		switch {
		case json.Unmarshal(msg, &f) != nil:
			c.reply(map[string]any{"type": "error", "code": "invalid", "message": "frame is not a JSON object"})
		case f.ID == "":
			c.reply(map[string]any{"type": "error", "code": "invalid", "message": "frame has no id"})
		case s.Chats != nil && (s.command(c, f.Type, f.ID, msg) || s.chatCommand(c, f.Type, f.ID, msg) || s.forgeCommand(c, f.Type, f.ID, msg)):
		case s.usage(c, f.Type, f.ID, msg):
		default:
			c.reply(result(f.ID, "unsupported", fmt.Sprintf("pitwall does not serve %q yet", f.Type)))
		}
	}
}

func (s *Server) send(c *conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.write(opText, data)
}
