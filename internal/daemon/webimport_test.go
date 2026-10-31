package daemon

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/session/eventlog"
	"github.com/quanticstudios/pitwall/internal/session/fake"
	"github.com/quanticstudios/pitwall/internal/web"
)

// importSnapshot is aide's snapshot of an old session, as aide sends it.
func importSnapshot(id, text string) map[string]any {
	return map[string]any{
		"sessionId": id, "paneId": "pane-" + id, "workspaceId": "aide-ws", "provider": "claude",
		"content": []any{map[string]any{"id": "u1", "kind": "user-prompt", "text": text, "provider": "claude", "turnId": "t1", "updatedAt": "2026-09-01T10:00:00.000Z"}},
		"turns":   []any{}, "pendingApprovals": []any{}, "pendingUserInputs": []any{}, "planApproval": nil,
		"sessionState": "stopped", "running": false, "detail": nil, "lastCompletedAt": nil, "lastError": nil, "lastErrorAt": nil,
		"model": nil, "effort": nil, "serviceTier": nil, "permissionMode": "default", "interactionMode": "build", "selectionNotice": nil,
		"runtimeGeneration": 1, "revision": 7, "updatedAt": "2026-09-01T10:00:00.000Z", "fleet": map[string]any{"nodes": []any{}},
	}
}

// TestWebImport imports an old aide session as an archived chat: it lists
// with its title and time, reads back with its transcript and aide's own
// keys, a second import replaces it, and a live chat's id is left alone.
// Archiving a chat keeps its title for the list.
func TestWebImport(t *testing.T) {
	root := t.TempDir()
	d, _, stop := runDaemon(t, &fakes{statsCalls: map[string]int{}}, func(o *Options) {
		chatProvider(fake.Options{})(o)
		o.Chats = root
		o.Gates = decide.FakeGates()
	})
	t.Cleanup(stop)
	ws := &web.Server{Token: func() (string, error) { return webToken, nil }, Chats: chatHub{d}}
	hs := httptest.NewServer(ws.Handler())
	t.Cleanup(func() { ws.Close(); hs.Close() })
	c := dialWeb(t, strings.TrimPrefix(hs.URL, "http://"), "aide")

	frame := map[string]any{"type": "import", "sessionId": "old-1", "dir": "/src/app", "provider": "claude", "model": "claude-opus-5-5",
		"title": "Fix the login form", "createdAt": "2026-08-30T09:00:00.000Z", "updatedAt": "2026-09-01T10:00:00.000Z",
		"snapshot": importSnapshot("old-1", "Fix the login form")}
	if r := c.call(frame); r["ok"] != true || at(r, "data.imported") != true {
		t.Fatalf("import: %v", r)
	}
	r := c.call(map[string]any{"type": "listSessions", "archived": true})
	if at(r, "data.chats.0.id") != "old-1" || at(r, "data.chats.0.title") != "Fix the login form" ||
		at(r, "data.chats.0.updatedAt") != "2026-09-01T10:00:00Z" || at(r, "data.chats.0.workspaceId") != "aide-ws" {
		t.Fatalf("listSessions archived: %v", r)
	}
	r = c.call(map[string]any{"type": "read", "sessionId": "old-1", "after": 0})
	if at(r, "data.snapshot.content.0.text") != "Fix the login form" || at(r, "data.snapshot.fleet") == nil || at(r, "data.seq") != 1.0 {
		t.Fatalf("read: %v", r)
	}

	frame["snapshot"] = importSnapshot("old-1", "Fix the login form, then the signup form")
	if r := c.call(frame); at(r, "data.imported") != true {
		t.Fatalf("import again: %v", r)
	}
	if r := c.call(map[string]any{"type": "read", "sessionId": "old-1", "after": 0}); at(r, "data.snapshot.content.0.text") != "Fix the login form, then the signup form" {
		t.Fatalf("read after a second import: %v", r)
	}

	c.call(map[string]any{"type": "setJev", "apiKey": "synthetic-e2e-key"})
	r = c.call(map[string]any{"type": "start", "dir": t.TempDir(), "provider": "claude", "prompt": "Rename the button"})
	live, _ := at(r, "data.sessionId").(string)
	if live == "" {
		t.Fatalf("start: %v", r)
	}
	frame["sessionId"], frame["snapshot"] = live, importSnapshot(live, "not this")
	if r := c.call(frame); r["ok"] != true || at(r, "data.imported") != false {
		t.Fatalf("import over a live chat: %v", r)
	}
	if _, err := os.Lstat(filepath.Join(root, "archive", live)); err == nil {
		t.Fatal("import over a live chat archived a copy")
	}

	if r := c.call(map[string]any{"type": "archive", "sessionId": live}); r["ok"] != true {
		t.Fatalf("archive: %v", r)
	}
	waitUntil(t, "archived with its title", func() bool {
		cfg, err := eventlog.ReadConfig(filepath.Join(root, "archive", live))
		return err == nil && cfg.Title == "Rename the button" && !cfg.Updated.IsZero()
	})
	if r := c.call(frame); at(r, "data.imported") != false {
		t.Fatalf("import over a chat pitwall archived: %v", r)
	}
	if n := len(at(c.call(map[string]any{"type": "listSessions", "archived": true}), "data.chats").([]any)); n != 2 {
		t.Fatalf("%d archived chats, want 2", n)
	}

	for _, bad := range []map[string]any{
		{"type": "import", "sessionId": "old-2", "provider": "claude", "snapshot": importSnapshot("old-2", "x")},
		{"type": "import", "sessionId": "old-2", "dir": "/src", "provider": "claude", "snapshot": importSnapshot("other", "x")},
		{"type": "import", "sessionId": "old-2", "dir": "/src", "provider": "claude", "snapshot": "x"},
		{"type": "import", "sessionId": "../old-2", "dir": "/src", "provider": "claude", "snapshot": importSnapshot("../old-2", "x")},
	} {
		if r := c.call(bad); r["code"] != "invalid" {
			t.Errorf("%v: %v, want invalid", bad, r)
		}
	}
}
