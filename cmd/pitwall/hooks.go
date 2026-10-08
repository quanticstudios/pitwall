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
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
)

var hookExecutable = func() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	bin, err = filepath.EvalSymlinks(bin)
	return homebrewOpt(bin), err
}

// homebrewOpt maps a Homebrew keg's binary, which brew upgrade deletes, to
// its opt link, which brew points at each new version.
func homebrewOpt(bin string) string {
	prefix, rest, ok := strings.Cut(bin, "/Cellar/pitwall/")
	if !ok {
		return bin
	}
	if _, rest, ok = strings.Cut(rest, "/"); !ok {
		return bin
	}
	return prefix + "/opt/pitwall/" + rest
}

var hookBeforeWrite = func(path string) {}

func runHooks(args []string, out io.Writer) error {
	if len(args) == 0 {
		return printHooks()
	}
	if (args[0] != "install" && args[0] != "uninstall") || len(args) > 2 || (len(args) == 2 && args[1] != "--dry-run") {
		return errors.New("usage: pitwall hooks [install|uninstall] [--dry-run]")
	}
	install, dry := args[0] == "install", len(args) == 2
	bin, err := hookExecutable()
	if err != nil {
		return err
	}
	if !filepath.IsAbs(bin) {
		return errors.New("hook executable path must be absolute")
	}
	if install {
		cache, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		for _, dir := range []string{"/tmp", os.TempDir(), filepath.Join(cache, "go-build"), os.Getenv("GOCACHE")} {
			if dir != "" && (bin == dir || strings.HasPrefix(bin, filepath.Clean(dir)+string(os.PathSeparator))) {
				return fmt.Errorf("refusing to install hooks for temporary executable %s; use scripts/install.sh first", bin)
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(stateDir(), "hooks.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := waitLock(lock); err != nil {
		return err
	}
	type config struct {
		path           string
		data, original []byte // data nil: remove the file
		merge          func(original []byte) ([]byte, []string, error)
		mode           os.FileMode
		changes        []string
		skip           string // why the file is left alone, as a warning
	}
	merge := func(f *config, original []byte) error {
		var err error
		f.data, f.changes, err = f.merge(original)
		if s := (skipError{}); errors.As(err, &s) {
			f.data, f.changes, f.skip, err = original, nil, s.why, nil
		}
		return err
	}
	jsonHooks := func(generated []byte) func([]byte) ([]byte, []string, error) {
		return func(original []byte) ([]byte, []string, error) { return mergeHooks(original, generated, install) }
	}
	files := []config{
		{path: filepath.Join(home, ".claude", "settings.json"), merge: jsonHooks(agent.ClaudeHooks(bin))},
		{path: filepath.Join(home, ".codex", "hooks.json"), merge: jsonHooks(agent.CodexHooks(bin))},
	}
	if dir := piAgentDir(home); dir != "" {
		files = append(files, config{path: filepath.Join(dir, "extensions", "pitwall.ts"), merge: func(original []byte) ([]byte, []string, error) {
			return mergeGenerated(original, agent.PiExtension(bin), install, agent.IsPiExtension, "pi extension")
		}})
	}
	if dir := agent.GeminiDir(home); installed("gemini", dir) {
		files = append(files, config{path: filepath.Join(dir, "settings.json"), merge: func(original []byte) ([]byte, []string, error) {
			data, changes, err := mergeHooks(original, agent.GeminiHooks(bin), install)
			if err != nil {
				// why: Gemini reads settings.json with comments, which this merge cannot keep; the other agents still install.
				return nil, nil, skipError{err.Error() + "; add the block `pitwall hooks` prints by hand"}
			}
			return data, changes, nil
		}})
	}
	if dir := agent.OpenCodeDir(home); installed("opencode", dir) {
		files = append(files, config{path: filepath.Join(dir, "plugins", "pitwall.js"), merge: func(original []byte) ([]byte, []string, error) {
			return mergeGenerated(original, agent.OpenCodePlugin(bin), install, agent.IsOpenCodePlugin, "OpenCode plugin")
		}})
	}
	for i := range files {
		f := &files[i]
		f.original, err = os.ReadFile(f.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := merge(f, f.original); err != nil {
			return fmt.Errorf("%s: %w", f.path, err)
		}
	}
	for _, f := range files {
		if dry {
			// The changes as comment lines, then the file they make.
			switch {
			case f.skip != "":
				fmt.Fprintf(out, "# %s: skipped: %s\n", f.path, f.skip)
			case len(f.changes) == 0:
				fmt.Fprintf(out, "# %s: unchanged\n", f.path)
			default:
				for _, change := range f.changes {
					fmt.Fprintf(out, "# %s: %s\n", f.path, change)
				}
				if f.data == nil {
					fmt.Fprintln(out, "(no file)")
				} else {
					fmt.Fprintf(out, "%s\n", f.data)
				}
			}
			continue
		}
		hookBeforeWrite(f.path)
		fresh, err := os.ReadFile(f.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("re-read %s: %w", f.path, err)
		}
		if !bytes.Equal(fresh, f.original) || (fresh == nil) != (f.original == nil) {
			if err := merge(&f, fresh); err != nil {
				return fmt.Errorf("%s changed during hook update; refusing to overwrite: %w", f.path, err)
			}
			f.original = fresh
		}
		f.mode = 0o600
		if fresh != nil {
			info, err := os.Stat(f.path)
			if err != nil {
				return err
			}
			f.mode = info.Mode().Perm()
		}
		if f.skip != "" {
			fmt.Fprintf(out, "%s: skipped: %s\n", f.path, f.skip)
			continue
		}
		if len(f.changes) == 0 {
			fmt.Fprintf(out, "%s: unchanged\n", f.path)
			continue
		}
		if f.data == nil {
			if err := os.Remove(f.path); err != nil {
				return err
			}
			for _, change := range f.changes {
				fmt.Fprintf(out, "%s: %s\n", f.path, change)
			}
			continue
		}
		backup, err := writeHookConfig(f.path, f.data, f.original, f.mode)
		if err != nil {
			return err
		}
		if backup != "" {
			fmt.Fprintf(out, "Backup: %s\n", backup)
		}
		for _, change := range f.changes {
			fmt.Fprintf(out, "%s: %s\n", f.path, change)
		}
	}
	if install {
		fmt.Fprintln(out, "Codex needs /hooks once to trust the hooks.")
	}
	return nil
}

// installHooks is `pitwall hooks install` for the window's Install
// button. A dry run returns only what would change, one line per change,
// without the files.
func installHooks(dry bool) (string, error) {
	var b strings.Builder
	args := []string{"install"}
	if dry {
		args = append(args, "--dry-run")
	}
	err := runHooks(args, &b)
	if !dry {
		return b.String(), err
	}
	var lines []string
	for line := range strings.Lines(b.String()) {
		if s, ok := strings.CutPrefix(line, "# "); ok {
			lines = append(lines, s)
		}
	}
	return strings.Join(lines, ""), err
}

// piAgentDir is pi's agent directory, $PI_CODING_AGENT_DIR or ~/.pi/agent,
// or "" when pi is neither on PATH nor configured, so hooks skip it.
func piAgentDir(home string) string {
	dir := os.Getenv("PI_CODING_AGENT_DIR")
	switch {
	case dir == "~":
		dir = home
	case strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(home, dir[2:])
	case dir == "":
		dir = filepath.Join(home, ".pi", "agent")
	}
	if !installed("pi", dir) {
		return ""
	}
	return dir
}

// installed reports whether an agent is on PATH or its config dir exists;
// hooks skip one that is neither.
func installed(name, dir string) bool {
	if _, err := exec.LookPath(name); err == nil {
		return true
	}
	_, err := os.Stat(dir)
	return err == nil
}

// mergeGenerated returns the next contents of a file pitwall generates
// whole, such as pi's extension, nil to remove it. Install replaces only a
// file some pitwall binary wrote and nobody edited since (is reports one);
// uninstall removes only this binary's file.
func mergeGenerated(original, generated []byte, install bool, is func([]byte) bool, name string) ([]byte, []string, error) {
	switch {
	case original != nil && bytes.Equal(original, generated):
		if install {
			return original, nil, nil
		}
		return nil, []string{"removed " + name}, nil
	case !install:
		return original, nil, nil
	case original != nil && !is(original):
		return nil, nil, skipError{"edited since pitwall wrote it; move it away to reinstall"}
	}
	return generated, []string{"wrote " + name}, nil
}

// skipError is a merge result that leaves the file alone and warns, instead
// of stopping the whole install.
type skipError struct{ why string }

func (e skipError) Error() string { return e.why }

// Raw messages keep unrelated values, including large JSON numbers, intact.
type hookObject map[string]json.RawMessage

func hookJSON[T any](data []byte) (T, error) {
	var v T
	err := json.Unmarshal(data, &v)
	return v, err
}

func mergeHooks(original, generated []byte, install bool) ([]byte, []string, error) {
	root := hookObject{}
	if original != nil {
		var err error
		root, err = hookJSON[hookObject](original)
		if err != nil || root == nil {
			return nil, nil, errors.New("config must be a JSON object")
		}
	}
	events := hookObject{}
	if raw, ok := root["hooks"]; ok {
		var err error
		events, err = hookJSON[hookObject](raw)
		if err != nil || events == nil {
			return nil, nil, errors.New("hooks must be a JSON object")
		}
	}
	wanted, _ := hookJSON[hookObject](generated)
	commands := map[string]bool{}
	for _, raw := range wanted {
		groups, _ := hookJSON[[]hookObject](raw)
		for _, group := range groups {
			handlers, _ := hookJSON[[]hookObject](group["hooks"])
			for _, handler := range handlers {
				command, _ := hookJSON[string](handler["command"])
				if command != "" {
					commands[command] = true
				}
			}
		}
	}
	var names []string
	for name := range events {
		names = append(names, name)
	}
	if install {
		for name := range wanted {
			if _, ok := events[name]; !ok {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	var changes []string
	for _, name := range names {
		groups := []hookObject{}
		if raw, ok := events[name]; ok {
			var err error
			groups, err = hookJSON[[]hookObject](raw)
			if err != nil || groups == nil {
				return nil, nil, fmt.Errorf("hooks.%s must be an array of matcher groups", name)
			}
		}
		var command string
		if raw, ok := wanted[name]; install && ok {
			wg, _ := hookJSON[[]hookObject](raw)
			wh, _ := hookJSON[[]hookObject](wg[0]["hooks"])
			command, _ = hookJSON[string](wh[0]["command"])
		}
		found, removed := false, false
		kept := make([]hookObject, 0, len(groups))
		for _, group := range groups {
			handlers, err := hookJSON[[]hookObject](group["hooks"])
			if err != nil || handlers == nil || group == nil {
				return nil, nil, fmt.Errorf("hooks.%s matcher group must contain a hooks array", name)
			}
			remaining := make([]hookObject, 0, len(handlers))
			groupRemoved := false
			for _, handler := range handlers {
				cmd, _ := hookJSON[string](handler["command"])
				if command != "" && cmd == command {
					found = true
				}
				if !install && commands[cmd] {
					groupRemoved, removed = true, true
					changes = append(changes, "removed "+name+": "+cmd)
					continue
				}
				remaining = append(remaining, handler)
			}
			if groupRemoved {
				if len(remaining) == 0 {
					continue
				}
				group["hooks"], _ = json.Marshal(remaining)
			}
			kept = append(kept, group)
		}
		if install && command != "" && !found {
			wg, _ := hookJSON[[]hookObject](wanted[name])
			kept = append(kept, wg[0])
			changes = append(changes, "added "+name+": "+command)
		} else if !removed {
			continue
		}
		if removed && len(kept) == 0 {
			delete(events, name)
		} else {
			events[name], _ = json.Marshal(kept)
		}
	}
	if len(changes) > 0 {
		if len(events) == 0 {
			delete(root, "hooks")
		} else {
			root["hooks"], _ = json.Marshal(events)
		}
	}
	data, err := json.MarshalIndent(root, "", "  ")
	return append(data, '\n'), changes, err
}

func writeHookConfig(path string, data, original []byte, mode os.FileMode) (string, error) {
	info, err := os.Lstat(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", fmt.Errorf("resolve hook config symlink: %w", err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	backup := ""
	if original != nil {
		for stamp := time.Now().Unix(); ; stamp++ {
			backup = fmt.Sprintf("%s.pitwall-backup-%d", path, stamp)
			f, err := os.OpenFile(backup, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				return "", err
			}
			_, err = io.Copy(f, bytes.NewReader(original))
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err != nil {
				return "", err
			}
			if closeErr != nil {
				return "", closeErr
			}
			break
		}
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pitwall-hooks-*")
	if err != nil {
		return backup, err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return backup, err
	}
	if _, err := f.Write(data); err != nil {
		return backup, err
	}
	if err := f.Sync(); err != nil {
		return backup, err
	}
	if err := f.Close(); err != nil {
		return backup, err
	}
	return backup, os.Rename(f.Name(), path)
}
