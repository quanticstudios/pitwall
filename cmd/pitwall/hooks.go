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

// sibling is the other flavor of pitwall in bin's folder: pitwall-core
// next to pitwall and the reverse, "" when bin has neither name.
func sibling(bin string) string {
	switch filepath.Base(bin) {
	case "pitwall":
		return filepath.Join(filepath.Dir(bin), "pitwall-core")
	case "pitwall-core":
		return filepath.Join(filepath.Dir(bin), "pitwall")
	}
	return ""
}

// owns reports whether a hook's binary is this install, whose hooks name
// self: it resolves to self, as a pitwall link to pitwall-core does, or to
// self's sibling, the other flavor in the same folder.
func owns(self, bin string) bool {
	r, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return false
	}
	if s, err := filepath.EvalSymlinks(self); err == nil {
		self = s
	}
	return r == self || r == sibling(self)
}

// ownedBins are the binaries other than self that original, an agent
// config, names in a hook command or a generated file's `const bin` and
// that are this install (owns).
func ownedBins(self string, original []byte) []string {
	var found []string
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if s, ok := x.(string); ok && k == "command" {
					found = append(found, commandBin(s))
				}
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	var root any
	if json.Unmarshal(original, &root) == nil {
		walk(root)
	}
	if _, rest, ok := strings.Cut(string(original), "const bin = "); ok {
		literal, _, _ := strings.Cut(rest, ";\n")
		var bin string
		if json.Unmarshal([]byte(literal), &bin) == nil {
			found = append(found, bin)
		}
	}
	var out []string
	for _, bin := range found {
		if bin != self && filepath.IsAbs(bin) && !slices.Contains(out, bin) && owns(self, bin) {
			out = append(out, bin)
		}
	}
	return out
}

// others are what gen writes for each binary ownedBins finds in original.
func others(self string, original []byte, gen func(bin string) []byte) [][]byte {
	var out [][]byte
	for _, bin := range ownedBins(self, original) {
		out = append(out, gen(bin))
	}
	return out
}

// commandBin is the binary a hook command runs: its first word, unquoted
// as agent's commandPath quotes it.
func commandBin(command string) string {
	if rest, ok := strings.CutPrefix(command, "'"); ok {
		var b strings.Builder
		for {
			part, after, found := strings.Cut(rest, "'")
			if !found {
				return ""
			}
			b.WriteString(part)
			if rest, ok = strings.CutPrefix(after, `\''`); !ok {
				return b.String()
			}
			b.WriteString("'")
		}
	}
	if rest, ok := strings.CutPrefix(command, `"`); ok {
		bin, _, _ := strings.Cut(rest, `"`)
		return bin
	}
	bin, _, _ := strings.Cut(command, " ")
	return bin
}

var hookBeforeWrite = func(path string) {}

func runHooks(args []string, out io.Writer) error { return hooks(args, out, nil) }

// hookPlan is what a dry run would do to one file: skip it and why, or its
// changes, none when it is unchanged.
type hookPlan struct {
	path, skip string
	changes    []string
}

// hooks is runHooks; a dry run with plans set appends one per file there
// instead of printing it.
func hooks(args []string, out io.Writer, plans *[]hookPlan) error {
	if len(args) == 0 {
		return printHooks()
	}
	install, dry, statusline := args[0] == "install", false, false
	for _, a := range args[1:] {
		switch {
		case a == "--dry-run" && !dry:
			dry = true
		case a == "--statusline" && install && !statusline:
			statusline = true
		default:
			args = nil
		}
	}
	if args == nil || !install && args[0] != "uninstall" {
		return errors.New("usage: pitwall hooks [install [--statusline]|uninstall] [--dry-run]")
	}
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
		ownDir         bool   // its directory is pitwall's too: removed with it when empty
	}
	merge := func(f *config, original []byte) error {
		var err error
		f.data, f.changes, err = f.merge(original)
		if s := (skipError{}); errors.As(err, &s) {
			f.data, f.changes, f.skip, err = original, nil, s.why, nil
		}
		return err
	}
	jsonHooks := func(gen func(string) []byte) func([]byte) ([]byte, []string, error) {
		return func(original []byte) ([]byte, []string, error) {
			return mergeHooks(original, gen(bin), install, others(bin, original, gen)...)
		}
	}
	// skill is pitwall's skill in an agent's home dir.
	skill := func(dir string) config {
		return config{path: filepath.Join(dir, "skills", "pitwall", "SKILL.md"), ownDir: true, merge: func(original []byte) ([]byte, []string, error) {
			return mergeGenerated(original, agent.Skill(), install, agent.IsSkill, "pitwall skill")
		}}
	}
	files := []config{
		{path: filepath.Join(agent.ClaudeDir(home), "settings.json"), merge: func(original []byte) ([]byte, []string, error) {
			data, changes, err := jsonHooks(agent.ClaudeHooks)(original)
			if err != nil || install && !statusline {
				return data, changes, err
			}
			data, more, err := mergeStatusline(data, bin, install, ownedBins(bin, original)...)
			return data, append(changes, more...), err
		}},
		skill(agent.ClaudeDir(home)),
	}
	// why: ~/.codex gets hooks.json above whatever is installed, so a skill
	// there only when the dir existed would arrive on the second install.
	if dir := agent.CodexDir(home); dir == filepath.Join(home, ".codex") || installed("codex", dir) {
		files = append(files, config{path: filepath.Join(dir, "hooks.json"), merge: jsonHooks(agent.CodexHooks)}, skill(dir))
	}
	if dir := piAgentDir(home); dir != "" {
		files = append(files, config{path: filepath.Join(dir, "extensions", "pitwall.ts"), merge: func(original []byte) ([]byte, []string, error) {
			return mergeGenerated(original, agent.PiExtension(bin), install, agent.IsPiExtension, "pi extension", others(bin, original, agent.PiExtension)...)
		}})
	}
	// why: omp reads $PI_CODING_AGENT_DIR too; when it is set, omp loads pi's extension, which reports as omp there.
	if dir := agent.OmpDir(home); dir != agent.PiDir(home) && installed("omp", dir) {
		files = append(files, config{path: filepath.Join(dir, "extensions", "pitwall.ts"), merge: func(original []byte) ([]byte, []string, error) {
			return mergeGenerated(original, agent.OmpExtension(bin), install, agent.IsOmpExtension, "omp extension", others(bin, original, agent.OmpExtension)...)
		}})
	}
	if dir := agent.GeminiDir(home); installed("gemini", dir) {
		files = append(files, config{path: filepath.Join(dir, "settings.json"), merge: func(original []byte) ([]byte, []string, error) {
			data, changes, err := mergeHooks(original, agent.GeminiHooks(bin), install, others(bin, original, agent.GeminiHooks)...)
			if err != nil {
				// why: Gemini reads settings.json with comments, which this merge cannot keep; the other agents still install.
				return nil, nil, skipError{err.Error() + "; add the block `pitwall hooks` prints by hand"}
			}
			return data, changes, nil
		}})
	}
	if dir := agent.CursorDir(home); installed("cursor-agent", dir) {
		files = append(files, config{path: filepath.Join(dir, "hooks.json"), merge: func(original []byte) ([]byte, []string, error) {
			return mergeCursorHooks(original, agent.CursorHooks(bin), install, others(bin, original, agent.CursorHooks)...)
		}})
	}
	if dir := agent.OpenCodeDir(home); installed("opencode", dir) {
		plugin := func(original []byte) ([]byte, []string, error) {
			return mergeGenerated(original, agent.OpenCodePlugin(bin), install, agent.IsOpenCodePlugin, "OpenCode plugin", others(bin, original, agent.OpenCodePlugin)...)
		}
		// why: OpenCode 2 runs plugins/pitwall.js in a background service every TUI shares, and loads the TUI half only from a directory.
		files = append(files, config{path: filepath.Join(dir, "plugins", "pitwall.js"), merge: plugin},
			config{path: filepath.Join(dir, "plugins", "pitwall", "tui.js"), ownDir: true, merge: plugin})
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
		if dry && plans != nil {
			*plans = append(*plans, hookPlan{f.path, f.skip, f.changes})
			continue
		}
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
			if f.ownDir {
				os.Remove(filepath.Dir(f.path)) // fails, harmlessly, when the user put more there
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
// without the files. statusline adds pitwall statusline to Claude Code's.
func installHooks(dry, statusline bool) (string, error) {
	args := []string{"install"}
	if statusline {
		args = append(args, "--statusline")
	}
	var b strings.Builder
	if !dry {
		err := runHooks(args, &b)
		return b.String(), err
	}
	plans, err := planHooks(args)
	for _, p := range plans {
		switch {
		case p.skip != "":
			fmt.Fprintf(&b, "%s: skipped: %s\n", p.path, p.skip)
		case len(p.changes) == 0:
			fmt.Fprintf(&b, "%s: unchanged\n", p.path)
		}
		for _, change := range p.changes {
			fmt.Fprintf(&b, "%s: %s\n", p.path, change)
		}
	}
	return b.String(), err
}

// planHooks is a dry run of `pitwall hooks <args...>`, one plan per file.
func planHooks(args []string) ([]hookPlan, error) {
	var plans []hookPlan
	err := hooks(append(args, "--dry-run"), io.Discard, &plans)
	return plans, err
}

// piAgentDir is agent.PiDir, or "" when pi is neither on PATH nor
// configured, so hooks skip it.
func piAgentDir(home string) string {
	dir := agent.PiDir(home)
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
// uninstall removes only this install's file: generated, or one of also,
// what another path to this install wrote.
func mergeGenerated(original, generated []byte, install bool, is func([]byte) bool, name string, also ...[]byte) ([]byte, []string, error) {
	ours := original != nil && slices.ContainsFunc(append([][]byte{generated}, also...), func(g []byte) bool { return bytes.Equal(original, g) })
	switch {
	case ours:
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

// mergeHooks returns original with generated's hooks added (install) or
// removed. A hook also has, as another path to this install wrote it,
// counts as generated's.
func mergeHooks(original, generated []byte, install bool, also ...[]byte) ([]byte, []string, error) {
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
	commands := map[string]bool{}          // every command of this install, for uninstall
	accept := map[string]map[string]bool{} // by event, the commands that count as installed
	for _, g := range append([][]byte{generated}, also...) {
		events, _ := hookJSON[hookObject](g)
		for name, raw := range events {
			groups, _ := hookJSON[[]hookObject](raw)
			for _, group := range groups {
				handlers, _ := hookJSON[[]hookObject](group["hooks"])
				for _, handler := range handlers {
					command, _ := hookJSON[string](handler["command"])
					if command != "" {
						commands[command] = true
						if accept[name] == nil {
							accept[name] = map[string]bool{}
						}
						accept[name][command] = true
					}
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
				if command != "" && accept[name][cmd] {
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

// mergeCursorHooks is mergeHooks for Cursor's hooks.json: {"version": 1,
// "hooks": {event: [{command, ...}]}}, a list of commands per event rather
// than matcher groups. Install adds each missing command and sets version
// 1 when unset; uninstall removes exactly the commands generated and also
// hold.
func mergeCursorHooks(original, generated []byte, install bool, also ...[]byte) ([]byte, []string, error) {
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
	wanted, _ := hookJSON[map[string][]hookObject](generated)
	accept := map[string]map[string]bool{} // by event, this install's commands
	for _, g := range append([][]byte{generated}, also...) {
		events, _ := hookJSON[map[string][]hookObject](g)
		for name, hooks := range events {
			for _, h := range hooks {
				if command, _ := hookJSON[string](h["command"]); command != "" {
					if accept[name] == nil {
						accept[name] = map[string]bool{}
					}
					accept[name][command] = true
				}
			}
		}
	}
	names := make([]string, 0, len(wanted))
	for name := range wanted {
		names = append(names, name)
	}
	slices.Sort(names)
	var changes []string
	for _, name := range names {
		ours := wanted[name][0]
		command, _ := hookJSON[string](ours["command"])
		handlers := []hookObject{}
		if raw, ok := events[name]; ok {
			var err error
			handlers, err = hookJSON[[]hookObject](raw)
			if err != nil || handlers == nil {
				return nil, nil, fmt.Errorf("hooks.%s must be an array of hooks", name)
			}
		}
		kept := make([]hookObject, 0, len(handlers))
		found := false
		for _, h := range handlers {
			if cmd, _ := hookJSON[string](h["command"]); accept[name][cmd] && h != nil {
				found = true
				if !install {
					changes = append(changes, "removed "+name+": "+cmd)
					continue
				}
			}
			kept = append(kept, h)
		}
		switch {
		case install && !found:
			kept = append(kept, ours)
			changes = append(changes, "added "+name+": "+command)
		case install || !found:
			continue
		}
		if len(kept) == 0 {
			delete(events, name)
		} else {
			events[name], _ = json.Marshal(kept)
		}
	}
	if len(changes) > 0 {
		if _, ok := root["version"]; !ok && install {
			root["version"] = json.RawMessage("1")
		}
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
