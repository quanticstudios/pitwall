//go:build unix

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
)

func TestWorktreePTYWithoutControllingTerminalDoesNotPrompt(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	defer slave.Close()
	if foregroundTerminal(slave) {
		t.Fatal("an unattended PTY can prompt")
	}
}

func TestWorktreeInterruptCancelsTeardown(t *testing.T) {
	repo := gitRepo(t)
	t.Chdir(repo)
	t.Setenv("PITWALL_SOCKET", filepath.Join(t.TempDir(), "none.sock"))
	t.Setenv("PITWALL_PANE", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	path, _, err := gitstat.AddWorktree(context.Background(), repo, "feature", model.WorktreeFrom{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("[worktrees]\nteardown = 'touch ../../started; sleep 1; touch ../../late'"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "worktree", "rm", "feature")
	command.Env = append(os.Environ(), "PITWALL_TEST_MAIN=1")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { command.Process.Kill() })
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err := os.Stat(filepath.Join(repo, "started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("teardown did not start: %s", &stderr)
		}
		time.Sleep(time.Millisecond)
	}
	if err := command.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("interrupted removal succeeded")
	}
	if !strings.Contains(stderr.String(), "context canceled") {
		t.Fatalf("CLI did not handle interrupt: %s", &stderr)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(repo, "late")); !os.IsNotExist(err) {
		t.Fatal("teardown survived Ctrl-C")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("interrupted teardown lost worktree", err)
	}
}
