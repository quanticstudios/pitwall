//go:build linux || darwin

package pane

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/quanticstudios/pitwall/internal/vt"
	"golang.org/x/sys/unix"
)

func screen(p *Pane) string {
	g := p.Snapshot()
	var b strings.Builder
	for y := range g.Rows {
		for x := range g.Cols {
			b.WriteString(g.At(x, y).Content)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func awaitScreen(t *testing.T, p *Pane, want string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !strings.Contains(screen(p), want); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("screen never showed %q:\n%s", want, screen(p))
		}
	}
}

const echoLoop = `stty -echo; echo ready; while read l; do echo "out:$l"; done`

// A paused pane's master and snapshot rebuild the pane elsewhere: output
// the program wrote while paused reaches the new pane, never the old one.
func TestPauseHandsOver(t *testing.T) {
	p, err := Start(Config{Cols: 80, Rows: 24, NewVT: vt.New, Cmd: []string{"sh", "-c", echoLoop}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Resume(); p.Close() })
	awaitScreen(t, p, "ready")

	p.Pause()
	if n, err := p.Write([]byte("lost\n")); n != 0 || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Write while paused = %d, %v", n, err)
	}
	snap, err := p.Save()
	if err != nil {
		t.Fatal(err)
	}
	master, pid, err := p.Master()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := master.Write([]byte("during\n")); err != nil {
		t.Fatal(err)
	}
	restore := func(_, _ int, reply io.Writer) vt.Emulator {
		e, err := vt.Restore(snap, reply)
		if err != nil {
			t.Error(err)
			return vt.New(80, 24, reply)
		}
		return e
	}
	q, err := Prepare(Config{Cols: 80, Rows: 24, NewVT: restore}, master, pid)
	if err != nil {
		t.Fatal(err)
	}
	q.Start()
	t.Cleanup(func() { q.Close() })
	awaitScreen(t, q, "out:during")
	if !strings.Contains(screen(q), "ready") {
		t.Fatalf("snapshot lost:\n%s", screen(q))
	}
	if _, err := q.Write([]byte("after\n")); err != nil {
		t.Fatal(err)
	}
	awaitScreen(t, q, "out:after")
	if s := screen(p); strings.Contains(s, "out:") {
		t.Fatalf("paused pane read output:\n%s", s)
	}
}

// Resume reads what came while paused, and a process that exited while
// paused still drains and reports its code. macOS holds an exiting process
// until its output is read, so there it exits only after Resume.
func TestPauseResume(t *testing.T) {
	p, err := Start(Config{Cols: 80, Rows: 24, NewVT: vt.New, Cmd: []string{"sh", "-c", `stty -echo; echo ready; read l; echo "bye:$l"; exit 3`}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Resume(); p.Close() })
	awaitScreen(t, p, "ready")
	p.Pause()
	p.Pause() // a second pause is a no-op
	master, _, err := p.Master()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	if _, err := master.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" {
		select {
		case <-p.exited:
		case <-time.After(5 * time.Second):
			t.Fatal("process did not exit")
		}
		if _, _, err := p.Master(); err == nil {
			t.Fatal("Master of an exited process succeeded")
		}
	}
	p.Resume()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("pane did not finish after Resume")
	}
	if p.ExitCode() != 3 || !strings.Contains(screen(p), "bye:x") {
		t.Fatalf("exit %d, screen:\n%s", p.ExitCode(), screen(p))
	}
}

// Release gives a prepared pane up without signalling its process or
// closing the master the old owner still holds.
func TestPrepareRelease(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	// The daemon's master is on Go's poller, which Prepare's switch of the
	// shared file description to non-blocking does not disturb.
	if master, err = pollable(master); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close() })
	cmd := exec.Command("sh", "-c", `stty -echo; read l; echo "got:$l"`)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	slave.Close()
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	var dup int
	rc, err := master.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	rc.Control(func(fd uintptr) { dup, err = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0) })
	if err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(Config{Cols: 80, Rows: 24, NewVT: vt.New}, os.NewFile(uintptr(dup), "dup"), cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	p.Release()
	if _, err := master.Write([]byte("hi\n")); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(master)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("child gone after Release: %v", err)
		}
		if strings.Contains(line, "got:hi") {
			break
		}
	}
}
