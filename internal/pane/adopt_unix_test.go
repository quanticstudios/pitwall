//go:build linux || darwin

package pane

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/quanticstudios/pitwall/internal/vt"
	"golang.org/x/sys/unix"
)

// The helper owns the child but deliberately never waits for it. Closing fd 4
// models the old daemon exiting after the replacement registers its watcher.
func TestAdoptHelper(t *testing.T) {
	if os.Getenv("PITWALL_ADOPT_HELPER") != "1" {
		return
	}
	slave := os.NewFile(3, "slave")
	control := os.NewFile(4, "control")
	cmd := exec.Command("sh", "-c", `trap '' HUP; echo ready; read line; `+os.Getenv("PITWALL_ADOPT_ACTION"))
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(cmd.Process.Pid)
	slave.Close()
	io.Copy(io.Discard, control)
	os.Exit(0)
}

func adoptedPane(t *testing.T, action string) (*Pane, func()) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { master.Close(); slave.Close() })
	controlR, controlW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controlR.Close(); controlW.Close() })
	helper := exec.Command(os.Args[0], "-test.run=^TestAdoptHelper$")
	helper.Env = append(os.Environ(), "PITWALL_ADOPT_HELPER=1", "PITWALL_ADOPT_ACTION="+action)
	helper.ExtraFiles = []*os.File{slave, controlR}
	helper.Stderr = os.Stderr
	out, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stopParent := func() {
		if !stopped {
			stopped = true
			controlW.Close()
			if err := helper.Wait(); err != nil {
				t.Error(err)
			}
		}
	}
	t.Cleanup(stopParent)
	slave.Close()
	controlR.Close()
	in := bufio.NewScanner(out)
	if !in.Scan() {
		t.Fatal("helper did not report child PID", in.Err())
	}
	pid, err := strconv.Atoi(in.Text())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Kill(-pid, unix.SIGKILL) })
	p, err := Adopt(Config{Cols: 80, Rows: 24, NewVT: vt.New}, master, pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		g := p.Snapshot()
		var output strings.Builder
		for y := range g.Rows {
			for x := range g.Cols {
				output.WriteString(g.At(x, y).Content)
			}
		}
		if strings.Contains(output.String(), "ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not become ready")
		}
	}
	return p, stopParent
}

func awaitExit(t *testing.T, p *Pane) {
	t.Helper()
	select {
	case <-p.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("pane did not report exit")
	}
}

func awaitReaped(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		err := unix.Kill(pid, 0)
		if err == unix.ESRCH {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("PID %d still exists after parent exit, possibly a zombie: %v", pid, err)
		}
	}
}

func TestAdoptExit(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		code         int
		parentAlive  bool
	}{
		{"orphan", "echo final-output; exit 7", 7, false},
		{"non-child", "exit 3", 3, true},
		{"signal", "kill -KILL $$", -1, false},
		{"slave-holder", `sh -c 'trap "" HUP; exec sleep 10' & exit 7`, 7, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, stopParent := adoptedPane(t, tc.action)
			want := adoptedExitCode(t, p.pid, tc.code)
			if !tc.parentAlive {
				stopParent()
			}
			select {
			case <-p.Done():
				t.Fatal("reported exit while the child was waiting for input")
			default:
			}
			if _, err := p.Write([]byte("exit\n")); err != nil {
				t.Fatal(err)
			}
			awaitExit(t, p)
			if got, expected := p.ExitCode(), want(); got != expected {
				t.Fatalf("exit code %d, want %d", got, expected)
			}
			if tc.name == "orphan" {
				g := p.Snapshot()
				found := false
				for y := range g.Rows {
					var row string
					for x := range g.Cols {
						row += g.At(x, y).Content
					}
					if strings.TrimSpace(row) == "final-output" {
						found = true
					}
				}
				if !found {
					t.Fatal("final output was not drained")
				}
			}
			stopParent()
			awaitReaped(t, p.pid)
		})
	}
}

func TestAdoptClose(t *testing.T) {
	p, stopParent := adoptedPane(t, "exit 7")
	stopParent()
	// The helper child ignores HUP, so this also exercises the SIGKILL path.
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	awaitExit(t, p)
	if p.ExitCode() != -1 {
		t.Fatalf("killed pane exit code %d", p.ExitCode())
	}
	p.Close()
	awaitReaped(t, p.pid)
}

func TestWatcherReapsOwnChild(t *testing.T) {
	cmd := exec.Command("sh", "-c", "read line; exit 9")
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); cmd.Process.Kill(); cmd.Process.Release() })
	w, err := watchExit(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	input.Close()
	code, err := w.Wait()
	if err != nil || code != 9 {
		t.Fatalf("Wait = %d, %v, want 9", code, err)
	}
	var status unix.WaitStatus
	if _, err := unix.Wait4(cmd.Process.Pid, &status, unix.WNOHANG, nil); err != unix.ECHILD {
		t.Fatalf("watcher did not reap child: %v", err)
	}
}

func TestAdoptFailureConsumesMaster(t *testing.T) {
	for _, pid := range []int{0, -1, 1 << 30} {
		f, err := os.CreateTemp(t.TempDir(), "master")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Adopt(Config{}, f, pid); err == nil {
			t.Fatalf("adopt PID %d succeeded", pid)
		}
		if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("master still open after failed adoption: %v", err)
		}
	}
	if _, err := Adopt(Config{}, nil, 1); err == nil {
		t.Fatal("nil master accepted")
	}
}

func TestAdoptClosedMaster(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "master")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := Adopt(Config{}, f, os.Getpid()); err == nil {
		t.Fatal("closed master accepted")
	}
}

func TestStartedExitUnchanged(t *testing.T) {
	p, err := Start(Config{Cols: 80, Rows: 24, NewVT: vt.New, Cmd: []string{"sh", "-c", "exit 3"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	awaitExit(t, p)
	if p.ExitCode() != 3 {
		t.Fatalf("normal exit code %d", p.ExitCode())
	}
	awaitReaped(t, p.pid)
}
