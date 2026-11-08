package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
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
	d, f := talkDaemon(t)
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
	waitUntil(t, "window message submitted", func() bool { return strings.HasSuffix(f.pane(0).got(), "\r") })
	claudeHook(t, d, "b", `{"hook_event_name":"UserPromptSubmit","prompt":"newer"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"review"}`)
	if reportedPane(d, "a").LastPromptAt != before {
		t.Fatal("window message's later hook changed destination")
	}
}

func TestLastPromptCLISubmission(t *testing.T) {
	for _, from := range []string{"", "b"} {
		t.Run("sender="+from, func(t *testing.T) {
			d, f := talkDaemon(t)
			t.Cleanup(d.shutdown)
			claudeHook(t, d, "a", `{"hook_event_name":"Stop"}`)
			d.mu.Lock()
			d.st.Panes[0].LastPromptAt = time.Time{}
			d.mu.Unlock()
			must(t, d.handle(t.Context(), proto.Send{Pane: "a", From: from, Text: "review"}))
			if got := reportedPane(d, "a").LastPromptAt.IsZero(); got != (from != "") {
				t.Fatalf("sender %q: zero time = %v", from, got)
			}
			before := reportedPane(d, "a").LastPromptAt
			waitUntil(t, "CLI message submitted", func() bool { return strings.HasSuffix(f.pane(0).got(), "\r") })
			claudeHook(t, d, "b", `{"hook_event_name":"UserPromptSubmit","prompt":"newer"}`)
			claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"review"}`)
			if reportedPane(d, "a").LastPromptAt != before {
				t.Fatal("CLI message's hook changed destination")
			}
			d.noteUserKey("a", []byte("\r"))
			claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"my next prompt"}`)
			if !reportedPane(d, "a").LastPromptAt.After(reportedPane(d, "b").LastPromptAt) {
				t.Fatal("user's next typed prompt did not change destination")
			}
		})
	}
}

func TestLastPromptAutomatedSubmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(*testing.T, *Daemon)
	}{
		{"CI failures", func(t *testing.T, d *Daemon) {
			claudeHook(t, d, "a", `{"hook_event_name":"Stop"}`)
			must(t, d.sendMessage(proto.Send{Pane: "a", Text: "fix CI"}, "Sent CI failures to ", true))
		}},
		{"fleet notes", func(t *testing.T, d *Daemon) {
			claudeHook(t, d, "a", `{"hook_event_name":"Stop"}`)
			d.sendNotes([]note{{pane: "a", text: "worker is past its timebox"}})
		}},
		{"agent prompt", func(t *testing.T, d *Daemon) {
			claudeHook(t, d, "a", `{"hook_event_name":"Stop"}`)
			must(t, d.prompt(proto.Prompt{Pane: "a", Text: "review"}))
		}},
		{"quiet nudge", func(t *testing.T, d *Daemon) {
			d.o.QuietAfter = func() time.Duration { return time.Minute }
			d.o.ContinueWhenQuiet = func() bool { return true }
			d.checkQuiet()
			age(d, "a", 2*time.Minute)
			d.checkQuiet()
		}},
		{"usage-limit resume", func(t *testing.T, d *Daemon) {
			clock := useFakeClock(t, time.Date(2026, 10, 22, 13, 20, 0, 0, time.UTC))
			d.o.ResumeAfterLimit = func() bool { return true }
			claudeHook(t, d, "a", claudeLimit)
			clock.fire()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, f := talkDaemon(t)
			t.Cleanup(d.shutdown)
			claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"mine"}`)
			before := reportedPane(d, "a").LastPromptAt
			tc.send(t, d)
			waitUntil(t, "automatic message submitted", func() bool { return strings.HasSuffix(f.pane(0).got(), "\r") })
			claudeHook(t, d, "b", `{"hook_event_name":"UserPromptSubmit","prompt":"newer"}`)
			// Drafting a reply must not attribute the automatic message's hook to the user.
			d.noteUserKey("a", []byte("draft"))
			for _, payload := range []string{
				`{"hook_event_name":"UserPromptSubmit","prompt":"automatic"}`,
				`{"hook_event_name":"BeforeAgent","prompt":"automatic"}`,
				`{"event":"before_agent_start","prompt":"automatic"}`,
			} {
				claudeHook(t, d, "a", payload)
				if reportedPane(d, "a").LastPromptAt != before {
					t.Fatalf("automatic message's hook changed destination: %s", payload)
				}
			}
		})
	}
}

func TestLastPromptPhoneSubmission(t *testing.T) {
	d, f := talkDaemon(t)
	t.Cleanup(d.shutdown)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop"}`)
	must(t, d.remoteReply("a", d.activityOf("a").UpdatedAt.UnixNano(), "review"))
	before := reportedPane(d, "a").LastPromptAt
	if before.IsZero() {
		t.Fatal("phone submission did not change destination")
	}
	waitUntil(t, "phone message submitted", func() bool { return strings.HasSuffix(f.pane(0).got(), "\r") })
	claudeHook(t, d, "b", `{"hook_event_name":"UserPromptSubmit","prompt":"newer"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"review"}`)
	if reportedPane(d, "a").LastPromptAt != before {
		t.Fatal("phone message's later hook changed destination")
	}
}

func TestLastPromptEnterFailure(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			d, f := talkDaemon(t)
			t.Cleanup(d.shutdown)
			claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"mine"}`)
			before := reportedPane(d, "a").LastPromptAt
			if pending {
				must(t, d.typeLine("a", f.pane(0), "automatic", "empty"))
			}
			d.mu.Lock()
			in := d.inputs["a"]
			d.inputs["a"] = nil
			d.mu.Unlock()
			err := d.pressEnter("a", f.pane(0), "")
			d.mu.Lock()
			d.inputs["a"] = in
			d.mu.Unlock()
			if err == nil {
				t.Fatal("Enter accepted without an input reader")
			}
			claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"next"}`)
			if got := reportedPane(d, "a").LastPromptAt; (got == before) != pending {
				t.Fatalf("failed Enter changed prompt attribution: pending=%v, before=%v, after=%v", pending, before, got)
			}
		})
	}
}
