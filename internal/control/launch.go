package control

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/guard"
	"github.com/quanticstudios/pitwall/internal/model"
)

// Launch is the command and environment of a tab that pitwall new,
// worktree new, queue add or the JSON newTab opens: preset's agent and
// args before rest, else rest; preset's variables, and guard.Env when
// guarded or the preset asks for the guard. s holds the presets.
func Launch(s config.Settings, preset string, guarded bool, rest []string) (cmd, vars []string, err error) {
	cmd = rest
	if preset != "" {
		p, ok := s.FindPreset(preset)
		if !ok {
			return nil, nil, fmt.Errorf("no preset %q in %s", preset, config.Path())
		}
		cmd = append(append([]string{p.Agent}, p.Args...), rest...)
		vars, guarded = slices.Clone(p.Env), guarded || p.Guard
	}
	if guarded {
		if len(cmd) == 0 {
			return nil, nil, errors.New("--guard needs an agent to run: --guard -- claude")
		}
		vars = append(vars, guard.Env+"=1")
	}
	return cmd, vars, nil
}

// WithModel applies a model override after resolving a preset.
func WithModel(cmd []string, selection string) ([]string, error) {
	if strings.TrimSpace(selection) == "" {
		return cmd, nil
	}
	if len(cmd) == 0 {
		return nil, errors.New("--model needs an agent to run: --model opus -- claude")
	}
	return append([]string{cmd[0]}, config.WithModel(cmd[0], cmd[1:], selection)...), nil
}

// WorkerGuard reports whether [agents] guard_workers guards the tab c's
// pane opens to run cmd or preset: an agent's worker. A tab opened from
// outside a pane, or running a shell, is no worker.
func WorkerGuard(s config.Settings, c Caller, cmd []string, preset string) bool {
	return s.GuardWorkers && c.Pane != "" && (len(cmd) > 0 || preset != "")
}

// Guarded reports whether tab runs the guard: one of its panes was
// started with guard.Env on.
func Guarded(state model.State, tab string) bool {
	return slices.ContainsFunc(state.Panes, func(p model.Pane) bool {
		return p.WorkspaceID == tab && slices.ContainsFunc(p.Env, guard.EnvOn)
	})
}
