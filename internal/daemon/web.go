package daemon

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/forge"
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
	ws := &web.Server{Version: Build, Token: token, Chats: chatHub{d}, Usage: d.o.Usage}
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

// usageReader reads what Settings, Usage shows: the limits as the GUI's
// meter reads them, from 8 days of Codex rollouts, and the calls in every
// agent's session files. Its scanners keep what they read, so the next
// call reads only what the agents appended since.
func usageReader() func(since time.Time) web.Usage {
	var mu sync.Mutex
	var limits flow.ProfileScanner
	var calls flow.Scanner
	return func(since time.Time) web.Usage {
		mu.Lock()
		defer mu.Unlock()
		home, _ := os.UserHomeDir()
		cfg, _ := config.Load()
		return web.Usage{
			Limits:   limits.Read(home, config.StateDir(), cfg.Profiles, time.Now(), 8),
			Records:  calls.Scan(flow.Sources(home), since),
			ShowCost: cfg.ShowCost,
		}
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
			Title: c.Title, RecentPrompt: c.RecentPrompt, State: session.StateReady, UpdatedAt: cmp.Or(c.Updated, c.Created)})
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

var _ web.Forge = chatHub{}

func (h chatHub) PRs() map[string]model.PR {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	return maps.Clone(h.d.st.PRs)
}

// pr is the folder and pull request of tab ws, or why there is none.
func (h chatHub) pr(ws string) (string, model.PR, error) {
	h.d.mu.Lock()
	defer h.d.mu.Unlock()
	w := h.d.workspace(ws)
	pr, ok := h.d.st.PRs[ws]
	if w == nil || !ok {
		return "", model.PR{}, chatError{"not-found", "this tab's branch has no pull request"}
	}
	return h.d.st.LivePath(*w), pr, nil
}

func (h chatHub) Reviews(ctx context.Context, ws string) ([]forge.Thread, []forge.Comment, error) {
	if h.d.o.Reviews == nil {
		return nil, nil, chatError{"unsupported", "this pitwall cannot read reviews"}
	}
	dir, pr, err := h.pr(ws)
	if err != nil {
		return nil, nil, err
	}
	threads, changes, err := h.d.o.Reviews(ctx, dir, pr.Number)
	return threads, changes, forgeError(err)
}

func (h chatHub) Prompt(ctx context.Context, ws, what string) (string, error) {
	if h.d.o.Reviews == nil || h.d.o.Failures == nil {
		return "", chatError{"unsupported", "this pitwall cannot read checks or reviews"}
	}
	dir, pr, err := h.pr(ws)
	if err != nil {
		return "", err
	}
	act := "send_review"
	if what == "failures" {
		act = "send_failures"
	}
	text, err := forge.SendPrompt(ctx, dir, pr.Number, act, h.d.o.Failures, h.d.o.Reviews)
	return text, forgeError(err)
}

func (h chatHub) Merge(ctx context.Context, ws string) error {
	if h.d.o.Merge == nil {
		return chatError{"unsupported", "this pitwall cannot merge"}
	}
	dir, pr, err := h.pr(ws)
	if err != nil {
		return err
	}
	if pr.State != model.PROpen {
		return chatError{"conflict", fmt.Sprintf("pull request #%d is %s", pr.Number, pr.State)}
	}
	if err := h.d.o.Merge(ctx, dir, pr.Number); err != nil {
		return forgeError(err)
	}
	h.d.mu.Lock()
	h.d.pokePR(ws)
	h.d.mu.Unlock()
	return nil
}

// forgeError is a failed gh call as the web answers it: code forge, with
// gh's words.
func forgeError(err error) error {
	if err == nil {
		return nil
	}
	return chatError{"forge", err.Error()}
}
