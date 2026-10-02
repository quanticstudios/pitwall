package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/proto"
)

// TestMain runs pitwall's main instead of the tests when a test re-executes
// this binary as pitwall.
func TestMain(m *testing.M) {
	if os.Getenv("PITWALL_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestDaemonsRaceForLock(t *testing.T) {
	runDir, stateDir := t.TempDir(), t.TempDir()
	exited := make(chan *exec.Cmd, 2)
	var cmds []*exec.Cmd
	for range 2 {
		cmd := exec.Command(os.Args[0], "daemon")
		cmd.Env = append(os.Environ(), "PITWALL_TEST_MAIN=1", "XDG_RUNTIME_DIR="+runDir, "XDG_STATE_HOME="+stateDir)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill() })
		cmds = append(cmds, cmd)
		go func() { cmd.Wait(); exited <- cmd }()
	}

	var loser *exec.Cmd
	select {
	case loser = <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("both daemons kept running")
	}
	if code := loser.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("losing daemon exited %d, want 0", code)
	}
	winner := cmds[0]
	if winner == loser {
		winner = cmds[1]
	}

	c, err := proto.Dial(filepath.Join(runDir, "pitwall", "pitwall.sock"))
	if err != nil {
		t.Fatalf("winner does not serve: %v", err)
	}
	defer c.Close()
	if err := c.Send(proto.Hello{Version: proto.Version, Kind: "gui"}); err != nil {
		t.Fatal(err)
	}
	if m, err := c.Recv(); err != nil {
		t.Fatal(err)
	} else if _, ok := m.(proto.StateMsg); !ok {
		t.Fatalf("got %T, want StateMsg", m)
	}

	winner.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("winner did not stop on SIGTERM")
	}
	if code := winner.ProcessState.ExitCode(); code != 0 {
		t.Fatalf("winner exited %d, want 0", code)
	}
}
