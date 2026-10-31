package daemon

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/eventlog"
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

// Read reads a live chat on its loop, else an archived one from its
// folder, which always answers with its snapshot.
func (h chatHub) Read(id string, after int64) (out proto.ChatDeltas, err error) {
	err = h.on(id, func(c *chat) { out = c.read(after) })
	if chatCode(err) != "not-found" || h.d.o.Chats == "" {
		return out, err
	}
	_, s, seq, rerr := eventlog.ReadArchived(h.d.o.Chats, id)
	if rerr != nil {
		if !errors.Is(rerr, fs.ErrNotExist) {
			log.Printf("chat %s: read archived: %q", id, rerr)
		}
		return out, err
	}
	snap, _ := json.Marshal(s)
	return proto.ChatDeltas{Chat: id, Seq: seq, Snapshot: snap}, nil
}

// Archived lists the archived chats from their config.json alone.
func (h chatHub) Archived() []model.Chat {
	if h.d.o.Chats == "" {
		return nil
	}
	var out []model.Chat
	for _, c := range eventlog.Archived(h.d.o.Chats) {
		out = append(out, model.Chat{ID: c.ID, WorkspaceID: c.Workspace, Provider: c.Provider, Model: c.Model, Effort: c.Effort, Mode: c.Mode,
			Title: c.Title, State: session.StateReady, UpdatedAt: cmp.Or(c.Updated, c.Created)})
	}
	slices.SortFunc(out, func(a, b model.Chat) int { return cmp.Or(b.UpdatedAt.Compare(a.UpdatedAt), cmp.Compare(a.ID, b.ID)) })
	return out
}

// Import stores an archived chat from aide's store, unless a live chat
// has its id.
func (h chatHub) Import(cfg session.Config, s session.Snapshot) (bool, error) {
	if h.d.o.Chats == "" {
		return false, chatError{"unsupported", "this daemon keeps no chats on disk"}
	}
	if _, err := h.d.chat(cfg.ID); err == nil {
		return false, nil
	}
	ok, err := eventlog.Import(h.d.o.Chats, cfg, s)
	if err != nil {
		return false, invalid("import %s: %v", cfg.ID, err)
	}
	return ok, nil
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

// Do runs m as the socket does. Its context is not the connection's:
// a turn a web client starts outlives the client.
func (h chatHub) Do(m any) any {
	r, _ := h.d.chatRequest(context.Background(), m)
	return r
}

func (h chatHub) Workspaces() []model.Workspace {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	return slices.Clone(h.d.st.Workspaces)
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
