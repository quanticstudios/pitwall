//go:build unix

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/quanticstudios/pitwall/internal/daemon"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/testenv"
)

// TestMain runs pitwall's main instead of the tests when a test re-executes
// this binary as pitwall.
func TestMain(m *testing.M) {
	if file := os.Getenv("PITWALL_DRILL_SERVE"); file != "" {
		os.Exit(serveDrill(file))
	}
	if mode := os.Getenv("PITWALL_TEST_OLD"); mode != "" {
		if err := fakeOldDaemon(mode); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if dir := os.Getenv("PITWALL_TEST_SSH"); dir != "" {
		os.Exit(fakeSSH(dir, os.Args[1:]))
	}
	if os.Getenv("PITWALL_TEST_MAIN") == "1" {
		if len(os.Args) == 2 && os.Args[1] == "daemon" {
			if err := fakeOldDaemon("healthy"); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			os.Exit(0)
		}
		setWindowSize(os.Getenv("PITWALL_TEST_SIZE"))
		main()
		os.Exit(0)
	}
	testenv.Main(m)
}

func TestDaemonRefusesHeldLock(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	path, err := proto.SocketPath()
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := runDaemon(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("held lock created a socket: %v", err)
	}
}

// fakeOldDaemon holds the daemon lock until the test sends a signal.
func fakeOldDaemon(mode string) error {
	if mode == "stuck" {
		signal.Ignore(syscall.SIGTERM)
	}
	path, err := proto.SocketPath()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil
		}
		return err
	}
	if mode != "empty" {
		if err := lock.Truncate(0); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(lock, "%d\n", os.Getpid()); err != nil {
			return err
		}
	}
	if mode == "healthy" {
		os.Remove(path)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer ln.Close()
	fmt.Fprintln(os.Stdout, "ready")
	for {
		nc, err := ln.Accept()
		if err != nil {
			return err
		}
		if mode != "raw" && mode != "silent" {
			conn := proto.NewConn(nc)
			if _, err := conn.Recv(); err != nil {
				conn.Close()
				return err
			}
			if mode == "healthy" {
				conn.Send(proto.StateMsg{State: model.State{Workspaces: []model.Workspace{{ID: "fake", Name: "fake"}}}})
				conn.Close()
				continue
			}
			if mode == "behind" { // this Version, the Level before this one
				conn.Send(proto.StateMsg{Level: proto.Level - 1})
				conn.Close()
				continue
			}
			if mode == "older" { // this Level, an older release
				conn.Send(proto.StateMsg{Level: proto.Level, Build: "v0.0.1"})
				conn.Close()
				continue
			}
			if err := conn.Send(proto.Frame{}); err != nil {
				conn.Close()
				return err
			}
			if err := conn.Send(proto.Error{Message: "daemon speaks protocol version 15; send Hello{Version: 15} first"}); err != nil {
				conn.Close()
				return err
			}
		} else {
			var header [4]byte
			if _, err := io.ReadFull(nc, header[:]); err != nil {
				nc.Close()
				return err
			}
			if mode == "silent" {
				io.Copy(io.Discard, nc)
			}
		}
		nc.Close()
	}
}

func TestLockHolderFromProcLocks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux has /proc/locks")
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "pitwall.sock.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	pid, err := lockHolder(f)
	if err != nil || pid != os.Getpid() {
		t.Fatalf("lockHolder = %d, %v; want %d", pid, err, os.Getpid())
	}
}

// TestNoTypeSafeCalls: no pitwall code reads the old key's variable or names
// the old decision service's host.
func TestNoTypeSafeCalls(t *testing.T) {
	banned := []string{"typesafe" + ".ai", "TYPESAFE_" + "API_KEY"}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if n := e.Name(); n == ".git" || n == ".ref" || n == ".worktrees" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".js", ".html", ".sh":
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, w := range banned {
			if bytes.Contains(b, []byte(w)) {
				t.Errorf("%s names %s", path, w)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// main hands adopt to the CLI, which answers with adopt's own error
// instead of the usage text and exit 2.
func TestMainRunsAdopt(t *testing.T) {
	fakeCLI(t, cliExchange{state: cliState()})
	cmd := exec.Command(os.Args[0], "adopt", "--all")
	cmd.Env = append(os.Environ(), "PITWALL_TEST_MAIN=1")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "adopt must run inside a pitwall pane") {
		t.Fatalf("%v: %s %s", err, out.String(), stderr.String())
	}
}

// The daemon's Build is the version alone, whichever binary runs it: the
// window treats a daemon pitwall-core started as any other.
func TestBuildHasNoFlavor(t *testing.T) {
	setHome(t, t.TempDir())
	t.Setenv("PITWALL_SOCKET", "")
	defer func(b string) { daemon.Build = b }(daemon.Build)
	daemonSetup(filepath.Join(t.TempDir(), "pitwall.sock"))
	if daemon.Build != versionString() || !strings.HasPrefix(versionLine(), versionString()) {
		t.Fatalf("Build %q, version %q, --version %q", daemon.Build, versionString(), versionLine())
	}
}
