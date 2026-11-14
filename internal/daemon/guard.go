package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
)

// errUnguarded marks a guarded pane that did not start because the guard
// could not hold in it.
var errUnguarded = errors.New("guarded tab not started")

// guardGate refuses a pane whose environment turns the guard on when the
// guard would not run in it: its program is not Claude Code or Codex, or
// pitwall's PreToolUse hook is missing from the config dir that program
// reads. env is what the pane gets on top of the daemon's own environment,
// so a later entry wins, as it does for the process.
func guardGate(cmd, env []string) error {
	get := func(k string) string {
		for i := len(env) - 1; i >= 0; i-- {
			if v, ok := strings.CutPrefix(env[i], k+"="); ok {
				return v
			}
		}
		return os.Getenv(k)
	}
	if get(guard.Env) == "" {
		return nil // the hook guards only while it is set, to anything
	}
	program, provider := "a shell", model.Provider("")
	if len(cmd) > 0 {
		program, provider = filepath.Base(cmd[0]), agent.Identify("", cmd[0])
	}
	home, _ := os.UserHomeDir()
	var key, dir string
	switch provider {
	case model.ProviderClaude:
		key, dir = "CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude")
	case model.ProviderCodex:
		key, dir = "CODEX_HOME", filepath.Join(home, ".codex")
	default:
		return fmt.Errorf("%w: the guard covers Claude Code and Codex, not %s", errUnguarded, program)
	}
	if v := get(key); v != "" {
		dir = v
	}
	if err := agent.GuardHooked(provider, dir); err != nil {
		return fmt.Errorf("%w: %v; run %s=%s pitwall hooks install", errUnguarded, err, key, dir)
	}
	return nil
}
