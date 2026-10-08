package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestUsageWatch: a tab's card starts one watch per agent pane, once; the
// usages add up; a new session file restarts that pane's watch and drops
// its stale usage; prune stops the watches of panes that went.
func TestUsageWatch(t *testing.T) {
	type watch struct {
		ctx     context.Context
		path    string
		changed func(flow.Feed)
	}
	var mu sync.Mutex
	var watches []watch
	old := watchFeed
	t.Cleanup(func() { watchFeed = old })
	watchFeed = func(ctx context.Context, _ model.Provider, path string, changed func(flow.Feed)) {
		mu.Lock()
		watches = append(watches, watch{ctx, path, changed})
		mu.Unlock()
	}
	started := func() []watch {
		mu.Lock()
		defer mu.Unlock()
		return append([]watch(nil), watches...)
	}
	wait := func(n int) []watch {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(time.Millisecond) {
			if w := started(); len(w) >= n {
				return w
			}
		}
		t.Fatalf("%d watches started, want %d", len(started()), n)
		return nil
	}
	// The watches start in their own goroutines, in any order.
	byPath := func(ws []watch, path string) watch {
		t.Helper()
		for _, w := range ws {
			if w.path == path {
				return w
			}
		}
		t.Fatalf("no watch on %s", path)
		return watch{}
	}
	feed := func(model string, in int64) flow.Feed {
		return flow.Feed{Usage: flow.Usage{Model: model, Models: map[string]flow.Tokens{model: {Input: in}}}}
	}

	st := &model.State{Panes: []model.Pane{
		{ID: "a", WorkspaceID: "ws", Provider: model.ProviderClaude, Transcript: "/t/a.jsonl"},
		{ID: "b", WorkspaceID: "ws", Provider: model.ProviderCodex, Transcript: "/t/b.jsonl"},
		{ID: "c", WorkspaceID: "ws"}, // a shell
		{ID: "d", WorkspaceID: "other", Provider: model.ProviderPi, Transcript: "/t/d.jsonl"},
	}}
	var w usageWatch
	defer w.prune(nil)
	if u := w.of(st, "ws", func() {}); u != nil {
		t.Fatalf("usage before any read: %+v", u)
	}
	w.of(st, "ws", func() {})
	ws := wait(2)
	a, b := byPath(ws, "/t/a.jsonl"), byPath(ws, "/t/b.jsonl")
	a.changed(feed("claude-opus-5-5", 10))
	b.changed(feed("gpt-6.1-sol", 5))
	u := w.of(st, "ws", func() {})
	if u == nil || u.Tokens().Input != 15 || len(u.Models) != 2 {
		t.Fatalf("usage = %+v, want both panes added up", u)
	}

	st.Panes[0].Transcript = "/t/a2.jsonl"
	if u := w.of(st, "ws", func() {}); u == nil || u.Tokens().Input != 5 {
		t.Fatalf("usage = %+v, want only b's while a's new file is read", u)
	}
	a2 := byPath(wait(3), "/t/a2.jsonl")
	if a.ctx.Err() == nil {
		t.Fatal("a new file left the old watch running")
	}
	a.changed(feed("claude-opus-5-5", 1000))
	if u := w.of(st, "ws", func() {}); u.Tokens().Input != 5 {
		t.Fatalf("a stale watch's usage landed: %+v", u)
	}

	st.Panes = st.Panes[1:]
	w.prune(st)
	if a2.ctx.Err() == nil || b.ctx.Err() != nil {
		t.Fatal("prune stopped the wrong watches")
	}
	if n := len(started()); n != 3 {
		t.Fatalf("%d watches, want no new one", n)
	}
}
