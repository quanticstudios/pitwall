package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// GuardHooked returns why pitwall's guard would not run for provider's
// agent reading its config from dir, nil when it would: Claude Code's
// settings.json or Codex's hooks.json there must run `pitwall hook
// <provider>` on every PreToolUse, from a file that exists, with hooks on.
// pi, omp and OpenCode require the current generated extension or plugin
// and its executable. Old generated files may predate the guard.
// For OpenCode, callers must also check the actual program with GuardOpenCode.
func GuardHooked(provider model.Provider, dir string) error {
	var path string
	switch provider {
	case model.ProviderClaude:
		path = filepath.Join(dir, "settings.json")
	case model.ProviderCodex:
		path = filepath.Join(dir, "hooks.json")
	case model.ProviderPi, model.ProviderOmp, model.ProviderOpenCode:
		return guardGenerated(provider, dir)
	default:
		return fmt.Errorf("the guard does not cover %q", provider)
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
					Type        string          `json:"type"`
					Command     string          `json:"command"`
					If          string          `json:"if"`
					Async       bool            `json:"async"`
					AsyncRewake bool            `json:"asyncRewake"`
					Args        json.RawMessage `json:"args"`
					Shell       string          `json:"shell"`
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
			// The installed hook runs synchronously on every call, through
			// the shell that commandPath quotes for. Exec-form args would
			// instead treat the entire command string as one executable.
			if h.If != "" || h.Async || h.AsyncRewake || h.Args != nil || h.Shell != "" && h.Shell != "bash" {
				continue
			}
			if bin, ok := hookBinary(h.Command, provider); ok && h.Type == "command" && runnable(bin) {
				return nil
			}
		}
	}
	return fmt.Errorf("%s has no PreToolUse hook for every tool that runs an existing pitwall with hook %s", path, provider)
}

func guardGenerated(provider model.Provider, dir string) error {
	path, generate := filepath.Join(dir, "extensions", "pitwall.ts"), PiExtension
	if provider == model.ProviderOmp {
		generate = OmpExtension
	} else if provider == model.ProviderOpenCode {
		path, generate = filepath.Join(dir, "plugins", "pitwall.js"), OpenCodePlugin
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s guard: %w", provider, err)
	}
	_, rest, found := strings.Cut(string(b), "const bin = ")
	literal, _, terminated := strings.Cut(rest, ";\n")
	var bin string
	if !found || !terminated || json.Unmarshal([]byte(literal), &bin) != nil || !filepath.IsAbs(bin) || !runnable(bin) {
		return fmt.Errorf("%s must name an existing pitwall executable for the guard", path)
	}
	if bytes.Equal(b, generate(bin)) || provider == model.ProviderOmp && bytes.Equal(b, PiExtension(bin)) {
		return nil
	}
	return fmt.Errorf("%s is not the current pitwall guard extension or plugin; install the current unedited copy", path)
}

// GuardOpenCode checks the executable version separately from its plugin:
// OpenCode 2's shared background service cannot attribute guard calls to
// a pane. Unknown versions fail closed. env is added to this process's.
func GuardOpenCode(program, cwd string, env []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, "--version")
	cmd.Dir, cmd.Env, cmd.WaitDelay = cwd, append(os.Environ(), env...), time.Second
	b, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("cannot check OpenCode's guard support with --version: %w", err)
	}
	if openCodeGuardedVersion(string(b)) {
		return nil
	}
	return fmt.Errorf("the guard requires OpenCode 1.4 or later in series 1; OpenCode 2's shared service cannot guard a pane, and unknown versions are refused")
}

func openCodeGuardedVersion(version string) bool {
	core, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(version), "v"), "-")
	core, _, _ = strings.Cut(core, "+")
	parts := strings.Split(core, ".")
	if len(parts) != 3 || parts[0] != "1" {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	patch, patchErr := strconv.Atoi(parts[2])
	return err == nil && patchErr == nil && minor >= 4 && patch >= 0
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
	return bin, filepath.IsAbs(bin) && command == commandPath(runtime.GOOS, bin)+" hook "+string(provider)
}

// runnable reports whether path is a file the agent can run.
func runnable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && (runtime.GOOS == "windows" || fi.Mode()&0o111 != 0)
}
