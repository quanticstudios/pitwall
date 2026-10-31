package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/eventlog"
)

// TestWebUnarchive archives a chat and unarchives it at once, while its
// folder may still be moving: it comes back in a new tab with its id and
// transcript, and the next message resumes the same provider session. A
// live chat stays in its tab; an unknown one and a bad id are refused. An
// imported chat unarchives as a live chat an import no longer replaces.
func TestWebUnarchive(t *testing.T) {
	root := t.TempDir()
	spy := &spyProvider{}
	d, _, stop := runDaemon(t, &fakes{statsCalls: map[string]int{}}, func(o *Options) {
		o.Chats = root
		o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: spy}
		o.Gates = decide.FakeGates()
	})
	t.Cleanup(stop)
	addr := webOn(t, d)
	c, other := dialWeb(t, addr, "unarchive"), dialWeb(t, addr, "unarchive-too")
	r := c.call(map[string]any{"type": "start", "dir": t.TempDir(), "provider": "claude", "prompt": "hello"})
	id, _ := at(r, "data.sessionId").(string)
	tab := at(r, "data.workspaceId")
	read := func() map[string]any { return c.call(map[string]any{"type": "read", "sessionId": id}) }
	waitUntil(t, "first turn", func() bool { return at(read(), "data.snapshot.turns.0.state") == "completed" })
	cfg, err := eventlog.ReadConfig(filepath.Join(root, id))
	if err != nil || cfg.Resume == "" {
		t.Fatalf("config after the first turn: %+v, %v", cfg, err)
	}
	if r := c.call(map[string]any{"type": "unarchive", "sessionId": id}); r["ok"] != true || at(r, "data.workspaceId") != tab {
		t.Fatalf("unarchive a live chat: %v", r)
	}

	if r := c.call(map[string]any{"type": "archive", "sessionId": id}); r["ok"] != true {
		t.Fatalf("archive: %v", r)
	}
	again := make(chan map[string]any, 1)
	go func() { again <- other.call(map[string]any{"type": "unarchive", "sessionId": id}) }()
	r = c.call(map[string]any{"type": "unarchive", "sessionId": id})
	back, _ := at(r, "data.workspaceId").(string)
	if r2 := <-again; r2["ok"] != true || at(r2, "data.workspaceId") != back {
		t.Fatalf("a second unarchive at once: %v, the first %v", r2, r)
	}
	if r["ok"] != true || at(r, "data.sessionId") != id || back == "" || back == tab {
		t.Fatalf("unarchive: %v (the old tab was %v)", r, tab)
	}
	if _, err := os.Lstat(filepath.Join(root, "archive", id)); err == nil {
		t.Fatal("the chat is still in the archive")
	}
	if cfg, err := eventlog.ReadConfig(filepath.Join(root, id)); err != nil || cfg.Workspace != back {
		t.Fatalf("config.json after unarchive: workspace %q, %v; want %s", cfg.Workspace, err, back)
	}
	list := c.call(map[string]any{"type": "listSessions"})
	if at(list, "data.chats.0.id") != id || at(list, "data.chats.0.workspaceId") != back {
		t.Fatalf("listSessions: %v", list)
	}
	if r := c.call(map[string]any{"type": "send", "sessionId": id, "text": "again"}); r["ok"] != true {
		t.Fatalf("send after unarchive: %v", r)
	}
	waitUntil(t, "second turn", func() bool { return at(read(), "data.snapshot.turns.1.state") == "completed" })
	if got, want := spy.origins(), []session.Origin{{}, {Resume: cfg.Resume}}; !slices.Equal(got, want) {
		t.Fatalf("starts %+v, want %+v", got, want)
	}
	if got, want := spy.sent(), []string{"hello", "again"}; !slices.Equal(got, want) {
		t.Fatalf("sent %q, want %q", got, want)
	}
	if content := fmt.Sprint(at(read(), "data.snapshot.content")); !strings.Contains(content, "Handled: hello") {
		t.Fatalf("the transcript lost the first turn: %s", content)
	}

	for _, tc := range []struct{ chat, code string }{{"nope", "not-found"}, {"..", "invalid"}} {
		if r := c.call(map[string]any{"type": "unarchive", "sessionId": tc.chat}); r["code"] != tc.code {
			t.Errorf("unarchive %s: %v, want %s", tc.chat, r, tc.code)
		}
	}

	frame := map[string]any{"type": "import", "sessionId": "old-1", "dir": t.TempDir(), "provider": "claude", "title": "Fix the login form",
		"snapshot": importSnapshot("old-1", "Fix the login form")}
	if r := c.call(frame); at(r, "data.imported") != true {
		t.Fatalf("import: %v", r)
	}
	if r := c.call(map[string]any{"type": "unarchive", "sessionId": "old-1"}); r["ok"] != true {
		t.Fatalf("unarchive an imported chat: %v", r)
	}
	if cfg, err := eventlog.ReadConfig(filepath.Join(root, "old-1")); err != nil || cfg.Imported {
		t.Fatalf("an unarchived import's config: %+v, %v", cfg, err)
	}
	if r := c.call(map[string]any{"type": "read", "sessionId": "old-1"}); at(r, "data.snapshot.content.0.text") != "Fix the login form" {
		t.Fatalf("read an unarchived import: %v", r)
	}
	if r := c.call(map[string]any{"type": "archive", "sessionId": "old-1"}); r["ok"] != true {
		t.Fatalf("archive the import again: %v", r)
	}
	waitUntil(t, "old-1 archived", func() bool {
		_, err := os.Lstat(filepath.Join(root, "archive", "old-1"))
		return err == nil
	})
	if r := c.call(frame); at(r, "data.imported") != false {
		t.Fatalf("an import over a chat pitwall archived: %v", r)
	}
}
