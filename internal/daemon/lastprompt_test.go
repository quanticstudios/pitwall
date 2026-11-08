package daemon

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func TestLastPromptVersion18Interoperates(t *testing.T) {
	sock, stop := run(t, &fakes{statsCalls: map[string]int{}})
	defer stop()
	for _, kind := range []string{"gui", "cli"} {
		t.Run(kind, func(t *testing.T) {
			c := dialHello(t, sock, proto.Hello{Version: 18, Level: 64, Kind: kind, Cwd: t.TempDir()})
			c.send(proto.Sync{})
			c.waitFor("state for a client without prompt tracking", func(m any) bool {
				_, ok := m.(proto.StateMsg)
				return ok
			})
		})
	}
}

func TestLastPromptTracking(t *testing.T) {
	d, _ := nestedDaemon(t)
	nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "owner")
	first := reportedPane(d, "a").LastPromptAt
	if first.IsZero() {
		t.Fatal("prompt did not set destination")
	}
	for _, event := range []string{"PreToolUse", "PostToolUse", "PermissionRequest"} {
		nestedHook(t, d, model.ProviderClaude, event, "owner")
		if got := reportedPane(d, "a").LastPromptAt; got != first {
			t.Fatalf("%s changed prompt time", event)
		}
	}
	for _, child := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		nestedHook(t, d, child, "UserPromptSubmit", "child")
		if got := reportedPane(d, "a").LastPromptAt; got != first {
			t.Fatalf("nested %s changed prompt time", child)
		}
	}
	nestedHook(t, d, model.ProviderClaude, "UserPromptSubmit", "owner")
	if got := reportedPane(d, "a").LastPromptAt; !got.After(first) {
		t.Fatal("second prompt did not update destination")
	}
	claudeHook(t, d, "b", `{"hook_event_name":"UserPromptSubmit","prompt":"review"}`)
	if got := reportedPane(d, "b").LastPromptAt; !got.After(first) {
		t.Fatal("other pane prompt did not set destination")
	}
	must(t, d.closePane("b"))
	if got := d.state().Panes; len(got) != 1 || got[0].ID != "a" || got[0].LastPromptAt.IsZero() {
		t.Fatalf("close lost earlier target: %+v", got)
	}
	data, err := json.Marshal(d.state())
	must(t, err)
	var restored model.State
	must(t, json.Unmarshal(data, &restored))
	if !restored.Panes[0].LastPromptAt.IsZero() {
		t.Fatal("prompt time survived saved state")
	}
}

func TestLastPromptWindowSubmission(t *testing.T) {
	d, _ := talkDaemon(t)
	t.Cleanup(d.shutdown)
	must(t, d.windowDeliver(proto.Deliver{Pane: "a", Text: "review"}))
	if reportedPane(d, "a").LastPromptAt.IsZero() {
		t.Fatal("window submission did not set destination")
	}
	before := reportedPane(d, "a").LastPromptAt
	if err := d.windowDeliver(proto.Deliver{Pane: "a"}); err == nil {
		t.Fatal("empty message accepted")
	}
	if reportedPane(d, "a").LastPromptAt != before {
		t.Fatal("rejected message changed destination")
	}
}

func TestLastPromptCLISubmission(t *testing.T) {
	for _, from := range []string{"", "b"} {
		t.Run("sender="+from, func(t *testing.T) {
			d, _ := talkDaemon(t)
			t.Cleanup(d.shutdown)
			claudeHook(t, d, "a", `{"hook_event_name":"Stop"}`)
			d.mu.Lock()
			d.st.Panes[0].LastPromptAt = time.Time{}
			d.mu.Unlock()
			must(t, d.handle(t.Context(), proto.Send{Pane: "a", From: from, Text: "review"}))
			if got := reportedPane(d, "a").LastPromptAt.IsZero(); got != (from != "") {
				t.Fatalf("sender %q: zero time = %v", from, got)
			}
		})
	}
}
