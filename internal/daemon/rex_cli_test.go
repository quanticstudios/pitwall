//go:build unix

package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestDaemonSendKeyModesAndGuard(t *testing.T) {
	d, f, sock := controlDaemon(t, func(*Options) {})
	c := dialJSON(t, sock)
	c.hello(t)

	p1 := fakeNamed(f, "p1")
	p2 := fakeNamed(f, "p2")

	// 1. Legacy mode: up = \x1b[A, enter = \r, ctrl+c = \x03.
	c.line(t, `{"type":"sendKey","id":"k1","pane":"@p1","keys":["up","enter","ctrl+c"]}`)
	if r := c.next(t); r["ok"] != true {
		t.Fatalf("sendKey legacy failed: %v", r)
	}
	if got, want := p1.got(), "\x1b[A\r\x03"; got != want {
		t.Fatalf("legacy bytes = %q, want %q", got, want)
	}

	// 2. AppCursorKeys mode: up = \x1bOA.
	p1.mu.Lock()
	p1.input = nil
	p1.modes = vt.Modes{AppCursorKeys: true}
	p1.mu.Unlock()
	c.line(t, `{"type":"sendKey","id":"k2","pane":"@p1","keys":["up"]}`)
	if r := c.next(t); r["ok"] != true {
		t.Fatalf("sendKey app cursor failed: %v", r)
	}
	if got, want := p1.got(), "\x1bOA"; got != want {
		t.Fatalf("app cursor bytes = %q, want %q", got, want)
	}

	// 3. Kitty keyboard protocol disambiguate (flag 1): esc = \x1b[27;1u, shift+tab = \x1b[9;2u.
	p1.mu.Lock()
	p1.input = nil
	p1.modes = vt.Modes{KittyKeyboard: 1}
	p1.mu.Unlock()
	c.line(t, `{"type":"sendKey","id":"k3","pane":"@p1","keys":["esc","shift+tab"]}`)
	if r := c.next(t); r["ok"] != true {
		t.Fatalf("sendKey kitty failed: %v", r)
	}
	if got, want := p1.got(), "\x1b[27;1u\x1b[9;2u"; got != want {
		t.Fatalf("kitty bytes = %q, want %q", got, want)
	}

	// 4. Guarded tab refuses sendKey with code "refused".
	d.mu.Lock()
	for i := range d.st.Panes {
		if d.st.Panes[i].ID == "p2" {
			d.st.Panes[i].Env = []string{"PITWALL_GUARD=1"}
		}
	}
	d.mu.Unlock()
	c.line(t, `{"type":"sendKey","id":"k4","pane":"@p2","keys":["enter"]}`)
	if r := c.next(t); r["ok"] != false || r["code"] != "refused" {
		t.Fatalf("guarded sendKey = %v, want refused", r)
	}
	if got := p2.got(); got != "" {
		t.Fatalf("guarded pane received unexpected bytes %q", got)
	}
}

func TestDaemonEventsAllKinds(t *testing.T) {
	d, f, sock := controlDaemon(t, func(o *Options) {
		o.Derive, o.SessionID = agent.Derive, agent.SessionID
	})
	c := dialJSON(t, sock)
	c.hello(t)

	c.line(t, `{"type":"subscribeEvents","id":"sub1"}`)
	if r := c.next(t); r["ok"] != true {
		t.Fatalf("subscribeEvents failed: %v", r)
	}

	waitEvent := func(wantEvent string, check func(map[string]any) bool) map[string]any {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			ev := c.next(t)
			if ev["type"] == "event" && ev["event"] == wantEvent && (check == nil || check(ev)) {
				return ev
			}
		}
		t.Fatalf("timed out waiting for event %q", wantEvent)
		return nil
	}

	// 1. Agent state (working -> pending-approval with detail -> done + turn.end).
	d.agentEvent(context.Background(), proto.AgentEvent{
		Pane:     "p1",
		Provider: model.ProviderClaude,
		Payload:  []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"hi"}`),
	})
	waitEvent("pane.state", func(ev map[string]any) bool {
		return ev["pane"] == "p1" && ev["state"] == "working"
	})

	d.agentEvent(context.Background(), proto.AgentEvent{
		Pane:     "p1",
		Provider: model.ProviderClaude,
		Payload:  []byte(`{"hook_event_name":"PermissionRequest","session_id":"s1","tool_name":"Bash","tool_input":{"description":"Run tests"}}`),
	})
	waitEvent("pane.state", func(ev map[string]any) bool {
		return ev["pane"] == "p1" && ev["state"] == "pending-approval" && strings.Contains(fmt.Sprint(ev["detail"]), "Run tests")
	})

	d.agentEvent(context.Background(), proto.AgentEvent{
		Pane:     "p1",
		Provider: model.ProviderClaude,
		Payload:  []byte(`{"hook_event_name":"Stop","session_id":"s1","last_assistant_message":"all good"}`),
	})
	seenDone, seenTurnEnd := false, false
	for !seenDone || !seenTurnEnd {
		ev := c.next(t)
		if ev["type"] != "event" {
			continue
		}
		if ev["event"] == "pane.state" && ev["pane"] == "p1" && ev["state"] == "done" {
			seenDone = true
		}
		if ev["event"] == "turn.end" && ev["pane"] == "p1" && ev["state"] == "done" {
			seenTurnEnd = true
		}
	}

	// 2. Title change.
	d.mu.Lock()
	d.st.Workspaces[0].Name = "renamed-build"
	d.changed()
	d.mu.Unlock()
	waitEvent("title", func(ev map[string]any) bool {
		return ev["tab"] == "w1" && ev["title"] == "renamed-build"
	})

	// 3. PR state change.
	d.mu.Lock()
	if d.st.PRs == nil {
		d.st.PRs = map[string]model.PR{}
	}
	d.st.PRs["w1"] = model.PR{Number: 99, State: model.PROpen, Reason: model.ReasonReady}
	d.changed()
	d.mu.Unlock()
	waitEvent("pr.state", func(ev map[string]any) bool {
		return ev["tab"] == "w1" && ev["prState"] == "open" && ev["prReason"] == "ready" && ev["prNumber"] == float64(99)
	})

	// 4. Queued message sent (deliver on p1 which is now done & ready).
	if err := d.send(proto.Send{Pane: "p1", From: "p2", Text: "next step"}); err != nil {
		t.Fatalf("d.send failed: %v", err)
	}
	waitEvent("queued.sent", func(ev map[string]any) bool {
		return ev["pane"] == "p1" && ev["from"] == "p2" && ev["queued"] == float64(0)
	})

	// 5. Pane exited.
	exit(f, "p2", 4)
	waitEvent("pane.exited", func(ev map[string]any) bool {
		return ev["pane"] == "p2" && ev["exitCode"] == float64(4)
	})
}

func TestDaemonReadFormatPalette(t *testing.T) {
	_, f, sock := controlDaemon(t, func(*Options) {})
	vt.SetPalette(vt.Palette{
		Fg: 0xeeeeee,
		Bg: 0x102030,
	})
	p1 := fakeNamed(f, "p1")
	p1.mu.Lock()
	p1.screen = []string{"hello <world>"}
	p1.mu.Unlock()

	c := dialJSON(t, sock)
	c.hello(t)

	c.line(t, `{"type":"readPane","id":"r1","pane":"@p1","screen":true,"format":"html"}`)
	r := c.next(t)
	if r["ok"] != true {
		t.Fatalf("readPane html failed: %v", r)
	}
	text := fmt.Sprint(r["data"].(map[string]any)["text"])
	if !strings.Contains(text, "#102030") || !strings.Contains(text, "#eeeeee") || !strings.Contains(text, "hello &lt;world&gt;") {
		t.Fatalf("readPane html = %q", text)
	}
}
