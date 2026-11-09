// Package testenv keeps tests away from the user's own pitwall: its state
// and config directories, and the daemon socket.
package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/gitenv"
)

// Main runs m with XDG_STATE_HOME, XDG_CONFIG_HOME, XDG_RUNTIME_DIR and
// PITWALL_OMARCHY_DIR in a new temporary directory and PITWALL_SOCKET and
// PITWALL_PANE cleared, so no test logs, saves, dials or reads the desktop's
// theme outside it, then exits with m's code. A test can still point them
// elsewhere with t.Setenv. Git's inherited repository selection is unset before
// any test runs, so Git commands stay inside their temporary repositories.
func Main(m *testing.M) {
	for _, name := range strings.Fields(gitenv.RepositoryVars) {
		if err := os.Unsetenv(name); err != nil {
			fmt.Fprintln(os.Stderr, "testenv:", err)
			os.Exit(1)
		}
	}
	dir, err := os.MkdirTemp("", "pitwall-test")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testenv:", err)
		os.Exit(1)
	}
	for k, v := range map[string]string{
		"XDG_STATE_HOME":    filepath.Join(dir, "state"),
		"XDG_CONFIG_HOME":   filepath.Join(dir, "config"),
		"XDG_RUNTIME_DIR":   filepath.Join(dir, "run"),
		"PITWALL_SOCKET":    "",
		"PITWALL_PANE":      "",
		"PITWALL_PROFILE":   "",
		"CLAUDE_CONFIG_DIR": "",
		"CODEX_HOME":        "",
		// No Omarchy theme: a missing folder, so the defaults stay aide's.
		"PITWALL_OMARCHY_DIR": filepath.Join(dir, "omarchy"),
	} {
		os.Setenv(k, v)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
