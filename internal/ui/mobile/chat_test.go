package mobile

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
)

func TestModelName(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5":     "Opus 5.5",
		"claude-opus-5-5[1m]": "Opus 5.5 1M",
		"Claude-Sonnet-5-5":   "Sonnet 5.5",
		"opus[1m]":            "Opus 5.5 1M",
		"default":             "Default",
		"gpt-6.1-sol":         "GPT-6.1 Sol",
		"claude-mystery-9":    "mystery-9", // not in the table: the id, less claude-
		"":                    "",
	} {
		if got := modelName(id); got != want {
			t.Errorf("modelName(%q) = %q, want %q", id, got, want)
		}
	}
	for _, c := range []struct{ pick, current, want string }{
		{"default", "claude-opus-5-5", "what Claude Code starts with"},
		{"opus", "claude-opus-5-5", "now"},
		{"opus[1m]", "claude-opus-5-5", "1M context"},
		{"opus[1m]", "claude-opus-5-5[1m]", "now"},
		{"sonnet", "claude-opus-5-5", ""},
		{"sonnet", "", ""},
	} {
		if got := modelNote(c.pick, c.current); got != c.want {
			t.Errorf("modelNote(%q, %q) = %q, want %q", c.pick, c.current, got, c.want)
		}
	}
	if effortLevel("low") != 1 || effortLevel("max") != 5 || effortLevel("") != 0 {
		t.Error("effortLevel: want low 1, max 5, unknown 0")
	}
}

func TestSplitFences(t *testing.T) {
	got := splitFences("Found it.\n\n```go\nfunc f() {\n}\n```\nDone.\n```\nstill streaming")
	want := []mdPart{{text: "Found it."}, {code: true, text: "func f() {\n}"}, {text: "Done."}, {code: true, text: "still streaming"}}
	if len(got) != len(want) {
		t.Fatalf("parts = %+v", got)
	}
	for i := range want {
		if got[i].code != want[i].code || got[i].text != want[i].text {
			t.Errorf("part %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if p := splitFences("just text"); len(p) != 1 || p[0].code {
		t.Errorf("plain text = %+v", p)
	}
}

func TestSendState(t *testing.T) {
	for _, c := range []struct {
		text                  string
		sending, online, busy bool
		want                  sendState
	}{
		{"", false, true, false, sendIdle},
		{"  \n", false, true, true, sendIdle},
		{"hi", false, true, false, sendReady},
		{"hi", false, true, true, sendQueue},
		{"hi", true, true, true, sendSending},
		{"hi", true, false, false, sendOff}, // offline wins: nothing goes out
	} {
		if got := sendStateOf(c.text, c.sending, c.online, c.busy); got != c.want {
			t.Errorf("sendStateOf(%q, %v, %v, %v) = %d, want %d", c.text, c.sending, c.online, c.busy, got, c.want)
		}
	}
	working := remote.Tab{Item: remote.Item{Agent: "claude", State: "working"}}
	if placeholder(working) != "Queue a message. Enter adds a line." {
		t.Errorf("working placeholder = %q", placeholder(working))
	}
	done := remote.Tab{Item: remote.Item{Agent: "codex", State: "completed"}}
	if placeholder(done) != "Message Codex. Enter adds a line." {
		t.Errorf("idle placeholder = %q", placeholder(done))
	}
	if !busy(remote.Tab{Item: remote.Item{State: "pending-approval"}}) || busy(done) {
		t.Error("busy: an approval queues, a finished turn does not")
	}
	if idle(remote.Tab{Item: remote.Item{State: "awaiting-input"}}) || !idle(done) {
		t.Error("idle: a question is not idle, a finished turn is")
	}
}

func TestQueueLine(t *testing.T) {
	ps := []*phone.Pending{
		{Text: "sent, not queued", Sent: true},
		{Text: "Run the  benchmarks\nfirst", Sent: true, Queued: true},
		{Text: "second", Sent: true, Queued: true},
	}
	b, r, bad := queueLine(remote.Tab{Queued: 2}, ps, "")
	if b != "2 queued" || r != "Run the benchmarks first" || bad {
		t.Errorf("queued = %q %q %v", b, r, bad)
	}
	// Messages queued from the desktop count, though the phone has no text.
	if b, r, _ := queueLine(remote.Tab{Queued: 3}, nil, ""); b != "3 queued" || r != "" {
		t.Errorf("desktop queue = %q %q", b, r)
	}
	if b, r, bad := queueLine(remote.Tab{Queued: 1}, ps, "the agent exited"); b != "Not sent:" || r != "the agent exited" || !bad {
		t.Errorf("not sent = %q %q %v", b, r, bad)
	}
	if b, _, _ := queueLine(remote.Tab{}, ps[:1], ""); b != "" {
		t.Errorf("nothing queued shows %q", b)
	}
}

func TestNotSentComesBack(t *testing.T) {
	now := time.Now()
	c := &phone.Chat{Pending: []*phone.Pending{{Text: "ok", Sent: true, At: now}, {Text: "open a PR", Sent: true, ID: 4, At: now}}}
	c.Update(remote.Tab{Dropped: []uint64{4}}, now)
	why, text := takeNotSent(c, "")
	if why != "the agent exited" || text != "open a PR" || len(c.Pending) != 1 {
		t.Fatalf("empty box: %q %q, %d pending", why, text, len(c.Pending))
	}
	// A draft in the box stays: the line still says why.
	c.Pending = append(c.Pending, &phone.Pending{Text: "lost", Err: "refused"})
	if why, text := takeNotSent(c, "my draft"); why != "refused" || text != "" {
		t.Errorf("with a draft: %q %q", why, text)
	}
	if why, _ := takeNotSent(c, ""); why != "" {
		t.Errorf("nothing left, got %q", why)
	}
}

// fakeAPI records the chat's calls.
type fakeAPI struct {
	chatAPI
	commands []string
	err      error
}

func (f *fakeAPI) Command(ctx context.Context, pane, what, value string) error {
	f.commands = append(f.commands, pane+" /"+what+" "+value)
	return f.err
}

func TestPickWaitsForIdle(t *testing.T) {
	api := &fakeAPI{}
	c := newChat("p1")
	c.data.Model = "claude-opus-5-5"
	var later []func()
	env := chatEnv{Online: true, Tab: remote.Tab{Item: remote.Item{Pane: "p1", State: "working"}},
		Do: func(call func(context.Context, chatAPI) error, done func(error)) {
			err := call(context.Background(), api)
			later = append(later, func() { done(err) })
		}}
	c.cmp.pick = &pick{"model", "sonnet"}
	c.firePick(env)
	if len(api.commands) != 0 {
		t.Fatalf("fired mid-turn: %v", api.commands)
	}
	if got := c.cmp.modelShown(c.data.Model); got != "Opus 5.5" {
		t.Errorf("before the pick goes in, the chip says %q", got)
	}
	env.Tab.State = "completed"
	c.firePick(env)
	c.firePick(env) // one at a time
	if !slices.Equal(api.commands, []string{"p1 /model sonnet"}) {
		t.Fatalf("commands = %v", api.commands)
	}
	later[0]()
	if c.cmp.pick != nil || c.cmp.picking || c.cmp.modelShown(c.data.Model) != "Sonnet 5.5" {
		t.Errorf("after: pick %v, picking %v, chip %q", c.cmp.pick, c.cmp.picking, c.cmp.modelShown(c.data.Model))
	}
	// The next turn reports its own model, which the chip then follows.
	if got := c.cmp.modelShown("claude-haiku-5-5"); got != "Haiku 5.5" {
		t.Errorf("after a turn, the chip says %q", got)
	}

	api.err = errors.New("that pane shows a form; answer it in its tab")
	c.cmp.pick = &pick{"effort", "max"}
	c.firePick(env)
	later[1]()
	if c.cmp.effort != "" || c.cmp.note != "That pane shows a form; answer it in its tab" {
		t.Errorf("failed pick: effort %q, note %q", c.cmp.effort, c.cmp.note)
	}
}

func TestPageInKeepsPlace(t *testing.T) {
	c := newChat("p1")
	c.reset(phone.Entries{Entries: []flow.Entry{{Seq: 10}, {Seq: 11}}, More: true})
	c.list.Position.First, c.list.Position.Offset = 1, 30
	c.pageIn([]flow.Entry{{Seq: 7}, {Seq: 8}, {Seq: 10}}, false)
	if c.list.Position.First != 3 || c.list.Position.Offset != 30 || c.data.More {
		t.Errorf("position %+v, more %v", c.list.Position, c.data.More)
	}
	if got := c.data.First(); got != 7 {
		t.Errorf("first seq %d", got)
	}
}
