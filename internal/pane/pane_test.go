//go:build linux

package pane

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// fakeVT records every byte the pane feeds it.
type fakeVT struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	size  [2]int
	check func(cols, rows int) // called from Resize, while the pane holds its lock
}

func (f *fakeVT) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buf.Write(b)
}
func (f *fakeVT) Resize(c, r int) {
	if f.check != nil {
		f.check(c, r)
	}
	f.mu.Lock()
	f.size = [2]int{c, r}
	f.mu.Unlock()
}
func (f *fakeVT) Snapshot() vt.Grid                     { return vt.Grid{} }
func (f *fakeVT) SnapshotAt(int) vt.Grid                { return vt.Grid{} }
func (f *fakeVT) ScrollbackLen() int                    { return 0 }
func (f *fakeVT) ScrollbackPushed() uint64              { return 0 }
func (f *fakeVT) Search(string, int) ([]vt.Match, bool) { return nil, false }
func (f *fakeVT) Modes() vt.Modes                       { return vt.Modes{} }
func (f *fakeVT) String() string                        { f.mu.Lock(); defer f.mu.Unlock(); return f.buf.String() }

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

// An exited pane still resizes its screen: pitwall keeps a command's pane
// on screen after it ends, and its PTY is closed by then.
func TestResizeAfterExit(t *testing.T) {
	p, err := Start(Config{ID: "p1", Cmd: []string{"sh", "-c", "echo bye"}, Cols: 20, Rows: 5, NewVT: vt.New})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	waitDone(t, p)
	if err := p.Resize(40, 10); err != nil {
		t.Fatal(err)
	}
	if g := p.Snapshot(); g.Cols != 40 || g.Rows != 10 {
		t.Fatalf("screen %dx%d after resize", g.Cols, g.Rows)
	}
}

func TestEnv(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-sock")
	p, f := start(t, "p42", "sh", "-c", `printf '[%s|%s|%s]' "$PITWALL_PANE" "$TERM" "${TMUX-unset}"`)
	waitDone(t, p)
	if got := f.String(); !strings.Contains(got, "[p42|xterm-256color|unset]") {
		t.Fatalf("env output %q", got)
	}

	// A daemon started in a worktree tab's pane hands no pane its ports;
	// Config.Env still sets them.
	t.Setenv("PORT", "3010")
	t.Setenv("PITWALL_PORT_BASE", "3010")
	t.Setenv("PITWALL_PORTS", "3010-3019")
	p, f = start(t, "p43", "sh", "-c", `printf '[%s|%s|%s]' "${PORT-unset}" "${PITWALL_PORT_BASE-unset}" "${PITWALL_PORTS-unset}"`)
	waitDone(t, p)
	if got := f.String(); !strings.Contains(got, "[unset|unset|unset]") {
		t.Fatalf("inherited ports %q", got)
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

// A detached descendant that keeps the slave open must not keep the pane
// alive, or Close, once the pane's own process is gone.
func TestDetachedSlaveHolder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		close bool
		after string
	}{
		{"exits", false, "sleep 0.2"},
		{"closed", true, "sleep 100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, f := start(t, "p1", "sh", "-c",
				`setsid sh -c 'echo holder=$$; exec sleep 1000' </dev/null >/dev/tty 2>&1 & `+tc.after)
			holder := waitHolder(t, f)
			t.Cleanup(func() { syscall.Kill(holder, syscall.SIGKILL) }) // runs before start's Close
			finished := make(chan struct{})
			go func() {
				if tc.close {
					p.Close()
				}
				<-p.Done()
				close(finished)
			}()
			select {
			case <-finished:
			case <-time.After(3 * time.Second):
				t.Fatal("pane never finished while a detached process held its PTY")
			}
			if syscall.Kill(holder, 0) != nil {
				t.Fatal("the holder died; the test no longer covers a held slave")
			}
		})
	}
}

func waitHolder(t *testing.T, f *fakeVT) int {
	t.Helper()
	re := regexp.MustCompile(`holder=(\d+)`)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if m := re.FindStringSubmatch(f.String()); m != nil {
			pid, _ := strconv.Atoi(m[1])
			if comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid)); strings.TrimSpace(string(comm)) != "sleep" {
				continue // not exec'd yet
			}
			return pid
		}
	}
	t.Fatalf("holder pid not printed; output %q", f.String())
	return 0
}

func TestResizeRejectsBadSizes(t *testing.T) {
	p, f := start(t, "p1", "sleep", "100")
	for _, s := range [][2]int{{0, 24}, {80, 0}, {-1, 24}, {80, -1}, {70000, 24}, {80, 70000}, {1000, 1000}} {
		if err := p.Resize(s[0], s[1]); err == nil {
			t.Errorf("Resize(%d, %d) accepted", s[0], s[1])
		}
	}
	if f.size != [2]int{} {
		t.Fatalf("emulator resized to %v", f.size)
	}
	if err := p.Resize(1000, 500); err != nil {
		t.Fatalf("Resize(1000, 500): %v", err)
	}
}

func TestConcurrentResizesAgree(t *testing.T) {
	p, f := start(t, "p1", "sleep", "100")
	kernel := func() [2]int {
		var ws *unix.Winsize
		if err := p.control(func(fd int) (err error) { ws, err = unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); return }); err != nil {
			t.Error(err)
			return [2]int{}
		}
		return [2]int{int(ws.Col), int(ws.Row)}
	}
	// Another resize must not reach the kernel while this one is still
	// resizing the emulator.
	f.check = func(c, r int) {
		time.Sleep(100 * time.Microsecond)
		if k := kernel(); k != [2]int{c, r} {
			t.Errorf("emulator resizing to %dx%d while the kernel has %v", c, r, k)
		}
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			for j := range 50 {
				if err := p.Resize(10+i, 10+j%50); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if k := kernel(); f.size != k {
		t.Fatalf("emulator %v, kernel %v", f.size, k)
	}
}

// A synchronized-output frame that never ends must still reach the screen
// once its timeout passes, without the child writing anything else.
func TestSyncFrameTimeoutSignalsDirty(t *testing.T) {
	p, err := Start(Config{ID: "p1", Cols: 10, Rows: 1, NewVT: vt.New, Cmd: []string{"sh", "-c",
		`printf old; sleep 0.3; printf '\033[?2026h\r\033[Knew'; exec sleep 100`}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	row := func() string {
		g := p.Snapshot()
		var s strings.Builder
		for x := range g.Cols {
			s.WriteString(g.At(x, 0).Content)
		}
		return strings.TrimSpace(s.String())
	}
	for deadline := time.Now().Add(3 * time.Second); row() != "old"; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("screen %q, want old", row())
		}
	}
	timeout := time.After(3 * time.Second)
	for row() != "new" {
		select {
		case <-p.Dirty():
		case <-timeout:
			t.Fatalf("no dirty signal showed the timed-out frame; screen %q", row())
		}
	}
}

// Closed panes must free their emulator and its reply goroutines: deleting
// sessions all day must not pile up scrollback.
func TestClosedPanesAreFreed(t *testing.T) {
	before := runtime.NumGoroutine()
	var freed atomic.Int32
	newVT := func(c, r int, w io.Writer) vt.Emulator {
		e := vt.New(c, r, w)
		runtime.SetFinalizer(e, func(any) { freed.Add(1) })
		return e
	}
	const n = 50
	panes := make([]*Pane, n)
	for i := range panes {
		p, err := Start(Config{ID: strconv.Itoa(i), Cmd: []string{"sleep", "100"}, Cols: 80, Rows: 24, NewVT: newVT})
		if err != nil {
			t.Fatal(err)
		}
		panes[i] = p
	}
	for _, p := range panes {
		p.Close()
	}
	panes = nil
	deadline := time.Now().Add(5 * time.Second)
	for (freed.Load() < n || runtime.NumGoroutine() > before) && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
	if f, g := freed.Load(), runtime.NumGoroutine(); f < n || g > before {
		t.Fatalf("%d of %d emulators freed, goroutines %d after, %d before", f, n, g, before)
	}
}
