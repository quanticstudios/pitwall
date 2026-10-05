// Package testenv keeps tests away from the user's own pitwall: its state
// and config directories, and the daemon socket.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Main runs m with XDG_STATE_HOME, XDG_CONFIG_HOME and XDG_RUNTIME_DIR in a
// new temporary directory and PITWALL_SOCKET and PITWALL_PANE cleared, so no
// test logs, saves or dials outside it, then exits with m's code. A test can
// still point them elsewhere with t.Setenv.
func Main(m *testing.M) {
	dir, err := os.MkdirTemp("", "pitwall-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testenv:", err)
		os.Exit(1)
	}
	for k, v := range map[string]string{
		"XDG_STATE_HOME":  filepath.Join(dir, "state"),
		"XDG_CONFIG_HOME": filepath.Join(dir, "config"),
		"XDG_RUNTIME_DIR": filepath.Join(dir, "run"),
		"PITWALL_SOCKET":  "",
		"PITWALL_PANE":    "",
	} {
		os.Setenv(k, v)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
