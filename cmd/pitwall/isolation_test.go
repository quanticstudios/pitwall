//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunStaysOffHome runs this package's other tests again with HOME in a
// temporary directory and no XDG variables, and fails if any of them wrote
// pitwall's state, config or cache under it: a test run must never reach the
// user's daemon.log or state.json.
func TestRunStaysOffHome(t *testing.T) {
	if os.Getenv("PITWALL_TEST_GUARD") != "" {
		t.Skip("inside the guarded run")
	}
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.skip=^TestRunStaysOffHome$", "-test.count=1")
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "XDG_RUNTIME_DIR", "XDG_CACHE_HOME", "PITWALL_SOCKET", "PITWALL_PANE":
		default:
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+home, "PITWALL_TEST_GUARD=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tests with a temporary HOME: %v\n%s", err, out)
	}
	for _, dir := range []string{".local/state/pitwall", ".config/pitwall", ".cache/pitwall"} {
		if _, err := os.Stat(filepath.Join(home, dir)); err == nil {
			t.Errorf("a test wrote ~/%s", dir)
		}
	}
}
