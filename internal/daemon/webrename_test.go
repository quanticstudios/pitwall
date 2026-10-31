package daemon

import (
	"path/filepath"
	"testing"

	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/session/eventlog"
	"github.com/quanticstudios/pitwall/internal/session/fake"
)

// TestWebRenamePin renames and pins a chat over the web: the list shows
// both, a daemon restart keeps both, a second chat cannot take the name,
// and the archived list keeps the pin and the title.
func TestWebRenamePin(t *testing.T) {
	root := t.TempDir()
	f := &fakes{statsCalls: map[string]int{}}
	opts := func(o *Options) {
		chatProvider(fake.Options{})(o)
		o.Chats = root
		o.Gates = decide.FakeGates()
	}
	d, _, stop := runDaemon(t, f, opts)
	c := dialWeb(t, webOn(t, d), "aide")
	c.call(map[string]any{"type": "setJev", "apiKey": "synthetic-e2e-key"})
	r := c.call(map[string]any{"type": "start", "dir": t.TempDir(), "provider": "claude", "prompt": "initialize shared chat"})
	id, _ := at(r, "data.sessionId").(string)
	if id == "" {
		t.Fatalf("start: %v", r)
	}
	if r := c.call(map[string]any{"type": "rename", "sessionId": id, "title": "Daily review"}); r["ok"] != true {
		t.Fatalf("rename: %v", r)
	}
	if r := c.call(map[string]any{"type": "pin", "sessionId": id, "pinned": true}); r["ok"] != true {
		t.Fatalf("pin: %v", r)
	}
	listed := func(c *wsClient, archived bool) map[string]any {
		r := c.call(map[string]any{"type": "listSessions", "archived": archived})
		chats, _ := at(r, "data.chats").([]any)
		for _, ch := range chats {
			if m, _ := ch.(map[string]any); m["id"] == id {
				return m
			}
		}
		t.Fatalf("chat %s not listed: %v", id, r)
		return nil
	}
	if m := listed(c, false); m["title"] != "Daily review" || m["pinned"] != true {
		t.Fatalf("listed after rename and pin: %v", m)
	}
	waitUntil(t, "pin saved", func() bool {
		cfg, err := eventlog.ReadConfig(filepath.Join(root, id))
		return err == nil && cfg.Pinned
	})
	stop()

	d, _, stop = runDaemon(t, f, opts)
	t.Cleanup(stop)
	c = dialWeb(t, webOn(t, d), "aide")
	c.call(map[string]any{"type": "setJev", "apiKey": "synthetic-e2e-key"})
	if m := listed(c, false); m["title"] != "Daily review" || m["pinned"] != true {
		t.Fatalf("listed after a restart: %v", m)
	}
	r = c.call(map[string]any{"type": "start", "dir": t.TempDir(), "provider": "claude", "prompt": "another"})
	other, _ := at(r, "data.sessionId").(string)
	if r := c.call(map[string]any{"type": "rename", "sessionId": other, "title": "Daily review"}); r["code"] != "conflict" {
		t.Fatalf("rename to a taken name: %v", r)
	}
	if r := c.call(map[string]any{"type": "pin", "sessionId": "nope", "pinned": true}); r["code"] != "not-found" {
		t.Fatalf("pin of no chat: %v", r)
	}

	// A pinned chat archives: the pin is the chat's, not the tab's.
	if r := c.call(map[string]any{"type": "archive", "sessionId": id}); r["ok"] != true {
		t.Fatalf("archive: %v", r)
	}
	waitUntil(t, "archived", func() bool {
		cfg, err := eventlog.ReadConfig(filepath.Join(root, "archive", id))
		return err == nil && cfg.Title == "Daily review"
	})
	if m := listed(c, true); m["title"] != "Daily review" || m["pinned"] != true {
		t.Fatalf("archived: %v", m)
	}
}
