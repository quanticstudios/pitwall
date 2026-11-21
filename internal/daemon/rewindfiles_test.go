package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// rewindRepo is a git repository in a temp folder with f.txt committed,
// and functions that write and read its files.
func rewindRepo(t *testing.T) (repo string, write func(name, text string), read func(name string) string) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repo, "init", "--initial-branch=main")
	gitIn(t, repo, "config", "core.autocrlf", "false")
	write = func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read = func(name string) string {
		b, _ := os.ReadFile(filepath.Join(repo, name))
		return string(b)
	}
	write("f.txt", "0\n")
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-m", "init")
	return repo, write, read
}

func fileNames(fs []proto.RewindFile) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Path+" "+f.Status)
	}
	slices.Sort(out)
	return out
}

// TestChatRewindFiles: in a chat tab whose turns edit files, the check
// lists what a rewind of the files does; the user's own edit after the
// last turn refuses the rewind until it is stashed; the stashed rewind
// puts the files back and its backup brings the edit back.
func TestChatRewindFiles(t *testing.T) {
	repo, write, read := rewindRepo(t)
	spy := &spyProvider{edit: func(n int) {
		write("f.txt", strings.Repeat("x", n)+"\n")
		if n == 2 {
			write("new.txt", "made by turn 2\n")
		}
	}}
	d := forkDaemon(t, spy, true)
	id := chatDo(t, d, proto.ChatNew{Dir: repo}).Chat
	chatTurns(t, d, id, nil, "one", "two")
	spy.mu.Lock()
	spy.edit = nil
	spy.mu.Unlock()
	ctx := context.Background()
	one := chatTurnID(t, d, id, 0)

	r := d.chatRewindCheck(ctx, proto.ChatRewindCheck{Chat: id, Turn: one})
	if r.Chat != id || r.Turn != one || r.Refused != "" || len(r.Unsaved) != 0 ||
		!slices.Equal(fileNames(r.Files), []string{"f.txt modified", "new.txt deleted"}) {
		t.Fatalf("check: %+v", r)
	}

	write("mine.txt", "the user's own\n")
	r = d.chatRewindCheck(ctx, proto.ChatRewindCheck{Chat: id, Turn: one})
	if r.Refused != "" || !slices.Equal(fileNames(r.Unsaved), []string{"mine.txt added"}) || len(r.Files) != 3 {
		t.Fatalf("check with an unsaved file: %+v", r)
	}
	before := chatItems(t, d, id)
	res := chatDo(t, d, proto.ChatRewind{Chat: id, Turn: one})
	if res.Code != "conflict" || !strings.Contains(res.Message, "mine.txt") || !strings.Contains(res.Message, "stash it first") {
		t.Fatalf("rewind with an unsaved file: %+v", res)
	}
	if got := chatItems(t, d, id); !slices.Equal(got, before) || read("mine.txt") == "" || read("new.txt") == "" {
		t.Fatal("a refused rewind changed the chat or the files")
	}

	if res := chatDo(t, d, proto.ChatRewind{Chat: id, Turn: one, Stash: true}); res.Code != "" {
		t.Fatalf("stash and rewind: %+v", res)
	}
	if read("f.txt") != "0\n" || read("new.txt") != "" || read("mine.txt") != "" {
		t.Errorf("files after the rewind: f %q new %q mine %q", read("f.txt"), read("new.txt"), read("mine.txt"))
	}
	items := chatItems(t, d, id)
	last := items[len(items)-1]
	i := strings.Index(last, "--source=")
	if i < 0 {
		t.Fatalf("the rewind does not say how to get the files back: %q", last)
	}
	sha := strings.Fields(last[i+len("--source="):])[0]
	gitIn(t, repo, "restore", "--source="+sha, "--worktree", "--", ":/")
	if read("mine.txt") != "the user's own\n" || read("new.txt") != "made by turn 2\n" || read("f.txt") != "xx\n" {
		t.Error("restoring the backup did not bring the files back")
	}
}

// TestChatRewindRefusals: a rewind of the files is refused, files and
// chat left alone, while another chat works in the folder, when the turn
// has no checkpoint, while a turn runs, and for no such turn; the check
// says why each time.
func TestChatRewindRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("another chat in the folder", func(t *testing.T) {
		repo, write, read := rewindRepo(t)
		spy := &spyProvider{edit: func(int) { write("f.txt", "turn\n") }}
		d := forkDaemon(t, spy, true)
		id := chatDo(t, d, proto.ChatNew{Dir: repo}).Chat
		chatTurns(t, d, id, nil, "one")
		other := chatDo(t, d, proto.ChatNew{Dir: repo}).Chat
		one := chatTurnID(t, d, id, 0)
		r := d.chatRewindCheck(ctx, proto.ChatRewindCheck{Chat: id, Turn: one})
		if !strings.Contains(r.Refused, "also works in this folder") || len(r.Files) != 0 {
			t.Errorf("check: %+v", r)
		}
		if res := chatDo(t, d, proto.ChatRewind{Chat: id, Turn: one, Stash: true}); res.Code != "conflict" || read("f.txt") != "turn\n" {
			t.Errorf("rewind: %+v, f.txt %q", res, read("f.txt"))
		}
		if res := chatDo(t, d, proto.ChatRewind{Chat: id, Turn: one, KeepFiles: true}); res.Code != "" {
			t.Errorf("a rewind that keeps the files is no business of the other chat: %+v", res)
		}
		_ = other
	})
	t.Run("no checkpoint", func(t *testing.T) {
		repo, write, read := rewindRepo(t)
		spy := &spyProvider{edit: func(int) { write("f.txt", "turn\n") }}
		d := forkDaemon(t, spy, false)
		id := chatDo(t, d, proto.ChatNew{Dir: repo}).Chat
		chatTurns(t, d, id, nil, "one")
		one := chatTurnID(t, d, id, 0)
		r := d.chatRewindCheck(ctx, proto.ChatRewindCheck{Chat: id, Turn: one})
		if !strings.Contains(r.Refused, "no snapshot of the files") {
			t.Errorf("check: %+v", r)
		}
		if res := chatDo(t, d, proto.ChatRewind{Chat: id, Turn: one, Stash: true}); res.Code != "unsupported" || read("f.txt") != "turn\n" {
			t.Errorf("rewind: %+v", res)
		}
	})
	t.Run("no such turn", func(t *testing.T) {
		repo, _, _ := rewindRepo(t)
		d := forkDaemon(t, &spyProvider{}, true)
		id := chatDo(t, d, proto.ChatNew{Dir: repo}).Chat
		if r := d.chatRewindCheck(ctx, proto.ChatRewindCheck{Chat: id, Turn: "nope"}); r.Refused == "" {
			t.Errorf("check: %+v", r)
		}
		if r := d.chatRewindCheck(ctx, proto.ChatRewindCheck{Chat: "no-chat", Turn: "1"}); r.Refused == "" {
			t.Errorf("check of no chat: %+v", r)
		}
	})
}

// TestTurnEndHook: a Stop hook in an agent pane takes its tab's turn end,
// and the next prompt's checkpoint drops it.
func TestTurnEndHook(t *testing.T) {
	repo, _, _ := rewindRepo(t)
	d, _, _ := phoneDaemon(t)
	d.o.Checkpoint, d.o.EndTurn = gitstat.Checkpoint, gitstat.EndTurn
	d.mu.Lock()
	d.st.Panes[0].Cwd = repo
	d.mu.Unlock()
	// why: marshaled, so a Windows path's backslashes stay valid JSON.
	hook := func(fields map[string]string) []byte {
		fields["session_id"], fields["transcript_path"], fields["cwd"] = "s1", "/x.jsonl", repo
		b, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	stop := hook(map[string]string{"hook_event_name": "Stop"})
	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: stop}); err != nil {
		t.Fatal(err)
	}
	// The hook waits at most turnWait for the snapshot, which a slow runner can pass.
	waitUntil(t, "a turn end from the Stop hook", func() bool {
		return gitIn(t, repo, "for-each-ref", "refs/pitwall/turns/w1/end") != ""
	})
	prompt := hook(map[string]string{"hook_event_name": "UserPromptSubmit", "prompt": "go"})
	if err := d.agentEvent(context.Background(), proto.AgentEvent{Pane: "a", Provider: model.ProviderClaude, Payload: prompt}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the next prompt's checkpoint, without the turn end", func() bool {
		out := gitIn(t, repo, "for-each-ref", "refs/pitwall/turns/w1/")
		return !strings.Contains(out, "/end") && strings.Contains(out, "/1")
	})
}

// TestPaneRewindFiles: a Claude Code pane whose tab took a checkpoint at
// its prompt rewinds the files too: the check lists them, an unsaved file
// refuses before any key is typed, another agent in the folder refuses,
// and a stashed rewind drives the picker, then puts the files back and
// says how to get them back.
func TestPaneRewindFiles(t *testing.T) {
	repo, write, read := rewindRepo(t)
	d, f, _ := phoneDaemon(t)
	ctx := context.Background()
	d.mu.Lock()
	d.st.Panes[0].Cwd = repo
	d.mu.Unlock()
	rev, err := gitstat.Checkpoint(ctx, repo, "w1")
	if err != nil {
		t.Fatal(err)
	}
	write("f.txt", "by the turn\n")
	if _, err := gitstat.EndTurn(ctx, repo, "w1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	path := filepath.Join(t.TempDir(), "s.jsonl")
	body := strings.ReplaceAll(promptLines("p1", "", "fix it"), "2026-11-20T10:00:00Z", now)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = path
	d.mu.Unlock()
	paneRead(t, d, "a", session.Snapshot{}, 0)
	if ws, got, err := d.rewindPoint(ctx, "a", "p1"); err != nil || ws != "w1" || got != rev {
		t.Fatalf("rewindPoint = %s, %s, %v; want w1, %s", ws, got, err, rev)
	}
	r := d.chatRewindCheck(ctx, proto.ChatRewindCheck{Chat: "a", Turn: "p1"})
	if r.Refused != "" || !slices.Equal(fileNames(r.Files), []string{"f.txt modified"}) || len(r.Unsaved) != 0 {
		t.Fatalf("check: %+v", r)
	}

	// Another agent in the folder refuses, before any key is typed.
	d.mu.Lock()
	d.st.Panes[1].Cwd, d.st.Panes[1].Provider = repo, model.ProviderCodex
	d.mu.Unlock()
	if r := d.chatRewindCheck(ctx, proto.ChatRewindCheck{Chat: "a", Turn: "p1"}); !strings.Contains(r.Refused, `Codex in tab "w2" also works in this folder`) {
		t.Errorf("check beside Codex: %+v", r)
	}
	if res, _ := d.chatRequest(ctx, proto.ChatRewind{Chat: "a", Turn: "p1", Stash: true}); res.(proto.ChatResult).Code != "conflict" || read("f.txt") != "by the turn\n" {
		t.Errorf("rewind beside Codex: %+v", res)
	}
	d.mu.Lock()
	d.st.Panes[1].Cwd, d.st.Panes[1].Provider = "", ""
	d.mu.Unlock()

	fakeRewind(f.pane(0), []string{"fix it"})
	write("mine.txt", "the user's own\n")
	before := len(f.pane(0).got())
	res, _ := d.chatRequest(ctx, proto.ChatRewind{Chat: "a", Turn: "p1"})
	if m := res.(proto.ChatResult); m.Code != "conflict" || !strings.Contains(m.Message, "mine.txt") {
		t.Fatalf("rewind with an unsaved file: %+v", m)
	}
	if got := f.pane(0).got()[before:]; len(got) != 0 {
		t.Errorf("a refused rewind typed %q", got)
	}

	res, _ = d.chatRequest(ctx, proto.ChatRewind{Chat: "a", Turn: "p1", Stash: true})
	m := res.(proto.ChatResult)
	if m.Code != "" || !strings.Contains(m.Message, "git restore --source=") {
		t.Fatalf("stash and rewind: %+v", m)
	}
	if read("f.txt") != "0\n" || read("mine.txt") != "" {
		t.Errorf("files after the rewind: f %q mine %q", read("f.txt"), read("mine.txt"))
	}
	if got := string(f.pane(0).got()[before:]); !strings.HasPrefix(got, "/rewind\r") {
		t.Errorf("typed %q, want the picker driven", got)
	}

}
