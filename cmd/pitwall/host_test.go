//go:build unix

package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/app"
)

// Every ssh call shares one connection, and the destination's port, user,
// key and jump host stay in the user's ssh config: pitwall passes none.
func TestRemoteSSHArgs(t *testing.T) {
	fakeHost(t)
	run := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", run)
	dir := filepath.Join(run, "pitwall")
	shared := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=" + dir + "/ssh-%C",
		"-o", "ControlPersist=10m",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ConnectTimeout=10",
	}
	hosts := []config.Host{{Name: "box", SSH: "me@box.example"}, {Name: "gpu", SSH: "gpu-alias"}, {Name: "bare"}}
	for _, c := range []struct {
		name, dest string
		restart    bool
		start      string
	}{
		{"box", "me@box.example", false, `sh -c 'PATH="$PATH:$HOME/.local/bin" exec pitwall remote-start'`},
		{"gpu", "gpu-alias", true, `sh -c 'PATH="$PATH:$HOME/.local/bin" exec pitwall remote-start --restart'`},
		{"bare", "bare", false, `sh -c 'PATH="$PATH:$HOME/.local/bin" exec pitwall remote-start'`},
		{"me@elsewhere", "me@elsewhere", false, `sh -c 'PATH="$PATH:$HOME/.local/bin" exec pitwall remote-start'`},
	} {
		r, err := newSSHHost(c.name, hosts)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(dir, fmt.Sprintf("remote-%d.sock", os.Getpid())); r.local != want {
			t.Errorf("%s: local socket %s, want %s", c.name, r.local, want)
		}
		if got, want := r.sshArgs(nil, startCommand(c.restart)), append(slices.Clone(shared), "--", c.dest, c.start); !slices.Equal(got, want) {
			t.Errorf("%s: start\n got %q\nwant %q", c.name, got, want)
		}
		fwd := []string{"-O", "forward", "-L", r.local + ":/run/user/1000/pitwall/pitwall.sock"}
		if got, want := r.sshArgs(fwd), append(append(slices.Clone(shared), fwd...), "--", c.dest); !slices.Equal(got, want) {
			t.Errorf("%s: forward\n got %q\nwant %q", c.name, got, want)
		}
	}
	if _, err := newSSHHost("-oProxyCommand=x", nil); err == nil {
		t.Error("an option as the destination was accepted")
	}
}

func TestParseHost(t *testing.T) {
	for _, c := range []struct {
		args          []string
		host, session string
		ok            bool
	}{
		{[]string{"box"}, "box", "", true},
		{[]string{"box", "-s", "work"}, "box", "work", true},
		{nil, "", "", false},
		{[]string{""}, "", "", false},
		{[]string{"box", "-s"}, "", "", false},
		{[]string{"box", "work"}, "", "", false},
	} {
		if host, session, ok := parseHost(c.args); host != c.host || session != c.session || ok != c.ok {
			t.Errorf("parseHost(%q) = %q, %q, %v", c.args, host, session, ok)
		}
	}
}

// A host without pitwall, with one too old for remote-start, or with one of
// another proto.Version gets the install command; ssh's own failure says so.
func TestRemoteErrors(t *testing.T) {
	exit := func(code int) error { return exec.Command("sh", "-c", "exit "+strconv.Itoa(code)).Run() }
	prev := version
	t.Cleanup(func() { version = prev })
	r := &sshHost{name: "box", dest: "me@box"}
	release := "ssh me@box 'curl -fsSL https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh | PITWALL_VERSION=v0.1.0-alpha.30 sh'"
	latest := "ssh me@box 'curl -fsSL https://raw.githubusercontent.com/quanticstudios/pitwall/main/scripts/get.sh | sh'"
	started := func(v int) func() error {
		return func() error {
			_, err := r.parseStart(fmt.Sprintf("Welcome to box\n%s\t%d\t0\tv0.1.0-alpha.9\t/run/user/1000/pitwall/pitwall.sock\n", remoteMark, v))
			return err
		}
	}
	for i, c := range []struct {
		version string
		err     func() error
		want    string
	}{
		{"v0.1.0-alpha.30", func() error { return r.sshError("sh: 1: exec: pitwall: not found\n", exit(127)) },
			"pitwall is not installed on box: it is not on the PATH there or in ~/.local/bin. Install it with:\n  " + release},
		{"dev-0123456789ab", func() error { return r.sshError("sh: 1: exec: pitwall: not found\n", exit(127)) },
			"pitwall is not installed on box: it is not on the PATH there or in ~/.local/bin. Install it with:\n  " + latest},
		{"v0.1.0-alpha.30", func() error { return r.sshError("usage:\n  pitwall --version          print the version\n", exit(2)) },
			"pitwall on box is too old to serve a window over ssh. Update it with:\n  " + release},
		{"v0.1.0-alpha.30", func() error {
			return r.sshError("kex_exchange_identification: read: Connection reset by peer\nssh: connect to host box port 22: Connection refused\n", exit(255))
		}, "cannot reach box over ssh: ssh: connect to host box port 22: Connection refused"},
		{"v0.1.0-alpha.30", func() error { return r.sshError("", exit(3)) }, "pitwall on box failed: exit status 3"},
		{"v0.1.0-alpha.30", func() error { return r.sshError("pitwall: daemon did not come up\n", exit(1)) }, "pitwall on box failed: daemon did not come up"},
		{"v0.1.0-alpha.30", started(proto.Version - 1), fmt.Sprintf("pitwall v0.1.0-alpha.9 on box speaks protocol version %d and this pitwall v0.1.0-alpha.30 speaks %d. Install a matching pitwall there with:\n  %s", proto.Version-1, proto.Version, release)},
		{"v0.1.0-alpha.30", started(proto.Version + 1), fmt.Sprintf("pitwall v0.1.0-alpha.9 on box speaks protocol version %d and this pitwall v0.1.0-alpha.30 speaks %d. Update this pitwall, or install a matching one there with:\n  %s", proto.Version+1, proto.Version, release)},
		{"v0.1.0-alpha.30", func() error { _, err := r.parseStart("motd\n"); return err }, `pitwall on box did not report its socket; it printed "motd\n"`},
	} {
		version = c.version
		if got := fmt.Sprint(c.err()); got != c.want {
			t.Errorf("%d:\n got %q\nwant %q", i, got, c.want)
		}
	}
	if sock, err := r.parseStart(fmt.Sprintf("motd\n%s\t%d\t9\tv0.1.0-alpha.99\t/run/user/1000/pitwall/pitwall.sock\r\n", remoteMark, proto.Version)); err != nil || sock != "/run/user/1000/pitwall/pitwall.sock" {
		t.Errorf("same version, any level: %q, %v", sock, err)
	}
}

// fakeSSH stands in for ssh on PATH. It logs its arguments to dir/ssh.log
// and runs a command here with sh, as the host's login shell would. -O
// forward starts a relay process, the shared connection, which serves the
// forward until -O cancel or a test kills it as a dropped link would.
func fakeSSH(dir string, args []string) int {
	os.Unsetenv("PITWALL_TEST_SSH") // the pitwall the command runs is not ssh
	if len(args) == 3 && args[0] == "relay" {
		relay(args[1], args[2])
		return 0
	}
	if f, err := os.OpenFile(filepath.Join(dir, "ssh.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		fmt.Fprintln(f, strings.Join(args, " "))
		f.Close()
	}
	var op, fwd string
	i := 0
	for ; i < len(args) && args[i] != "--"; i++ {
		switch args[i] {
		case "-o":
			i++
		case "-O":
			i++
			op = args[i]
		case "-L":
			i++
			fwd = args[i]
		}
	}
	local, remote, _ := strings.Cut(fwd, ":")
	pidFile := filepath.Join(dir, "relay.pid")
	switch op {
	case "":
		cmd := exec.Command("sh", "-c", strings.Join(args[i+2:], " "))
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		err := cmd.Run()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		} else if err != nil {
			return 255
		}
	case "forward":
		// pitwall forwards only while it has no connection, so a new relay
		// in place of a live one drops nothing.
		killRelay(dir)
		cmd := exec.Command(os.Args[0], "relay", local, remote)
		cmd.Env = append(os.Environ(), "PITWALL_TEST_SSH="+dir)
		ready, err := cmd.StdoutPipe()
		if err != nil || cmd.Start() != nil {
			return 255
		}
		os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		if _, err := bufio.NewReader(ready).ReadString('\n'); err != nil {
			return 255
		}
	case "cancel":
		killRelay(dir)
	}
	return 0
}

// relay is the fake shared connection's forward.
func relay(local, remote string) {
	os.Remove(local)
	ln, err := net.Listen("unix", local)
	if err != nil {
		return
	}
	fmt.Println("ready")
	for {
		c, err := ln.Accept()
		if err != nil {
			return
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

// fakeHost puts scripts named ssh and pitwall on PATH that run this test
// binary as fakeSSH and as pitwall, and returns the directory fakeSSH logs
// to.
func fakeHost(t *testing.T) string {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, env := range map[string]string{"ssh": "PITWALL_TEST_SSH='" + dir + "'", "pitwall": "PITWALL_TEST_MAIN=1"} {
		script := fmt.Sprintf("#!/bin/sh\n%s exec '%s' \"$@\"\n", env, os.Args[0])
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return dir
}

func killRelay(dir string) {
	data, err := os.ReadFile(filepath.Join(dir, "relay.pid"))
	if pid, _ := strconv.Atoi(string(data)); err == nil && pid > 1 {
		syscall.Kill(pid, syscall.SIGKILL)
	}
	os.Remove(filepath.Join(dir, "relay.pid"))
}

// TestRemoteReconnects runs pitwall --host's dial through a fake ssh whose
// "host" is this machine: remote-start runs there and reports a daemon
// socket, which the shared connection forwards. When the link drops, the
// window reconnects through a new forward to the same daemon.
func TestRemoteReconnects(t *testing.T) {
	dir := fakeHost(t)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "run"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("PITWALL_PANE", "")
	// remote-start, run on the "host", reports this socket; the window
	// reaches it only through the forward.
	sock := filepath.Join(dir, "host.sock")
	t.Setenv("PITWALL_SOCKET", sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Cleanup(func() { killRelay(dir) })

	accept := func() *proto.Conn {
		t.Helper()
		for {
			ln.(*net.UnixListener).SetDeadline(time.Now().Add(10 * time.Second))
			nc, err := ln.Accept()
			if err != nil {
				t.Fatalf("no dial: %v", err)
			}
			nc.SetDeadline(time.Now().Add(10 * time.Second))
			c := proto.NewConn(nc)
			m, err := c.Recv()
			if err != nil {
				nc.Close() // remote-start checking for a daemon
				continue
			}
			if h, ok := m.(proto.Hello); !ok || h.Kind != "gui" || h.Session != "work" {
				t.Fatalf("hello: %#v", m)
			}
			t.Cleanup(func() { nc.Close() })
			return c
		}
	}
	r, err := newSSHHost("box", []config.Host{{Name: "box", SSH: "me@box"}})
	if err != nil {
		t.Fatal(err)
	}
	type dialed struct {
		c   *proto.Conn
		st  proto.StateMsg
		err error
	}
	first := make(chan dialed, 1)
	go func() { c, st, err := r.dial("work", false); first <- dialed{c, st, err} }()
	d := accept()
	st := model.State{Version: 1, Sessions: []model.Session{{ID: "s1", Name: "work"}}}
	d.Send(proto.StateMsg{State: st})
	got := <-first
	if got.err != nil {
		t.Fatal(got.err)
	}
	b := newBackend(got.c, "s1")
	// Once the test is over, the backend's next try ends its dial loop.
	var over atomic.Bool
	defer over.Store(true)
	b.state, b.name = got.st.State, "work"
	b.redial = func(session string, restart bool) (*proto.Conn, proto.StateMsg, error) {
		if over.Load() {
			return nil, proto.StateMsg{}, incompatible{proto.Version - 1}
		}
		return r.dial(session, restart)
	}
	go b.recvLoop()
	go b.sendLoop()

	killRelay(dir) // the link drops, and the forward with it
	d = accept()
	st.Version = 2
	d.Send(proto.StateMsg{State: st})
	for deadline := time.Now().Add(10 * time.Second); b.Link() != (app.Link{Epoch: 2}) || b.State().Version != 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no reconnect: link %+v, state version %d", b.Link(), b.State().Version)
		}
	}
	b.Send(proto.Input{Pane: "p", Data: []byte("x")})
	if m, err := d.Recv(); err != nil {
		t.Fatal(err)
	} else if in, ok := m.(proto.Input); !ok || in.Pane != "p" {
		t.Fatalf("%#v through the new forward", m)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "ssh.log"))
	log := string(data)
	// A slow try can time out and go again; each one starts and forwards.
	starts := strings.Count(log, "-- me@box sh -c 'PATH=\"$PATH:$HOME/.local/bin\" exec pitwall remote-start'")
	forwards := strings.Count(log, "-O forward -L "+r.local+":"+sock+" -- me@box")
	if starts < 2 || forwards != starts {
		t.Errorf("%d starts and %d forwards, want 2 or more of each:\n%s", starts, forwards, log)
	}

	over.Store(true)
	r.close()
	data, _ = os.ReadFile(filepath.Join(dir, "ssh.log"))
	if !strings.HasSuffix(string(data), "-O cancel -L "+r.local+":"+sock+" -- me@box\n") {
		t.Errorf("no cancel on close:\n%s", data)
	}
	if _, err := os.Stat(r.local); !os.IsNotExist(err) {
		t.Errorf("forwarded socket left behind: %v", err)
	}
}

// refuses tells a daemon that turns this proto.Version away, which
// remote-start --restart replaces, from one that serves it.
func TestRemoteRestartChecksVersion(t *testing.T) {
	for _, c := range []struct {
		answer any
		want   bool
	}{
		{proto.Error{Message: fmt.Sprintf("daemon speaks protocol version %d; send Hello{Version: %d} first", proto.Version-1, proto.Version-1)}, true},
		{proto.StateMsg{}, false},
		{proto.Error{Message: "no such tab"}, false},
	} {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			d := proto.NewConn(b)
			if _, err := d.Recv(); err != nil {
				return
			}
			if _, err := d.Recv(); err != nil {
				return
			}
			d.Send(c.answer)
		}()
		if got := refuses(a); got != c.want {
			t.Errorf("%#v: refuses = %v", c.answer, got)
		}
		a.Close()
	}
}
