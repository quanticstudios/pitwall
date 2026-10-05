package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type syncBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// TestFollow: lines written before follow starts are skipped, new ones in
// either file come through, and a rotated file is read from its start.
func TestFollow(t *testing.T) {
	dir := t.TempDir()
	gui, daemon := filepath.Join(dir, "gui.log"), filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(gui, []byte("old line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out syncBuf
	stop, done := make(chan struct{}), make(chan struct{})
	go func() { follow([]string{gui, daemon}, &out, stop, time.Millisecond); close(done) }()
	appendTo := func(p, s string) {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	wait := func(want string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !strings.Contains(out.String(), want); time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("follow printed %q, want %q in it", out.String(), want)
			}
		}
	}
	for !strings.Contains(out.String(), "tick") { // follow has taken its start offsets
		appendTo(daemon, "tick\n")
		time.Sleep(5 * time.Millisecond)
	}
	appendTo(gui, "gui one\n")
	appendTo(daemon, "daemon one\n")
	wait("gui one\n")
	wait("daemon one\n")
	os.Rename(gui, gui+".1")
	appendTo(gui, "new\n")
	wait("new\n")
	close(stop)
	<-done
	if strings.Contains(out.String(), "old line") {
		t.Fatalf("printed a line from before it started: %q", out.String())
	}
}
