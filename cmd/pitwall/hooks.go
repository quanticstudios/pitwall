package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	return filepath.EvalSymlinks(bin)
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
		path                      string
		data, original, generated []byte
		mode                      os.FileMode
		changes                   []string
	}
	files := []config{{path: filepath.Join(home, ".claude", "settings.json")}, {path: filepath.Join(home, ".codex", "hooks.json")}}
	for i := range files {
		f := &files[i]
		f.original, err = os.ReadFile(f.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		f.generated = agent.ClaudeHooks(bin)
		if i == 1 {
			f.generated = agent.CodexHooks(bin)
		}
		f.data, f.changes, err = mergeHooks(f.original, f.generated, install)
		if err != nil {
			return fmt.Errorf("%s: %w", f.path, err)
		}
	}
	for _, f := range files {
		if dry {
			fmt.Fprintf(out, "# %s\n%s\n", f.path, f.data)
			continue
		}
		hookBeforeWrite(f.path)
		fresh, err := os.ReadFile(f.path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("re-read %s: %w", f.path, err)
		}
		if !bytes.Equal(fresh, f.original) || (fresh == nil) != (f.original == nil) {
			f.data, f.changes, err = mergeHooks(fresh, f.generated, install)
			if err != nil {
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
		if len(f.changes) == 0 {
			fmt.Fprintf(out, "%s: unchanged\n", f.path)
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
	names := make([]string, 0, len(events)+len(wanted))
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
