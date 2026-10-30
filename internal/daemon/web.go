package daemon

import (
	"context"
	"log"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/web"
)

// webLoop serves the web protocol while [web] enables it.
func (d *Daemon) webLoop(ctx context.Context) {
	if d.o.Web == nil {
		return
	}
	watchListener(ctx, d.o.Web, func(s config.WebSettings) bool { return s.Enabled }, d.serveWeb)
}

// webFails keeps a web listener that cannot start to a log line a minute.
var webFails = logs.Limiter{Every: time.Minute}

// serveWeb listens on 127.0.0.1, writes web.json for clients to find it,
// and returns what stops it, or nil after a failure, which is logged once
// a minute.
func (d *Daemon) serveWeb(s config.WebSettings) (stop func()) {
	ln, err := web.Listen(s.Port)
	if err != nil {
		if ok, _ := webFails.Allow("listen", time.Now()); ok {
			log.Printf("web: %q", err)
		}
		return nil
	}
	if _, err := web.Publish(d.o.WebPath, ln.Addr(), os.Getpid()); err != nil {
		ln.Close()
		if ok, _ := webFails.Allow("publish", time.Now()); ok {
			log.Printf("web: %q", err)
		}
		return nil
	}
	token := func() (string, error) {
		f, err := web.Read(d.o.WebPath)
		return f.Token, err
	}
	ws := &web.Server{Version: Build, Token: token, Chats: chatHub{d}}
	if s.Bundle != "" {
		// A session lives while web.json keeps the token it started under.
		ws.Client = &web.Client{Dir: s.Bundle, Live: func(owner string) bool {
			t, err := token()
			return err == nil && t == owner
		}}
		ws.OpenPath = web.OpenPath(d.o.WebPath)
	}
	srv := &http.Server{Handler: ws.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	log.Printf("web: serving ws://%s/ws, token in %s", ln.Addr(), d.o.WebPath)
	if ws.Client != nil {
		log.Printf("web: serving the web client in %s at http://%s/", s.Bundle, ln.Addr())
	}
	return func() {
		ws.Close()
		srv.Close()
		log.Printf("web: stopped serving %s", ln.Addr())
	}
}

// chatHub is the chats as the web server streams them: each chat's loop
// pushes its deltas to its subscribers, and changed tells the list's.
type chatHub struct{ d *Daemon }

func (h chatHub) Subscribe(id string, s web.Sink) error {
	return h.on(id, func(c *chat) {
		if c.subs == nil {
			c.subs = map[web.Sink]bool{}
		}
		c.subs[s] = true
	})
}

func (h chatHub) Unsubscribe(id string, s web.Sink) {
	h.on(id, func(c *chat) { delete(c.subs, s) })
}

func (h chatHub) Read(id string, after int64) (out proto.ChatDeltas, err error) {
	err = h.on(id, func(c *chat) { out = c.read(after) })
	return out, err
}

// on runs f on chat id's loop.
func (h chatHub) on(id string, f func(*chat)) error {
	c, err := h.d.chat(id)
	if err != nil {
		return err
	}
	if !c.do(func() { f(c) }) {
		return chatError{"not-found", "chat " + id + " is closed"}
	}
	return nil
}

func (h chatHub) List() []model.Chat {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	return slices.Clone(h.d.st.Chats)
}

func (h chatHub) WatchList(s web.Sink, on bool) {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	if !on {
		delete(h.d.listSinks, s)
		return
	}
	if h.d.listSinks == nil {
		h.d.listSinks = map[web.Sink]bool{}
	}
	h.d.listSinks[s] = true
}
