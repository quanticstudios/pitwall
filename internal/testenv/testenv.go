// Package testenv keeps tests away from the user's own pitwall: its state
// and config directories, and the daemon socket.
package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/gitenv"
)

// Main runs m with HOME (on Unix), XDG_STATE_HOME, XDG_CONFIG_HOME,
// XDG_RUNTIME_DIR and PITWALL_OMARCHY_DIR in a new temporary directory and
// PITWALL_SOCKET and PITWALL_PANE cleared, so no test logs, saves, dials or
// reads the desktop's theme outside it, then exits with m's code. GOCACHE,
// GOMODCACHE and GOPATH keep their real values, so a go build inside a test
// still hits the cache. A test can still point them elsewhere with t.Setenv.
// Git's inherited repository selection is unset before any test runs, so Git
// commands stay inside their temporary repositories. Each of setup runs
// after that and before m, for a package that reads the environment at init.
func Main(m *testing.M, setup ...func()) {
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
	env := map[string]string{
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
	}
	// why: Windows finds the home through USERPROFILE, and Git for Windows
	// reads HOME for its global config.
	if runtime.GOOS != "windows" {
		env["HOME"] = filepath.Join(dir, "home")
		if err := os.Mkdir(env["HOME"], 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "testenv:", err)
			os.Exit(1)
		}
		// Read before HOME and XDG_CONFIG_HOME move, since both feed go env.
		if out, err := exec.Command("go", "env", "GOCACHE", "GOMODCACHE", "GOPATH").Output(); err == nil {
			if v := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); len(v) == 3 {
				env["GOCACHE"], env["GOMODCACHE"], env["GOPATH"] = v[0], v[1], v[2]
			}
		}
	}
	for k, v := range env {
		os.Setenv(k, v)
	}
	for _, f := range setup {
		f()
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
