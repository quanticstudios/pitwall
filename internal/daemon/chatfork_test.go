package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// forkDaemon is a daemon whose Claude chats run on spy, with real turn
// checkpoints when check is set.
func forkDaemon(t *testing.T, spy *spyProvider, check bool) *Daemon {
	t.Helper()
	f := &fakes{statsCalls: map[string]int{}}
	o := f.options()
	o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: spy}
	if check {
		o.Checkpoint = gitstat.Checkpoint
	}
	return newDaemon(t, o)
}

// chatTurns sends each prompt to chat id and waits for its turn to end,
// running after(n) once turn n is over.
func chatTurns(t *testing.T, d *Daemon, id string, after func(n int), prompts ...string) {
	t.Helper()
	for _, p := range prompts {
		n := chatSummary(d, id).Turns + 1
		if r := chatDo(t, d, proto.ChatSend{Chat: id, Text: p}); r.Code != "" {
			t.Fatalf("send %s: %+v", p, r)
		}
		waitUntil(t, "turn "+p, func() bool { c := chatSummary(d, id); return c.Turns == n && c.State == session.StateReady })
		if after != nil {
			after(n)
		}
	}
}

func chatSummary(d *Daemon, id string) model.Chat {
	for _, c := range d.state().Chats {
		if c.ID == id {
			return c
		}
	}
	return model.Chat{}
}

// chatItems is chat id's transcript as "kind: text".
func chatItems(t *testing.T, d *Daemon, id string) []string {
	t.Helper()
	r, _ := d.chatRequest(context.Background(), proto.ChatRead{Chat: id})
	var s session.Snapshot
	if err := json.Unmarshal(r.(proto.ChatDeltas).Snapshot, &s); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range s.Content {
		out = append(out, string(it.Kind)+": "+it.Text)
	}
	return out
}

func chatConfig(t *testing.T, d *Daemon, id string) session.Config {
	t.Helper()
	c, err := d.chat(id)
	if err != nil {
		t.Fatal(err)
	}
	var cfg session.Config
	c.do(func() { cfg = c.cfg })
	return cfg
}

// TestChatFork: a fork after turn 1 is a new chat whose provider resumes
// the session cut at that turn under a new id; it holds the transcript
// through turn 1 and goes on from there, and the original does not
// change. A provider that cannot fork gets a text recap instead.
func TestChatFork(t *testing.T) {
	spy := &spyProvider{}
	d := forkDaemon(t, spy, false)
	src := chatDo(t, d, proto.ChatNew{Dir: t.TempDir(), Prompt: "one"}).Chat
	waitUntil(t, "turn one", func() bool { c := chatSummary(d, src); return c.Turns == 1 && c.State == session.StateReady })
	chatTurns(t, d, src, nil, "two")
	before, resume := chatItems(t, d, src), chatConfig(t, d, src).Resume

	r := chatDo(t, d, proto.ChatFork{Chat: src, Turn: "1"})
	if r.Code != "" || r.Chat == "" || r.Chat == src {
		t.Fatalf("fork: %+v", r)
	}
	at := chatConfig(t, d, src).Points[chatTurnID(t, d, src, 0)]
	if got, want := spy.origins()[len(spy.origins())-1], (session.Origin{Resume: resume, At: at, Fork: true}); got != want {
		t.Fatalf("the fork started from %+v, want %+v", got, want)
	}
	if got := chatItems(t, d, r.Chat); !slices.Equal(got, []string{"user-prompt: one", "assistant-text: Handled: one", "system: Forked from one"}) {
		t.Fatalf("fork transcript %q", got)
	}
	chatTurns(t, d, r.Chat, nil, "three")
	if cfg := chatConfig(t, d, r.Chat); cfg.From != nil || cfg.Resume == resume || !strings.HasSuffix(cfg.Resume, "-fork") {
		t.Errorf("fork config %+v, want the fork's own session", cfg)
	}
	if got := chatItems(t, d, r.Chat); got[len(got)-1] != "assistant-text: Handled: three" {
		t.Errorf("fork transcript %q", got)
	}
	if got := chatItems(t, d, src); !slices.Equal(got, before) || chatConfig(t, d, src).Resume != resume {
		t.Errorf("the original changed: %q, resume %q", got, chatConfig(t, d, src).Resume)
	}
	if r := chatDo(t, d, proto.ChatFork{Chat: src, Turn: "9"}); r.Code != "not-found" {
		t.Errorf("fork of turn 9: %+v", r)
	}

	t.Run("no fork", func(t *testing.T) {
		spy.mu.Lock()
		spy.fail = "start" // every resume or fork is refused
		spy.mu.Unlock()
		r := chatDo(t, d, proto.ChatFork{Chat: src, Turn: "2"})
		if r.Code != "" {
			t.Fatalf("fork: %+v", r)
		}
		got := chatItems(t, d, r.Chat)
		if !slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "system: The provider continues from a text recap") }) {
			t.Fatalf("no recap line in %q", got)
		}
		chatTurns(t, d, r.Chat, nil, "four")
		if sent := spy.sent(); !strings.Contains(sent[len(sent)-1], "Handled: two") || !strings.HasSuffix(sent[len(sent)-1], "\n\nfour") {
			t.Errorf("sent %q, want the recap before four", sent[len(sent)-1])
		}
	})
}

func chatTurnID(t *testing.T, d *Daemon, id string, i int) string {
	t.Helper()
	c, _ := d.chat(id)
	var turn string
	c.do(func() { turn = c.snap.Turns[i].ID })
	return turn
}

// TestChatRewind: three turns edit a repo's files; rewinding to before
// turn 2 puts the files back as turn 2 found them, cuts the transcript
// before its prompt, and the next message resumes the session cut at turn
// 1 under a new id. Once a commit moves HEAD, a rewind refuses, as Undo
// turn does, and changes nothing.
func TestChatRewind(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "--initial-branch=main")
	// why: Windows runners set core.autocrlf, so files git writes back come out with CRLF.
	gitIn(t, repo, "config", "core.autocrlf", "false")
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(repo, name))
		return string(b)
	}
	write("f.txt", "0\n")
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-m", "init")

	spy := &spyProvider{}
	d := forkDaemon(t, spy, true)
	id := chatDo(t, d, proto.ChatNew{Dir: repo}).Chat
	edit := func(n int) { // what turn n did to the files
		write("f.txt", strings.Repeat("x", n)+"\n")
		if n == 2 {
			write("new.txt", "made by turn 2\n")
		}
	}
	chatTurns(t, d, id, edit, "one", "two", "three")
	resume := chatConfig(t, d, id).Resume
	at := chatConfig(t, d, id).Points[chatTurnID(t, d, id, 0)]

	if r := chatDo(t, d, proto.ChatRewind{Chat: id, Turn: chatTurnID(t, d, id, 1)}); r.Code != "" {
		t.Fatalf("rewind: %+v", r)
	}
	if read("f.txt") != "x\n" || read("new.txt") != "" {
		t.Errorf("files after the rewind: f.txt %q, new.txt %q; want turn 2's start", read("f.txt"), read("new.txt"))
	}
	want := []string{"user-prompt: one", "assistant-text: Handled: one", "system: Rewound to before turn 2; its files are back as they were when it started"}
	if got := chatItems(t, d, id); !slices.Equal(got, want) {
		t.Fatalf("transcript after the rewind %q, want %q", got, want)
	}
	if c := chatSummary(d, id); c.Turns != 1 {
		t.Errorf("%d turns after the rewind, want 1", c.Turns)
	}
	chatTurns(t, d, id, nil, "four")
	if got, want := spy.origins()[len(spy.origins())-1], (session.Origin{Resume: resume, At: at, Fork: true}); got != want {
		t.Errorf("the rewound chat started from %+v, want %+v", got, want)
	}
	if sent := spy.sent(); sent[len(sent)-1] != "four" {
		t.Errorf("sent %q after the rewind, want four alone", sent[len(sent)-1])
	}
	if cfg := chatConfig(t, d, id); cfg.From != nil || cfg.Resume == resume {
		t.Errorf("config %+v after the rewind, want the forked session's id", cfg)
	}

	before := chatItems(t, d, id)
	write("f.txt", "turn four\n")
	gitIn(t, repo, "commit", "-am", "commit after turn 2")
	r := chatDo(t, d, proto.ChatRewind{Chat: id, Turn: "2"})
	if r.Code != "conflict" || !strings.Contains(r.Message, gitstat.ErrHeadMoved.Error()) {
		t.Fatalf("rewind after a commit: %+v, want the HEAD-moved refusal", r)
	}
	if got := chatItems(t, d, id); !slices.Equal(got, before) || read("f.txt") != "turn four\n" {
		t.Errorf("a refused rewind changed the chat %q or the files %q", got, read("f.txt"))
	}
}
