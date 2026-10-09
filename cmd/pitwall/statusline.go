package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/nowindow"
)

// runStatusline is Claude Code's statusLine command once `pitwall hooks
// install --statusline` set it: it saves the plan limits in Claude Code's
// JSON for the Usage page and the sidebar, then runs the user's own
// statusline command, args[0], on the same JSON, and passes its output
// and exit code through untouched. Saving never fails the status line.
func runStatusline(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 1 {
		fmt.Fprintln(stderr, "usage: pitwall statusline [command]")
		return 2
	}
	in, _ := io.ReadAll(io.LimitReader(stdin, 4<<20))
	if l, ok := flow.Statusline(in, time.Now()); ok {
		if data, err := json.Marshal(l); err == nil {
			_ = saveState(filepath.Join(stateDir(), flow.ClaudeLimitsFile), data)
		}
	}
	if len(args) == 0 {
		return 0
	}
	cmd := exec.Command("sh", "-c", args[0])
	nowindow.Set(cmd)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(in), stdout, stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		fmt.Fprintln(stderr, "pitwall statusline:", err)
		return 1
	}
	return 0
}

// saveState writes data to path in pitwall's state directory through a
// rename, so a reader never sees half of it.
func saveState(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".limits-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// mergeStatusline returns settings.json with its statusLine running
// through `pitwall statusline`, which keeps any command it had and the
// rest of the object; uninstall puts that command back, or drops the
// statusLine pitwall added.
func mergeStatusline(original []byte, bin string, install bool) ([]byte, []string, error) {
	root := hookObject{}
	if len(original) > 0 {
		var err error
		if root, err = hookJSON[hookObject](original); err != nil || root == nil {
			return nil, nil, errors.New("config must be a JSON object")
		}
	}
	line := hookObject{}
	if raw, ok := root["statusLine"]; ok {
		var err error
		if line, err = hookJSON[hookObject](raw); err != nil || line == nil {
			return nil, nil, errors.New("statusLine must be a JSON object")
		}
	}
	command, _ := hookJSON[string](line["command"])
	wrapped, ours := agent.StatuslineWrapped(bin, command)
	var change string
	switch {
	case install && ours, !install && !ours:
		return original, nil, nil
	case install:
		wrapped = command
		line["type"], _ = json.Marshal("command")
		line["command"], _ = json.Marshal(agent.ClaudeStatusline(bin, wrapped))
		root["statusLine"], _ = json.Marshal(line)
		change = "added statusLine: " + agent.ClaudeStatusline(bin, "")
		if wrapped != "" {
			change = "wrapped statusLine: " + wrapped
		}
	case wrapped != "":
		line["command"], _ = json.Marshal(wrapped)
		root["statusLine"], _ = json.Marshal(line)
		change = "restored statusLine: " + wrapped
	default:
		delete(root, "statusLine")
		change = "removed statusLine: " + command
	}
	data, err := json.MarshalIndent(root, "", "  ")
	return append(data, '\n'), []string{change}, err
}
