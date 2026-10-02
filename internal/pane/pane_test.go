package pane

import (
	"bytes"
	"io"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// fakeVT records every byte the pane feeds it.
type fakeVT struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	size [2]int
}

func (f *fakeVT) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(b)
}
func (f *fakeVT) Resize(c, r int)          { f.mu.Lock(); f.size = [2]int{c, r}; f.mu.Unlock() }
func (f *fakeVT) Snapshot() vt.Grid        { return vt.Grid{} }
func (f *fakeVT) SnapshotAt(int) vt.Grid   { return vt.Grid{} }
func (f *fakeVT) ScrollbackLen() int       { return 0 }
func (f *fakeVT) ScrollbackPushed() uint64 { return 0 }
func (f *fakeVT) Modes() vt.Modes          { return vt.Modes{} }
func (f *fakeVT) String() string           { f.mu.Lock(); defer f.mu.Unlock(); return f.buf.String() }

func start(t *testing.T, id string, cmd ...string) (*Pane, *fakeVT) {
	t.Helper()
	f := &fakeVT{}
	p, err := Start(Config{ID: id, Cmd: cmd, Cols: 80, Rows: 24,
		NewVT: func(int, int, io.Writer) vt.Emulator { return f }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p, f
}

func waitDone(t *testing.T, p *Pane) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit")
	}
}

func TestOutputAndDirty(t *testing.T) {
	p, f := start(t, "p1", "printf", "hi")
	select {
	case <-p.Dirty():
	case <-time.After(5 * time.Second):
		t.Fatal("Dirty never fired")
	}
	waitDone(t, p)
	if got := f.String(); !strings.Contains(got, "hi") {
		t.Fatalf("output %q", got)
	}
}

func TestExitCode(t *testing.T) {
	p, _ := start(t, "p1", "sh", "-c", "exit 3")
	waitDone(t, p)
	if got := p.ExitCode(); got != 3 {
		t.Fatalf("exit code %d", got)
	}
}

func TestResizeReachesChild(t *testing.T) {
	p, f := start(t, "p1", "sh", "-c", "read _; stty size")
	if err := p.Resize(100, 30); err != nil {
		t.Fatal(err)
	}
	p.Write([]byte("\n"))
	waitDone(t, p)
	if got := f.String(); !strings.Contains(got, "30 100") {
		t.Fatalf("stty size output %q", got)
	}
	if f.size != [2]int{100, 30} {
		t.Fatalf("emulator size %v", f.size)
	}
}

func TestEnv(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-sock")
	p, f := start(t, "p42", "sh", "-c", `printf '[%s|%s|%s]' "$PITWALL_PANE" "$TERM" "${TMUX-unset}"`)
	waitDone(t, p)
	if got := f.String(); !strings.Contains(got, "[p42|xterm-256color|unset]") {
		t.Fatalf("env output %q", got)
	}
}

func TestCwd(t *testing.T) {
	dir := t.TempDir()
	f := &fakeVT{}
	p, err := Start(Config{ID: "p1", Cmd: []string{"sleep", "100"}, Cwd: dir, Cols: 80, Rows: 24,
		NewVT: func(int, int, io.Writer) vt.Emulator { return f }})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if got := p.Cwd(); got != dir {
		t.Fatalf("cwd %q, want %q", got, dir)
	}
}

func TestCloseKillsAndDoesNotLeak(t *testing.T) {
	before := runtime.NumGoroutine()
	p, _ := start(t, "p1", "sleep", "100")
	p.Close()
	waitDone(t, p)
	p.Close() // idempotent
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("goroutines %d after Close, %d before", n, before)
	}
}

func TestCloseKillsWhenHUPIgnored(t *testing.T) {
	p, _ := start(t, "p1", "sh", "-c", `trap "" HUP; echo ready; sleep 100`)
	<-p.Dirty() // the trap is in place
	t0 := time.Now()
	p.Close()
	waitDone(t, p)
	if d := time.Since(t0); d < 2*time.Second || d > 4*time.Second {
		t.Fatalf("Close took %v, want the 2s SIGKILL path", d)
	}
}

func TestScrollback(t *testing.T) {
	p, err := Start(Config{ID: "p1", Cmd: []string{"seq", "30"}, Cols: 20, Rows: 5, NewVT: vt.New})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	waitDone(t, p)
	// 30 lines and the cursor row on 5 rows: 26 scrolled off.
	if n := p.ScrollbackLen(); n != 26 {
		t.Fatalf("ScrollbackLen %d", n)
	}
	if g := p.SnapshotAt(26); g.At(0, 0).Content != "1" || g.At(0, 4).Content != "5" {
		t.Fatalf("oldest rows %q %q", g.At(0, 0).Content, g.At(0, 4).Content)
	}
	if p.ScrollbackPushed() != 26 {
		t.Fatalf("pushed %d", p.ScrollbackPushed())
	}
}
