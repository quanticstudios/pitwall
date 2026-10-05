package logs

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestOpenFile: a file over the cap moves to .1 at open, one under it is
// appended to, and on Unix an existing directory and file get 0700 and 0600.
func TestOpenFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "gui.log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old 56789\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := OpenFile(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("kept\n")
	f.Close()
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
				t.Fatalf("%s: %v %v, want %v", p, fi.Mode().Perm(), err, want)
			}
		}
	}
	if f, err = OpenFile(path, 10); err != nil { // 15 bytes now: moved
		t.Fatal(err)
	}
	f.WriteString("new\n")
	f.Close()
	got, _ := os.ReadFile(path)
	old, _ := os.ReadFile(path + ".1")
	if string(got) != "new\n" || string(old) != "old 56789\nkept\n" {
		t.Fatalf("after the startup move: %q, .1 %q", got, old)
	}
}

// stuck blocks every write until release closes.
type stuck struct{ release chan struct{} }

func (s stuck) Write(p []byte) (int, error) { <-s.release; return len(p), nil }

// TestWriterNeverBlocks: an output that blocks forever does not block the
// caller; lines past the queue are dropped and counted once there is room.
func TestWriterNeverBlocks(t *testing.T) {
	var mu sync.Mutex
	var got bytes.Buffer
	out := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return got.Write(p) })
	s := stuck{make(chan struct{})}
	w := NewWriter("gui v1 7 ", nil, s, out)
	done := make(chan struct{})
	go func() {
		for range 10 * queue {
			w.Write([]byte("line\n"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked behind a stalled output")
	}
	close(s.release)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) { // the queue drains
		mu.Lock()
		n := strings.Count(got.String(), "line\n")
		mu.Unlock()
		if n >= queue {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d lines written after the output came back", n)
		}
	}
	w.Write([]byte("after\n"))
	w.Close(5 * time.Second)
	mu.Lock()
	text := got.String()
	mu.Unlock()
	if !strings.Contains(text, "gui v1 7 ") || !strings.Contains(text, " log lines dropped\n") || !strings.HasSuffix(text, "after\n") {
		t.Fatalf("no dropped-lines count before the next line:\n%s", text)
	}
	if n := strings.Count(text, "line\n"); n < 1 || n > queue+1 {
		t.Fatalf("%d lines written of a %d-line queue", n, queue)
	}

	blocked := NewWriter("", nil, stuck{make(chan struct{})})
	blocked.Write([]byte("x\n"))
	start := time.Now()
	blocked.Close(10 * time.Millisecond)
	if time.Since(start) > time.Second {
		t.Fatal("Close waited on a stalled output past its timeout")
	}
}

// TestOneLine: text with newlines and other control characters stays one
// line, and Start falls back to a non-blocking stderr writer when it cannot
// open the file.
func TestOneLine(t *testing.T) {
	var got bytes.Buffer
	w := NewWriter("", nil, &got)
	w.Write([]byte("err: \"x\nforged line\r\x1b[31m\ttab\"\n"))
	w.Close(5 * time.Second)
	if want := "err: \"x\\x0aforged line\\x0d\\x1b[31m\ttab\"\n"; got.String() != want {
		t.Fatalf("got %q, want %q", got.String(), want)
	}

	notDir := filepath.Join(t.TempDir(), "file")
	os.WriteFile(notDir, nil, 0o600)
	t.Cleanup(func() { log.SetOutput(os.Stderr); log.SetPrefix(""); log.SetFlags(log.LstdFlags) })
	fw, err := Start(filepath.Join(notDir, "gui.log"), "gui", "v1", false)
	if err == nil || fw == nil {
		t.Fatalf("Start under a file: %v, %v", fw, err)
	}
	fw.Close(time.Second)
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// TestLimiter: one line per key per interval, with the count held back.
func TestLimiter(t *testing.T) {
	l := Limiter{Every: 10 * time.Second}
	t0 := time.Now()
	if ok, held := l.Allow("a", t0); !ok || held != 0 {
		t.Fatalf("first: %v %d", ok, held)
	}
	for i := 1; i <= 3; i++ {
		if ok, _ := l.Allow("a", t0.Add(time.Duration(i)*time.Second)); ok {
			t.Fatalf("second %d passed", i)
		}
	}
	if ok, _ := l.Allow("b", t0); !ok {
		t.Fatal("another key was held")
	}
	if ok, held := l.Allow("a", t0.Add(10*time.Second)); !ok || held != 3 {
		t.Fatalf("after the interval: %v, held %d, want 3", ok, held)
	}
}
