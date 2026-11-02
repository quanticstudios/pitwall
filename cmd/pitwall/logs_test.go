package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
// either file come through, and a file replaced by one of the same size is
// read from its start.
func TestFollow(t *testing.T) {
	dir := t.TempDir()
	gui, daemon := filepath.Join(dir, "gui.log"), filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(gui, []byte("old line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out syncBuf
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- follow([]string{gui, daemon}, &out, stop, time.Millisecond) }()
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
	for !strings.Contains(out.String(), "tick") {
		appendTo(daemon, "tick\n")
		time.Sleep(5 * time.Millisecond)
	}
	appendTo(gui, "gui one\n")
	wait("gui one\n")
	size := len("old line\ngui one\n")
	next := filepath.Join(dir, "next")
	if err := os.WriteFile(next, []byte(strings.Repeat("n", size-1)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// why: Windows refuses the rename while follow has gui open; it opens
	// it only for a moment each poll.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		err := os.Rename(next, gui)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
	}
	wait(strings.Repeat("n", size-1) + "\n")
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "old line") {
		t.Fatalf("printed a line from before it started: %q", out.String())
	}
}

type failing struct{}

func (failing) Write([]byte) (int, error) { return 0, errors.New("closed pipe") }

// TestFollowWriteError: follow returns when it cannot write, instead of
// polling on.
func TestFollowWriteError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gui.log")
	done := make(chan error, 1)
	go func() { done <- follow([]string{path}, failing{}, nil, time.Millisecond) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString("x\n")
		f.Close()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("no error")
			}
			return
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("follow kept polling after a write error")
		}
	}
}

// TestFollowRetriesOpen: with retryOpen, as on Windows, a file follow cannot
// open for a few polls is read again once it opens, instead of follow
// ending on the first error.
func TestFollowRetriesOpen(t *testing.T) {
	open, retry := openLog, retryOpen
	t.Cleanup(func() { openLog, retryOpen = open, retry })
	var fails atomic.Int32
	openLog = func(p string) (*os.File, error) {
		if fails.Add(-1) >= 0 {
			return nil, os.ErrPermission
		}
		return os.Open(p)
	}
	retryOpen = true
	path := filepath.Join(t.TempDir(), "gui.log")
	appendTo := func(s string) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	var out syncBuf
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- follow([]string{path}, &out, stop, time.Millisecond) }()
	t.Cleanup(func() {
		close(stop)
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	for !strings.Contains(out.String(), "tick") {
		appendTo("tick\n")
		time.Sleep(5 * time.Millisecond)
	}
	fails.Store(5)
	for deadline := time.Now().Add(5 * time.Second); fails.Load() >= 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("follow stopped opening the file after %d failed opens", 5-fails.Load())
		}
	}
	appendTo("after the failed opens\n")
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(out.String(), "after the failed opens"); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("follow printed %q", out.String())
		}
	}
}
