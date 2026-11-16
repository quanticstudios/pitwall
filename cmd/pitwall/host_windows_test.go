//go:build windows

package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Windows' ssh shares no connection: every use is its own invocation and
// the forward a dedicated -N -L process that serves a loopback TCP port.
func TestWindowsSSHArgs(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	r, err := newSSHHost("box", nil)
	if err != nil {
		t.Skipf("no ssh: %v", err)
	}
	for _, a := range r.shared() {
		if strings.Contains(a, "ControlMaster") || strings.Contains(a, "ControlPath") ||
			strings.Contains(a, "ControlPersist") || strings.Contains(a, "StreamLocal") {
			t.Errorf("shared() passes %q, which Windows' ssh lacks", a)
		}
	}
	if r.localNet != "tcp" {
		t.Fatalf("localNet = %q, want tcp", r.localNet)
	}
	if _, err := net.ResolveTCPAddr("tcp", r.local); err != nil {
		t.Fatalf("local %q is not host:port: %v", r.local, err)
	}
	sock := "/run/user/1000/pitwall/pitwall.sock"
	want := append(slices.Clone(r.shared()), "-N", "-o", "ExitOnForwardFailure=yes", "-L", r.local+":"+sock, "--", "box")
	if got := r.sshArgs([]string{"-N", "-o", "ExitOnForwardFailure=yes", "-L", r.local + ":" + sock}); !slices.Equal(got, want) {
		t.Errorf("forward args\n got %q\nwant %q", got, want)
	}
}

// The forward runs through fakeSSHWin, a stand-in for ssh that relays the
// -L port to the host's unix socket. forwardStart returns once it answers,
// control "cancel" kills it, and the port stops serving.
func TestWindowsForward(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PITWALL_TEST_SSH", dir)
	// The host's daemon socket, here a unix listener that echoes a byte.
	sock := filepath.Join(dir, "daemon.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("the test needs unix sockets on Windows: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				io.Copy(c, c)
				c.Close()
			}()
		}
	}()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	local := free.Addr().String()
	free.Close()
	r := &sshHost{name: "box", ssh: os.Args[0], dest: "box", local: local, localNet: "tcp"}
	if err := r.control("forward", sock); err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("tcp", r.local, 2*time.Second)
	if err != nil {
		t.Fatalf("the forward does not answer: %v", err)
	}
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Write([]byte{7}); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1)
	if _, err := io.ReadFull(c, b); err != nil || b[0] != 7 {
		t.Fatalf("echo through the forward: %v", err)
	}
	c.Close()
	if err := r.control("cancel", sock); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if _, err := net.DialTimeout("tcp", r.local, 100*time.Millisecond); err != nil {
			return // the killed ssh took the port down with it
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the port still answers after cancel")
}

// A process in a KILL_ON_JOB_CLOSE job dies when the job's last handle
// closes, which is how the forward's ssh dies with a crashed window.
func TestKillJob(t *testing.T) {
	j, err := newKillJob()
	if err != nil {
		t.Fatalf("newKillJob: %v", err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", "Start-Sleep 60")
	if err := cmd.Start(); err != nil {
		t.Skipf("no powershell.exe: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	if err := jobAssign(j, cmd); err != nil {
		cmd.Process.Kill()
		windows.CloseHandle(j)
		t.Fatalf("jobAssign: %v", err)
	}
	windows.CloseHandle(j)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("closing the job did not kill the process")
	}
}

// fakeSSHWin stands in for ssh on Windows: it finds -L local:remote in
// pitwall's arguments, listens TCP on local and forwards each connect to
// remote, a unix socket. It serves until killed, as ssh -N would.
func fakeSSHWin(args []string) int {
	os.Unsetenv("PITWALL_TEST_SSH")
	var fwd string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-L" {
			fwd = args[i+1]
		}
	}
	// fwd is host:port:remote, and remote may carry a drive letter's colon.
	first, second := strings.Index(fwd, ":"), 0
	if first > 0 {
		second = strings.Index(fwd[first+1:], ":")
	}
	if fwd == "" || first <= 0 || second <= 0 {
		fmt.Fprintln(os.Stderr, "fakeSSHWin: no -L in", args)
		return 255
	}
	local, remote := fwd[:first+1+second], fwd[first+1+second+1:]
	ln, err := net.Listen("tcp", local)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeSSHWin:", err)
		return 255
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			return 0
		}
		go func() {
			defer c.Close()
			d, err := net.Dial("unix", remote)
			if err != nil {
				return
			}
			go func() { io.Copy(d, c); d.Close() }()
			io.Copy(c, d)
		}()
	}
}
