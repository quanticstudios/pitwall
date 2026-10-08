//go:build unix

package flow

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// A transcript or a meta.json that is a FIFO, or a symlink to one, is not
// opened: opening it would block until a writer came.
func TestFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("no FIFOs:", err)
	}
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s.jsonl")
	write(t, path, `{"type":"user","timestamp":"2026-10-05T10:00:00Z","message":{"content":"go"}}
{"type":"assistant","timestamp":"2026-10-05T10:00:01Z","message":{"content":[{"type":"tool_use","id":"a","name":"Agent","input":{"description":"d"}}]}}
`)
	subs := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(subs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fifo, filepath.Join(subs, "agent-x.meta.json")); err != nil {
		t.Fatal(err)
	}
	done := make(chan Feed, 2)
	go func() {
		done <- read(t, model.ProviderClaude, link)
		done <- read(t, model.ProviderClaude, path)
	}()
	for i := range 2 {
		select {
		case f := <-done:
			if i == 0 && f.Turns != nil {
				t.Errorf("FIFO feed = %+v", f)
			}
			if i == 1 && (len(f.Subagents) != 1 || len(f.Subagents[0].Calls) != 0) {
				t.Errorf("feed = %+v", f)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("read blocked on a FIFO")
		}
	}
}

// A file that can no longer be opened, or became a FIFO, empties the Feed.
func TestOpenFailureResets(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any file")
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	line := `{"type":"user","timestamp":"2026-10-05T10:00:00Z","message":{"content":"go"}}` + "\n"
	write(t, path, line)
	s := newSession(model.ProviderClaude, path, false, time.Time{})
	s.poll()
	appendFile(t, path, line)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	if !s.poll() || s.b.feed().Turns != nil {
		t.Fatalf("unreadable file kept %+v", s.b.feed())
	}
	if s.poll() {
		t.Fatal("a second failed poll reported a change")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.poll() || len(s.b.feed().Turns) != 2 {
		t.Fatalf("readable again: %+v", s.b.feed())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if !s.poll() || s.b.feed().Turns != nil {
		t.Fatalf("FIFO kept %+v", s.b.feed())
	}
}
