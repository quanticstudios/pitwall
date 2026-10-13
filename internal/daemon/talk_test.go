package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/decide"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// talkDaemon is a daemon with real hook parsing and one tab of panes a and
// b, neither reported on yet.
func talkDaemon(t *testing.T) (*Daemon, *fakes) {
	t.Helper()
	old := replyDelay
	replyDelay = 0
	t.Cleanup(func() { replyDelay = old })
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Sessions: []model.Session{{ID: "s", Name: "work"}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", Name: "w", Tabs: []model.Tab{{ID: "t", Layout: &layout.Node{
			Dir: layout.Horizontal, Children: []*layout.Node{{Pane: "a"}, {Pane: "b"}}}}}, ActiveTab: "t"}},
		Panes: []model.Pane{{ID: "a", WorkspaceID: "w"}, {ID: "b", WorkspaceID: "w"}},
	}
	o := f.options()
	o.Derive = agent.Derive
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	return d, f
}

func claudeHook(t *testing.T, d *Daemon, pane, payload string) {
	t.Helper()
	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: pane, Provider: model.ProviderClaude, Payload: []byte(payload)}); err != nil {
		t.Fatal(err)
	}
}

func TestPrompt(t *testing.T) {
	d, f := talkDaemon(t)
	refused := func(pane, want string) {
		t.Helper()
		if err := d.prompt(proto.Prompt{Pane: pane, Text: "fix it"}); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("prompt %s: %v, want an error with %q", pane, err, want)
		}
	}
	refused("b", "runs no agent")
	d.mu.Lock()
	d.setProvider("b", model.ProviderAider) // an agent pitwall reads from its screen
	d.mu.Unlock()
	refused("b", "reports nothing through hooks")

	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"plan"}`)
	refused("a", "working")
	claudeHook(t, d, "a", `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"description":"Run make"}}`)
	refused("a", "waits on the user: Run make")
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"done"}`)
	f.pane(0).mu.Lock()
	f.pane(0).screen = []string{" Do you want to proceed?", " > 1. Yes", " Esc to cancel"}
	f.pane(0).mu.Unlock()
	refused("a", "shows a prompt")
	if got := f.pane(0).got(); got != "" {
		t.Fatalf("a refused prompt typed %q", got)
	}

	f.pane(0).mu.Lock()
	f.pane(0).screen, f.pane(0).modes = nil, vt.Modes{BracketedPaste: true}
	f.pane(0).mu.Unlock()
	if err := d.prompt(proto.Prompt{Pane: "a", Text: "fix\x1b[201~ it"}); err != nil {
		t.Fatal(err)
	}
	want := "\x1b[200~fix[201~ it\x1b[201~\r"
	if got := gotAfter(t, f.pane(0), 0, len(want)); got != want {
		t.Fatalf("typed %q, want %q", got, want)
	}

	// Codex sends its first hook with its first prompt: seen running, it
	// takes one.
	d.mu.Lock()
	d.setProvider("b", model.ProviderCodex)
	d.mu.Unlock()
	if err := d.prompt(proto.Prompt{Pane: "b", Text: "review"}); err != nil {
		t.Fatal(err)
	}
	if got := gotAfter(t, f.pane(1), 0, 7); got != "review\r" {
		t.Fatalf("codex got %q", got)
	}
}

// The full last message of a finished turn is kept per pane, redacted and
// capped, until the next prompt or the pane closes.
func TestRead(t *testing.T) {
	d, f := talkDaemon(t)
	d.dec.cur.Secrets = []string{"hunter2hunter2"}
	read := func(pane string) proto.ReadResult { return d.read(proto.Read{Pane: pane}) }
	if r := read("a"); r.Err == "" {
		t.Fatalf("a reply before any turn: %+v", r)
	}
	long := strings.Repeat("é", maxReply+10)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"`+long+`"}`)
	if r := read("a"); len([]rune(r.Text)) != maxReply {
		t.Fatalf("kept %d runes, want %d", len([]rune(r.Text)), maxReply)
	}
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"The plan:\n1. use hunter2hunter2"}`)
	if r := read("a"); r.Err != "" || r.Text != "The plan:\n1. use "+decide.Redacted {
		t.Fatalf("read %+v", r)
	}
	// A subagent's stop is no reply of the pane's agent.
	claudeHook(t, d, "a", `{"hook_event_name":"SubagentStop","last_assistant_message":"inner"}`)
	if r := read("a"); !strings.HasPrefix(r.Text, "The plan") {
		t.Fatalf("after SubagentStop: %+v", r)
	}
	if err := d.prompt(proto.Prompt{Pane: "a", Text: "again"}); err != nil {
		t.Fatal(err)
	}
	if r := read("a"); r.Err == "" {
		t.Fatalf("the last turn's reply after a new prompt: %+v", r)
	}

	f.pane(1).mu.Lock()
	f.pane(1).screen = []string{"$ make   ", "", "ok", "", ""}
	f.pane(1).mu.Unlock()
	if r := d.read(proto.Read{Pane: "b", Screen: true}); r.Text != "$ make\n\nok" {
		t.Fatalf("screen %q", r.Text)
	}

	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"bye"}`)
	if err := d.closePane("a"); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	_, kept := d.replies["a"]
	d.mu.Unlock()
	if kept || read("a").Err == "" {
		t.Fatal("a closed pane's reply is kept")
	}
}

// TestSend queues messages per pane and types each, under a header naming
// its sender, when the agent sits at its prompt and the user is not
// typing, one per turn.
func TestSend(t *testing.T) {
	d, f := talkDaemon(t)
	for v, to := range map[*time.Duration]time.Duration{&sendSettle: 250 * time.Millisecond, &sendQuiet: 300 * time.Millisecond} {
		old := *v
		*v = to
		t.Cleanup(func() { *v = old })
	}
	a := f.pane(0)
	send := func(from, text string) error {
		t.Helper()
		return d.send(proto.Send{Pane: "a", From: from, Text: text})
	}
	// typed waits for the next message after n bytes of a's input.
	typed := func(n int, text string) {
		t.Helper()
		want := `[pitwall: message from claude in #1.2; reply with: pitwall send @b "..."] ` + text + "\r"
		if got := gotAfter(t, a, n, len(want)); got != want {
			t.Fatalf("typed %q, want %q", got, want)
		}
	}
	// quiet checks that nothing past n bytes reached a.
	quiet := func(n int) {
		t.Helper()
		time.Sleep(150 * time.Millisecond)
		if got := a.got(); len(got) != n {
			t.Fatalf("typed %q too soon", got[n:])
		}
	}
	d.mu.Lock()
	d.setProvider("b", model.ProviderClaude)
	d.mu.Unlock()

	if err := d.send(proto.Send{Pane: "a", From: "a", Text: "me"}); err == nil || !strings.Contains(err.Error(), "sending pane") {
		t.Fatalf("a send to its own pane: %v", err)
	}
	if err := send("b", "hi"); err == nil || !strings.Contains(err.Error(), "runs no agent") {
		t.Fatalf("a send to a shell: %v", err)
	}

	// Idle: typed at once, after the header.
	claudeHook(t, d, "a", `{"hook_event_name":"SessionStart","source":"startup"}`)
	if err := send("b", "hi"); err != nil {
		t.Fatal(err)
	}
	typed(0, "hi")
	n := len(a.got())

	// Working: typed sendSettle after the Stop, two in order, one per turn.
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"hi"}`)
	if err := send("b", "one"); err != nil {
		t.Fatal(err)
	}
	if err := send("b", "two"); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	queued := d.snapshot().Queued["a"]
	d.mu.Unlock()
	if queued != 2 {
		t.Fatalf("queued %d, want 2", queued)
	}
	quiet(n)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	quiet(n) // sendSettle
	typed(n, "one")
	n = len(a.got())
	if err := send("b", "three"); err != nil { // a wake before the turn "one" started reports
		t.Fatal(err)
	}
	quiet(n)
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"one"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	typed(n, "two")
	n = len(a.got())
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"two"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	typed(n, "three")
	n = len(a.got())

	// Blocked: held until the prompt is answered and the turn ends.
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"three"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"description":"Run make"}}`)
	if err := send("b", "held"); err != nil {
		t.Fatal(err)
	}
	quiet(n)
	claudeHook(t, d, "a", `{"hook_event_name":"PostToolUse","tool_name":"Bash"}`)
	quiet(n)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	typed(n, "held")
	n = len(a.got())

	// A key of the user's holds the next message for sendQuiet; a focus
	// report is no key.
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"held"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	d.noteUserKey("a", []byte("x"))
	if err := send("b", "four"); err != nil {
		t.Fatal(err)
	}
	quiet(n)
	typed(n, "four")
	n = len(a.got())
	d.mu.Lock()
	key := d.mail.keyAt["a"]
	d.mu.Unlock()
	d.noteUserKey("a", []byte("\x1b[I"))
	d.mu.Lock()
	focused := d.mail.keyAt["a"]
	d.mu.Unlock()
	if focused != key {
		t.Fatal("a focus report counts as a key")
	}

	// The loop guard: agents get sendLimit messages to a pane between two
	// of the user's keys; a script is not counted.
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"four"}`)
	d.noteUserKey("a", []byte("y"))
	for i := range sendLimit {
		if err := send("b", fmt.Sprint(i)); err != nil {
			t.Fatalf("message %d: %v", i+1, err)
		}
	}
	if err := send("b", "more"); err == nil || !strings.Contains(err.Error(), "ask the user") {
		t.Fatalf("message %d: %v", sendLimit+1, err)
	}
	if err := send("", "from cron"); err != nil {
		t.Fatalf("a script past the limit: %v", err)
	}
	d.noteUserKey("a", []byte("z"))
	if err := send("b", "more"); err != nil {
		t.Fatalf("after a key: %v", err)
	}

	// Closing the pane drops its queue; a send to it then fails.
	if err := d.closePane("a"); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	left, st := len(d.mail.queue), d.snapshot()
	d.mu.Unlock()
	if left != 0 || st.Queued != nil {
		t.Fatalf("a closed pane keeps %d queues, %v", left, st.Queued)
	}
	if err := send("b", "hello?"); err == nil || !strings.Contains(err.Error(), "no running pane") {
		t.Fatalf("a send to a closed pane: %v", err)
	}
	quiet(n)
}
