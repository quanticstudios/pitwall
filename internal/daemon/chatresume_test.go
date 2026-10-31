package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/eventlog"
	"github.com/quanticstudios/pitwall/internal/session/fake"
)

// spyProvider runs a fake provider and records each Start's Origin and
// each message its drivers got. fail picks how a resume fails: "start"
// refuses it, as Codex's thread/resume does, and "exit" exits on the first
// message, as Claude's --resume of a session it cannot find does.
type spyProvider struct {
	fail string

	mu    sync.Mutex
	froms []session.Origin
	sends []string
}

func (p *spyProvider) Start(ctx context.Context, cfg session.Config, from session.Origin, emit func(session.Event)) (session.Driver, error) {
	p.mu.Lock()
	p.froms = append(p.froms, from)
	p.mu.Unlock()
	if from.Resume != "" {
		switch p.fail {
		case "start":
			return nil, &session.ProviderError{Op: "thread/resume", Message: "no rollout found for thread id " + from.Resume}
		case "exit":
			return exitDriver{emit: emit}, nil
		}
	}
	drv, err := fake.New(fake.Options{}).Start(ctx, cfg, from, emit)
	if err != nil {
		return nil, err
	}
	return spyDriver{drv, p}, nil
}

func (p *spyProvider) origins() []session.Origin {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.froms)
}

func (p *spyProvider) sent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.sends)
}

type spyDriver struct {
	session.Driver
	p *spyProvider
}

func (d spyDriver) Send(ctx context.Context, m session.Message) (string, error) {
	d.p.mu.Lock()
	d.p.sends = append(d.p.sends, m.Text)
	d.p.mu.Unlock()
	return d.Driver.Send(ctx, m)
}

// exitDriver is a Claude driver whose --resume found no session: the
// first message starts a turn and the process exits.
type exitDriver struct{ emit func(session.Event) }

func (d exitDriver) Send(_ context.Context, m session.Message) (string, error) {
	now := session.Stamp(time.Now())
	code := 1
	d.emit(session.Event{Type: session.EventTurnStarted, TurnID: m.ID, CreatedAt: now})
	d.emit(session.Event{Type: session.EventError, Message: "Claude exited (code 1)\nNo conversation found", CreatedAt: now})
	d.emit(session.Event{Type: session.EventExited, ExitCode: &code, CreatedAt: now})
	return m.ID, nil
}
func (exitDriver) Steer(context.Context, session.Message) error            { return session.ErrClosed }
func (exitDriver) Interrupt(context.Context) error                         { return nil }
func (exitDriver) Approve(context.Context, string, session.Decision) error { return session.ErrClosed }
func (exitDriver) Answer(context.Context, string, map[string][]string) error {
	return session.ErrClosed
}
func (exitDriver) Set(context.Context, session.Settings) error { return nil }
func (exitDriver) Close(context.Context) error                 { return nil }

// transcript is the chat's items as "kind: text".
func transcript(t *testing.T, c *testClient, id string) []string {
	t.Helper()
	r := chatCall[proto.ChatDeltas](c, proto.ChatRead{Chat: id})
	var s session.Snapshot
	if err := json.Unmarshal(r.Snapshot, &s); err != nil {
		t.Fatalf("read %s: %v", id, err)
	}
	var out []string
	for _, it := range s.Content {
		out = append(out, string(it.Kind)+": "+it.Text)
	}
	return out
}

// restartChat runs a chat of provider p through a turn, restarts the
// daemon and sends "again". It returns the chat, its resume id before the
// restart, the client of the second daemon, and the config after it.
func restartChat(t *testing.T, p model.Provider, spy *spyProvider) (string, string, *testClient, session.Config) {
	t.Helper()
	root := t.TempDir()
	f := &fakes{statsCalls: map[string]int{}}
	opts := func(o *Options) {
		o.Chats = root
		o.Providers = map[model.Provider]session.Provider{p: spy}
	}
	hello := proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()}
	sock, stop := runWith(t, f, opts)
	gui := dialHello(t, sock, hello)
	r := chatCall[proto.ChatResult](gui, proto.ChatNew{Provider: p, Dir: t.TempDir(), Prompt: "hello"})
	if r.Code != "" {
		t.Fatalf("new: %+v", r)
	}
	waitChat(gui, "first turn", func(s model.State) bool { c := chatOf(s); return c.Turns == 1 && c.State == session.StateReady })
	stop()
	before, err := eventlog.ReadConfig(filepath.Join(root, r.Chat))
	if err != nil || before.Resume == "" {
		t.Fatalf("config after the first daemon: %+v, %v", before, err)
	}

	sock, stop = runWith(t, f, opts)
	gui = dialHello(t, sock, hello)
	st := waitChat(gui, "chat back", func(s model.State) bool { return chatOf(s).ID == r.Chat })
	if !slices.ContainsFunc(st.Workspaces, func(w model.Workspace) bool { return w.Chat == r.Chat }) {
		t.Fatalf("the chat's tab did not come back: %+v", st.Workspaces)
	}
	if n := len(spy.origins()); n != 1 {
		t.Fatalf("%d starts after the restart, before any command; want the first daemon's 1", n)
	}
	if r := chatCall[proto.ChatResult](gui, proto.ChatSend{Chat: r.Chat, Text: "again"}); r.Code != "" {
		t.Fatalf("send after the restart: %+v", r)
	}
	waitChat(gui, "second turn", func(s model.State) bool { c := chatOf(s); return c.Turns >= 2 && c.State == session.StateReady })
	stop()
	after, err := eventlog.ReadConfig(filepath.Join(root, r.Chat))
	if err != nil {
		t.Fatal(err)
	}
	sock, stop = runWith(t, f, opts) // for the transcript, as it was saved
	t.Cleanup(stop)
	return r.Chat, before.Resume, dialHello(t, sock, hello), after
}

// TestChatResumesAfterRestart: a chat's provider session resumes with its
// stored id after a daemon restart, when the next message needs it, and
// the transcript carries on.
func TestChatResumesAfterRestart(t *testing.T) {
	for _, p := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		t.Run(string(p), func(t *testing.T) {
			spy := &spyProvider{}
			id, resume, gui, cfg := restartChat(t, p, spy)
			if got, want := spy.origins(), []session.Origin{{}, {Resume: resume}}; !slices.Equal(got, want) {
				t.Fatalf("starts %+v, want %+v", got, want)
			}
			if got, want := spy.sent(), []string{"hello", "again"}; !slices.Equal(got, want) {
				t.Fatalf("sent %q, want %q", got, want)
			}
			if cfg.Resume != resume {
				t.Fatalf("resume id %q after the resume, want %q", cfg.Resume, resume)
			}
			want := []string{"user-prompt: hello", "assistant-text: Handled: hello", "user-prompt: again", "assistant-text: Handled: again"}
			if got := transcript(t, gui, id); !slices.Equal(got, want) {
				t.Fatalf("transcript %q, want %q", got, want)
			}
		})
	}
}

// TestChatResumeFallback: a resume that fails starts a new provider
// session, says so in the transcript, and sends the message with a recap
// of the turns before it.
func TestChatResumeFallback(t *testing.T) {
	for _, c := range []struct {
		p    model.Provider
		fail string
	}{{model.ProviderCodex, "start"}, {model.ProviderClaude, "exit"}} {
		t.Run(string(c.p), func(t *testing.T) {
			spy := &spyProvider{fail: c.fail}
			id, resume, gui, cfg := restartChat(t, c.p, spy)
			if got, want := spy.origins(), []session.Origin{{}, {Resume: resume}, {}}; !slices.Equal(got, want) {
				t.Fatalf("starts %+v, want %+v", got, want)
			}
			sent := spy.sent()
			if len(sent) != 2 || !strings.Contains(sent[1], "Handled: hello") || !strings.HasSuffix(sent[1], "\n\nagain") {
				t.Fatalf("sent %q, want the recap before again", sent)
			}
			if cfg.Resume == "" || cfg.Resume == resume {
				t.Fatalf("resume id %q after the fallback, want the new session's, not %q", cfg.Resume, resume)
			}
			got := transcript(t, gui, id)
			if !slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "system: Resumed without history: ") }) {
				t.Fatalf("no fallback line in %q", got)
			}
			if n := slices.Index(got, "user-prompt: again"); n < 0 || slices.Index(got[n+1:], "user-prompt: again") >= 0 {
				t.Fatalf("want again once in %q", got)
			}
			if last := got[len(got)-1]; !strings.HasPrefix(last, "assistant-text: Handled: ") || !strings.HasSuffix(last, "again") {
				t.Fatalf("last item %q, want the new session's reply", last)
			}
		})
	}
}

// TestChatBootLoadsNothing: at start the daemon reads each chat's
// config.json and nothing else: no log opens (eventlog.Open creates
// events.jsonl) and no provider starts, however many chats there are. The
// first command loads one.
func TestChatBootLoadsNothing(t *testing.T) {
	const n = 200
	root := t.TempDir()
	f := &fakes{statsCalls: map[string]int{}}
	f.saved.Sessions = []model.Session{{ID: "s1"}}
	for i := range n {
		id, ws := fmt.Sprint("chat-", i), fmt.Sprint("ws-", i)
		f.saved.Workspaces = append(f.saved.Workspaces, model.Workspace{ID: ws, SessionID: "s1", Path: t.TempDir(), Chat: id, Label: id})
		f.saved.Sessions[0].Order = append(f.saved.Sessions[0].Order, ws)
		dir := filepath.Join(root, id)
		os.MkdirAll(dir, 0o700)
		b, _ := json.Marshal(session.Config{Format: 1, ID: id, Provider: model.ProviderClaude, Workspace: ws, Resume: "r-" + id})
		os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600)
	}
	spy := &spyProvider{}
	sock, stop := runWith(t, f, func(o *Options) {
		o.Chats = root
		o.Providers = map[model.Provider]session.Provider{model.ProviderClaude: spy}
	})
	defer stop()
	gui := dialHello(t, sock, proto.Hello{Version: proto.Version, Level: proto.Level, Kind: "gui", Cwd: t.TempDir()})
	st := waitChat(gui, "chats back", func(s model.State) bool { return len(s.Chats) == n })
	if got := len(slices.DeleteFunc(slices.Clone(st.Workspaces), func(w model.Workspace) bool { return w.Chat == "" })); got != n {
		t.Fatalf("%d chat tabs, want %d", got, n)
	}
	opened := func() (ids []string) {
		for i := range n {
			id := fmt.Sprint("chat-", i)
			if _, err := os.Stat(filepath.Join(root, id, "events.jsonl")); err == nil {
				ids = append(ids, id)
			}
		}
		return ids
	}
	if got := opened(); len(got) != 0 || len(spy.origins()) != 0 {
		t.Fatalf("boot opened the logs of %v and started %d providers", got, len(spy.origins()))
	}
	chatCall[proto.ChatDeltas](gui, proto.ChatRead{Chat: "chat-7"})
	if got := opened(); !slices.Equal(got, []string{"chat-7"}) || len(spy.origins()) != 0 {
		t.Fatalf("a read opened %v and started %d providers, want chat-7's log alone", got, len(spy.origins()))
	}
	if r := chatCall[proto.ChatResult](gui, proto.ChatArchive{Chat: "chat-7"}); r.Code != "" {
		t.Fatalf("archive: %+v", r)
	}
	waitUntil(t, "chat-7 archived", func() bool {
		_, err := eventlog.ReadConfig(filepath.Join(root, "archive", "chat-7"))
		return err == nil
	})
}

func TestRecap(t *testing.T) {
	turn := func(s string) *string { return &s }
	s := session.Snapshot{Content: []session.Item{
		{Kind: session.KindUserPrompt, Text: "q1"},
		{Kind: session.KindAssistantText, Text: "first", TurnID: turn("t1")},
		{Kind: session.KindAssistantText, Text: "end of one", TurnID: turn("t1")},
		{Kind: session.KindSystem, Text: "a line"},
		{Kind: session.KindAssistantText, Text: "end of two", TurnID: turn("t2")},
	}}
	got := recap(s)
	if !strings.HasSuffix(got, "end of one\n\nend of two") || strings.Contains(got, "first") {
		t.Fatalf("recap %q", got)
	}
	if recap(session.Snapshot{}) != "" {
		t.Fatal("a recap of nothing")
	}
	s.Content = append(s.Content, session.Item{Kind: session.KindAssistantText, Text: strings.Repeat("x", 3000), TurnID: turn("t3")})
	if got := recap(s); strings.Contains(got, "end of two") || !strings.HasSuffix(got, strings.Repeat("x", recapLimit)) {
		t.Fatalf("a long recap keeps %d runes of the start", len(got))
	}
}
