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
// the upgrade, then the token in hello. Every command gets unsupported
// until ENG-2695 and ENG-2696 add them.
type Server struct {
	// Token is the token hello must carry. It is read on every hello, so
	// a rotated token works at once.
	Token   func() (string, error)
	Version string // pitwall's, for welcome

	mu     sync.Mutex
	conns  map[*conn]bool
	closed bool
}

// Handler serves /ws, behind the Host and Origin checks.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", s.ws)
	return local(mux)
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
	err = s.serve(c)
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
	want, err := s.Token()
	if err != nil {
		log.Printf("web: token: %q", err)
	}
	if err != nil || want == "" || subtle.ConstantTimeCompare([]byte(h.Token), []byte(want)) != 1 {
		return "", closeErr{4001, "bad token: read web.json again"}
	}
	if h.Protocol != Protocol {
		return "", closeErr{4002, fmt.Sprintf("pitwall speaks protocol %d, client sent %d: update aide or pitwall", Protocol, h.Protocol)}
	}
	c.nc.SetReadDeadline(time.Time{})
	err = s.send(c, map[string]any{
		"type": "welcome", "protocol": Protocol, "deltaVersion": DeltaVersion,
		"server":       map[string]string{"name": "pitwall", "version": s.Version},
		"capabilities": []string{}, // the commands, as ENG-2695 and ENG-2696 add them
	})
	return h.Client.Name, err
}

// serve reads commands until the connection ends.
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
			err = s.send(c, map[string]any{"type": "error", "code": "invalid", "message": "frame is not a JSON object"})
		case f.ID == "":
			err = s.send(c, map[string]any{"type": "error", "code": "invalid", "message": "frame has no id"})
		default:
			err = s.send(c, map[string]any{"type": "result", "id": f.ID, "ok": false, "code": "unsupported",
				"message": fmt.Sprintf("pitwall does not serve %q yet", f.Type)})
		}
		if err != nil {
			return err
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
