package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

func TestChatAside(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake claude is a shell script")
	}
	d, _, _ := remoteDaemon(t)
	fake := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s|' \"$@\"\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(claude.CommandEnv, fake)
	ask := func(q string) proto.ChatAsideResult { return d.chatAside(context.Background(), proto.ChatAside{Chat: "a", ID: "q1", Question: q}) }

	if r := ask("why?"); r.Err == "" || r.ID != "q1" {
		t.Errorf("before the transcript: %+v", r)
	}
	d.mu.Lock()
	d.st.Panes[0].Transcript = "/x/projects/repo/0b16bffd-aaaa.jsonl"
	d.mu.Unlock()
	if r := ask("  "); r.Err == "" {
		t.Errorf("an empty question: %+v", r)
	}
	r := ask("why did it pick Go?")
	want := "-p|--resume|0b16bffd-aaaa|--fork-session|--no-session-persistence|--tools||--output-format|text|--append-system-prompt|"
	if r.Err != "" || !strings.HasPrefix(r.Answer, want) || !strings.HasSuffix(r.Answer, "|why did it pick Go?") {
		t.Errorf("answer %+v, want it to start %q and end with the question", r, want)
	}
	if r := d.chatAside(context.Background(), proto.ChatAside{Chat: "nope", ID: "q2", Question: "x"}); r.Err == "" || r.Chat != "nope" {
		t.Errorf("no such chat: %+v", r)
	}
}
