package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
)

// GuardHooked returns why pitwall's guard would not run for provider's
// agent reading its config from dir, nil when it would: Claude Code's
// settings.json or Codex's hooks.json there must run `pitwall hook
// <provider>` on every PreToolUse, from a file that exists, with hooks on.
// Only Claude Code and Codex have the guard in a hook.
func GuardHooked(provider model.Provider, dir string) error {
	var path string
	switch provider {
	case model.ProviderClaude:
		path = filepath.Join(dir, "settings.json")
	case model.ProviderCodex:
		path = filepath.Join(dir, "hooks.json")
	default:
		return fmt.Errorf("the guard covers Claude Code and Codex, not %q", provider)
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no %s, so pitwall's hooks are not installed in %s", filepath.Base(path), dir)
	}
	if err != nil {
		return err
	}
	var s struct {
		Off   bool `json:"disableAllHooks"`
		Hooks struct {
			Pre []struct {
				Matcher string `json:"matcher"`
				Hooks   []struct {
					Type    string `json:"type"`
					Command string `json:"command"`
				} `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if s.Off {
		return fmt.Errorf("%s turns every hook off with disableAllHooks", path)
	}
	for _, g := range s.Hooks.Pre {
		if g.Matcher != "" && g.Matcher != "*" {
			continue // why: a matcher would leave some tools unchecked
		}
		for _, h := range g.Hooks {
			if bin, ok := hookBinary(h.Command, provider); ok && h.Type == "command" && runnable(bin) {
				return nil
			}
		}
	}
	return fmt.Errorf("%s has no PreToolUse hook for every tool that runs an existing pitwall with hook %s", path, provider)
}

// hookBinary is the program of a command hooksJSON wrote for provider.
func hookBinary(command string, provider model.Provider) (string, bool) {
	bin, ok := strings.CutSuffix(command, " hook "+string(provider))
	if !ok {
		return "", false
	}
	if q, ok := strings.CutPrefix(bin, "'"); ok { // commandPath's POSIX quoting
		bin = strings.ReplaceAll(strings.TrimSuffix(q, "'"), `'\''`, "'")
	}
	bin = strings.Trim(bin, `"`)
	return bin, bin != ""
}

// runnable reports whether path is a file the agent can run.
func runnable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && (runtime.GOOS == "windows" || fi.Mode()&0o111 != 0)
}
