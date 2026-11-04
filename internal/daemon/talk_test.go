package daemon

import (
	"context"
	"errors"
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
	oldSettle, oldQuiet := sendSettle.set(250*time.Millisecond), sendQuiet.set(300*time.Millisecond)
	t.Cleanup(func() {
		// why: a deliver timer can outlive the test and read these as they
		// are put back. An empty queue makes a late deliver return before
		// reading them, and sendMu waits out one already running.
		d.mu.Lock()
		for _, w := range d.mail.wake {
			w.Stop()
		}
		clear(d.mail.queue)
		d.mu.Unlock()
		d.sendMu.Lock()
		sendSettle.set(oldSettle)
		sendQuiet.set(oldQuiet)
		d.sendMu.Unlock()
	})
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

// claudeBox is a Claude Code screen with text in its input box.
var claudeBox = []string{"────────", "❯ half a thought", "────────"}

// setScreen sets what pane p's screen shows.
func setScreen(p *fakePane, rows []string) {
	p.mu.Lock()
	p.screen = rows
	p.mu.Unlock()
}

// draftDaemon is talkDaemon with short message timings, and pane a's agent
// past a first turn.
func draftDaemon(t *testing.T) (*Daemon, *fakePane) {
	t.Helper()
	d, f := talkDaemon(t)
	oldSettle, oldQuiet, oldLook := sendSettle.set(time.Millisecond), sendQuiet.set(50*time.Millisecond), sendLook.set(10*time.Millisecond)
	t.Cleanup(func() {
		// why: see TestSend; a look can outlive the test.
		d.mu.Lock()
		for _, w := range d.mail.wake {
			w.Stop()
		}
		clear(d.mail.queue)
		d.mu.Unlock()
		d.sendMu.Lock()
		sendSettle.set(oldSettle)
		sendQuiet.set(oldQuiet)
		sendLook.set(oldLook)
		d.sendMu.Unlock()
	})
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"plan"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	return d, f.pane(0)
}

func TestEmptyPlanTitlesHoldMessages(t *testing.T) {
	for _, title := range []string{
		"Exit plan mode? · from a subagent",
		"Exit plan mode? · from the planner agent",
		"Exit plan mode?                                         2 of 2",
	} {
		t.Run(title, func(t *testing.T) {
			d, p := draftDaemon(t)
			setScreen(p, strings.Split(strings.Replace(emptyPlanScreen, "Exit plan mode?", title, 1), "\n"))
			if err := d.send(proto.Send{Pane: "a", Text: "continue"}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(200 * time.Millisecond)
			if got := p.got(); got != "" {
				t.Fatalf("typed %q into the empty plan", got)
			}
			d.mu.Lock()
			queued := len(d.mail.queue["a"])
			d.mu.Unlock()
			if queued != 1 {
				t.Fatalf("queue = %d, want 1", queued)
			}
		})
	}
}

func TestPlanQuoteAcceptsMessage(t *testing.T) {
	d, p := draftDaemon(t)
	setScreen(p, []string{"The dialog says Claude wants to exit plan mode.", "❯ "})
	if err := d.send(proto.Send{Pane: "a", Text: "continue"}); err != nil {
		t.Fatal(err)
	}
	want := scriptHeader + "continue\r"
	if got := gotAfter(t, p, 0, len(want)); got != want {
		t.Fatalf("chat quote holds message: got %q, want %q", got, want)
	}
}

func draftHeld(d *Daemon, pane string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshot().DraftHeld[pane]
}

const scriptHeader = "[pitwall: message from a script] "

// A message waits while the input box holds the user's text, however long ago
// they stopped typing, and goes in queue order by itself once the box empties.
func TestDraftHoldsMessages(t *testing.T) {
	d, a := draftDaemon(t)
	setScreen(a, claudeBox)
	d.noteUserKey("a", []byte("t"))
	for _, text := range []string{"one", "two"} {
		if err := d.send(proto.Send{Pane: "a", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if got := a.got(); got != "" {
		t.Fatalf("typed %q into the draft", got)
	}
	if !draftHeld(d, "a") {
		t.Fatal("the row does not say the draft holds the message")
	}
	if err := d.prompt(proto.Prompt{Pane: "a", Text: "now"}); !errors.Is(err, errDraft) {
		t.Fatalf("prompt over a draft: %v", err)
	}

	// The box empties with no key, hook or send: the next look finds it.
	setScreen(a, []string{"────────", "❯ ", "────────"})
	want := scriptHeader + "one\r"
	if got := gotAfter(t, a, 0, len(want)); got != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
	if draftHeld(d, "a") {
		t.Fatal("the row still waits on a draft that is gone")
	}
	n := len(want)
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"one"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	want = scriptHeader + "two\r"
	if got := gotAfter(t, a, n, len(want)); got != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
}

// The user sends the draft: the message follows the turn it started, not the
// draft's own Enter.
func TestDraftSentReleasesMessage(t *testing.T) {
	d, a := draftDaemon(t)
	setScreen(a, claudeBox)
	d.noteUserKey("a", []byte("t"))
	if err := d.send(proto.Send{Pane: "a", Text: "one"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	d.noteUserKey("a", []byte("\r"))
	setScreen(a, []string{"────────", "❯ ", "────────"})
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"half a thought"}`)
	time.Sleep(100 * time.Millisecond)
	if got := a.got(); got != "" {
		t.Fatalf("typed %q during the user's turn", got)
	}
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	want := scriptHeader + "one\r"
	if got := gotAfter(t, a, 0, len(want)); got != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
}

// Without a box to read, or with text the user cannot have typed, a message
// waits only for the pause after the last key.
func TestDraftFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		screen []string
		key    bool
		prov   model.Provider
	}{
		{"no box on screen", []string{"something else entirely"}, true, model.ProviderClaude},
		{"no key since the last prompt", claudeBox, false, model.ProviderClaude},
		{"agent with no known box", claudeBox, true, model.ProviderGemini},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, a := draftDaemon(t)
			d.mu.Lock()
			d.setProvider("a", tc.prov)
			d.mu.Unlock()
			setScreen(a, tc.screen)
			if tc.key {
				d.noteUserKey("a", []byte("t"))
			}
			if err := d.send(proto.Send{Pane: "a", Text: "one"}); err != nil {
				t.Fatal(err)
			}
			want := scriptHeader + "one\r"
			if got := gotAfter(t, a, 0, len(want)); got != want {
				t.Fatalf("typed %q, want %q", got, want)
			}
		})
	}

	// A misread box in a typed-in pane stops holding once the user's prompt goes in.
	d, a := draftDaemon(t)
	d.noteUserKey("a", []byte("\r"))
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"more"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	setScreen(a, claudeBox)
	if err := d.send(proto.Send{Pane: "a", Text: "one"}); err != nil {
		t.Fatal(err)
	}
	want := scriptHeader + "one\r"
	if got := gotAfter(t, a, 0, len(want)); got != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
}

// Only the user's own Return before a prompt hook ends their draft: another
// turn's hook leaves it, however soon after a key it comes.
func TestDraftSurvivesOtherTurn(t *testing.T) {
	d, a := draftDaemon(t)
	setScreen(a, claudeBox)
	otherTurn := func() {
		t.Helper()
		claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"background task finished"}`)
		claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
		if _, _, err := d.ready("a"); !errors.Is(err, errDraft) {
			t.Fatalf("the draft remains on screen but ready returns %v", err)
		}
	}
	d.noteUserKey("a", []byte("t"))
	otherTurn()
	// A late hook of the prompt before: Return, then a new draft.
	d.noteUserKey("a", []byte("\r"))
	d.noteUserKey("a", []byte("t"))
	otherTurn()
	// Shift+Enter starts a new line and submits nothing.
	d.noteUserKey("a", []byte("\x1b[13;2u"))
	otherTurn()
}

// The window's deliveries pass the same check at typing time, from the live
// screen, and show on the row.
func TestWindowDeliverWaitsForDraft(t *testing.T) {
	d, a := draftDaemon(t)
	setScreen(a, claudeBox)
	d.noteUserKey("a", []byte("t"))
	if err := d.handle(t.Context(), proto.Deliver{Pane: "a", Text: "a comment", Images: []string{"/s/a.png"}}); err != nil {
		t.Fatal(err)
	}
	if err := d.send(proto.Send{Pane: "a", Text: "next"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := a.got(); got != "" {
		t.Fatalf("typed %q into the draft", got)
	}
	if !draftHeld(d, "a") {
		t.Fatal("the row does not say the draft holds the delivery")
	}
	setScreen(a, []string{"────────", "❯ ", "────────"})
	want := "'/s/a.png'a comment\r"
	if got := gotAfter(t, a, 0, len(want)); got != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
	if draftHeld(d, "a") {
		t.Fatal("the row still waits on a draft that is gone")
	}
	claudeHook(t, d, "a", `{"hook_event_name":"UserPromptSubmit","prompt":"a comment"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	next := scriptHeader + "next\r"
	if got := gotAfter(t, a, len(want), len(next)); got != next {
		t.Fatalf("typed %q, want %q", got, next)
	}
}

// A reply answers the question that a message ahead of it waits on, still not
// into a draft, and the message goes after the turn.
func TestWindowDeliverPassesQuestion(t *testing.T) {
	d, a := draftDaemon(t)
	claudeHook(t, d, "a", `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[{"question":"Which path?"}]}}`)
	if err := d.send(proto.Send{Pane: "a", Text: "review this later"}); err != nil {
		t.Fatal(err)
	}
	setScreen(a, claudeBox)
	d.noteUserKey("a", []byte("t"))
	if err := d.handle(t.Context(), proto.Deliver{Pane: "a", Text: "use a.go", Seq: 3}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := a.got(); got != "" {
		t.Fatalf("typed %q into the draft", got)
	}
	if !draftHeld(d, "a") {
		t.Fatal("the row does not say the draft holds the reply")
	}
	setScreen(a, []string{"────────", "❯ ", "────────"})
	if got := gotAfter(t, a, 0, 9); got != "use a.go\r" {
		t.Fatalf("typed %q, want the reply", got)
	}
	if draftHeld(d, "a") {
		t.Fatal("the row still waits on a draft that is gone")
	}
	time.Sleep(100 * time.Millisecond)
	if got := a.got(); got != "use a.go\r" {
		t.Fatalf("typed %q with the question still open", got)
	}
	claudeHook(t, d, "a", `{"hook_event_name":"PostToolUse","tool_name":"AskUserQuestion"}`)
	claudeHook(t, d, "a", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	next := scriptHeader + "review this later\r"
	if got := gotAfter(t, a, 9, len(next)); got != next {
		t.Fatalf("typed %q, want %q", got, next)
	}
}

// A window letter behind a message that can go keeps the queue's order.
func TestWindowDeliverOrder(t *testing.T) {
	d, a := draftDaemon(t)
	if err := d.send(proto.Send{Pane: "a", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	if err := d.handle(t.Context(), proto.Deliver{Pane: "a", Text: "second", Seq: 1}); err != nil {
		t.Fatal(err)
	}
	want := scriptHeader + "first\r"
	if got := gotAfter(t, a, 0, len(want)); got != want {
		t.Fatalf("typed %q, want %q", got, want)
	}
}

// Delivered reports a window letter once typed, and closing its pane first
// reports nothing, so the window can tell the two apart.
func TestWindowDeliverReportsDelivery(t *testing.T) {
	delivered := func(d *Daemon, pane string) (uint64, bool) {
		d.mu.Lock()
		defer d.mu.Unlock()
		seq, ok := d.snapshot().Delivered[pane]
		return seq, ok
	}
	d, a := draftDaemon(t)
	setScreen(a, claudeBox)
	d.noteUserKey("a", []byte("t"))
	if err := d.handle(t.Context(), proto.Deliver{Pane: "a", Text: "first", Seq: 5}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if seq, ok := delivered(d, "a"); ok {
		t.Fatalf("a held letter reads as delivered: %d", seq)
	}
	setScreen(a, []string{"────────", "❯ ", "────────"})
	if got := gotAfter(t, a, 0, 6); got != "first\r" {
		t.Fatalf("typed %q", got)
	}
	if seq, _ := delivered(d, "a"); seq != 5 {
		t.Fatalf("delivered %d, want 5", seq)
	}

	d, a = draftDaemon(t)
	setScreen(a, claudeBox)
	d.noteUserKey("a", []byte("t"))
	if err := d.handle(t.Context(), proto.Deliver{Pane: "a", Text: "lost", Seq: 6}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := d.closePane("a"); err != nil {
		t.Fatal(err)
	}
	if seq, ok := delivered(d, "a"); ok || a.got() != "" {
		t.Fatalf("after the close: delivered %d, typed %q", seq, a.got())
	}
}

// A delivery goes to the agents the window may reply to, which ready refuses:
// one asking a question, one with no hooks. A turn that began since holds it.
func TestWindowDeliverStates(t *testing.T) {
	d, a := draftDaemon(t)
	d.mu.Lock()
	d.st.Activities[d.activityIndex("a")].State = model.StateAwaitingInput
	d.mu.Unlock()
	if err := d.handle(t.Context(), proto.Deliver{Pane: "a", Text: "yes"}); err != nil {
		t.Fatal(err)
	}
	if got := gotAfter(t, a, 0, 4); got != "yes\r" {
		t.Fatalf("typed %q", got)
	}

	f := d.panes["b"].(*fakePane)
	d.mu.Lock()
	d.setProvider("b", model.ProviderAider)
	d.st.Activities = append(d.st.Activities, model.Activity{PaneID: "b", WorkspaceID: "w", Provider: model.ProviderAider, State: model.StateWorking})
	i := d.activityIndex("b")
	d.mu.Unlock()
	if err := d.handle(t.Context(), proto.Deliver{Images: []string{"/s/b.png"}, Pane: "b"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := f.got(); got != "" {
		t.Fatalf("typed %q into a running turn", got)
	}
	// No hook reports this: the next look finds the turn over.
	d.mu.Lock()
	d.st.Activities[i].State = model.StateCompleted
	d.mu.Unlock()
	want := "'/s/b.png'\r"
	if got := gotAfter(t, f, 0, len(want)); got != want {
		t.Fatalf("image only: typed %q, want %q", got, want)
	}

	if err := d.handle(t.Context(), proto.Deliver{Pane: "b", Text: " "}); err == nil {
		t.Fatal("an empty delivery was queued")
	}
	if err := d.handle(t.Context(), proto.Deliver{Pane: "gone", Text: "hi"}); err == nil {
		t.Fatal("a delivery to no pane was queued")
	}
}

// The phone's reply does not land in a draft either.
func TestRemoteReplyRefusesDraft(t *testing.T) {
	d, a := draftDaemon(t)
	setScreen(a, claudeBox)
	d.noteUserKey("a", []byte("t"))
	d.mu.Lock()
	at := d.st.Activities[d.activityIndex("a")].UpdatedAt.UnixNano()
	d.mu.Unlock()
	if err := d.remoteReply("a", at, "yes"); err == nil || !strings.Contains(err.Error(), "draft") {
		t.Fatalf("reply over a draft: %v", err)
	}
	if got := a.got(); got != "" {
		t.Fatalf("typed %q into the draft", got)
	}
}

// A form on the screen holds the window's Reply, Request changes and review
// comments, whatever the state says, and they go once it is gone.
func TestWindowDeliverWaitsForForm(t *testing.T) {
	for _, state := range []model.AgentState{model.StateCompleted, model.StateAwaitingInput} {
		d, a := draftDaemon(t)
		d.mu.Lock()
		d.st.Activities[d.activityIndex("a")].State = state
		d.mu.Unlock()
		setScreen(a, []string{"Which database?", "> 1. Redis", "  2. SQLite", "Press enter to confirm or esc to cancel"})
		if err := d.handle(t.Context(), proto.Deliver{Pane: "a", Text: "use a.go", Seq: 1}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(200 * time.Millisecond)
		if got := a.got(); got != "" {
			t.Fatalf("%s: typed %q into the form", state, got)
		}
		d.mu.Lock()
		queued := len(d.mail.queue["a"])
		d.mu.Unlock()
		if queued != 1 {
			t.Fatalf("%s: queue %d", state, queued)
		}
		setScreen(a, []string{"────────", "❯ ", "────────"})
		if got := gotAfter(t, a, 0, 9); got != "use a.go\r" {
			t.Fatalf("%s: typed %q", state, got)
		}
	}
}

// A form up by the time of the Enter does not get it, however the delivery
// began, and neither a quote of a footer nor the paste's own echo counts.
func TestWindowDeliverEnter(t *testing.T) {
	defer replyDelay.set(replyDelay.set(150 * time.Millisecond))
	const menu = "Which database?\n> 1. Redis\n  2. SQLite\n"
	for _, tc := range []struct {
		name          string
		before, after string
		text, want    string
	}{
		{"menu with another footer", "❯ ", menu + "Press enter to confirm or esc to cancel", "use a.go", "use a.go"},
		{"gemini's form", "❯ ", "Answer Questions\nWhich?\n│ ● 1. Postgres │\n│   2. SQLite   │", "use a.go", "use a.go"},
		{"menu after a quoted footer", "The menu says Enter to select\n❯ ", menu + "Enter to select · Esc to cancel", "use a.go", "use a.go"},
		{"quoted footer", "The menu says Enter to select\n❯ ", "The menu says Enter to select\n❯ use a.go", "use a.go", "use a.go\r"},
		{"numbered reply", "❯ ", "❯ 1. do a\n  2. do b", "1. do a\n2. do b", "1. do a 2. do b\r"},
	} {
		d, a := draftDaemon(t)
		setScreen(a, strings.Split(tc.before, "\n"))
		a.mu.Lock()
		a.wrote = func() { a.screen = strings.Split(tc.after, "\n") }
		a.mu.Unlock()
		if err := d.handle(t.Context(), proto.Deliver{Pane: "a", Text: tc.text}); err != nil {
			t.Fatal(err)
		}
		gotAfter(t, a, 0, len(tc.want))
		time.Sleep(2 * replyDelay.get())
		if got := a.got(); got != tc.want {
			t.Errorf("%s: typed %q, want %q", tc.name, got, tc.want)
		}
	}
}
