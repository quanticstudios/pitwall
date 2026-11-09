package gitstat

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/gitenv"
	"github.com/quanticstudios/pitwall/internal/nowindow"
)

// Adapted from tuios (MIT): internal/childproc/git.go

// pitwall runs git by itself in folders an agent can write to, and a
// repository's config can name programs that git runs: fsmonitor, hooks,
// filter and diff drivers, signing and ssh programs, credential helpers. An
// agent that writes .git/config would run them as the daemon, with no
// approval. Command is the one place that decides which of them run.
//
// Each git gets three kinds of setting:
//
//   - Fixed settings that turn the repository's own programs off. See
//     fixedSettings.
//   - For each config key that names a program and is set in the repository's
//     own config (local or worktree scope, includes counted, also ones whose
//     condition does not hold yet), the value the person's system, global or
//     command-line config gives it, or a value that runs nothing. A filter
//     git-lfs installed in ~/.gitconfig keeps working.
//     A filter set only in .git/config does not run, so its files are read as
//     they are on disk. See repoFilter for what must not do that.
//   - On diff and status, flags config cannot override. See guardArgs.
//
// The settings go in as GIT_CONFIG_COUNT entries, which git reads as
// command-line config. A key there needs no quoting, so a driver name that
// holds "=" cannot split it, and the git a submodule check starts inherits
// them.

// Command returns git with args, to run in dir, with the settings above. The
// caller may add to cmd.Env, but must not replace it. When the repository's
// config cannot be read, the command fails to start with that error, and git
// does not run.
func Command(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, guardArgs(args)...)...)
	nowindow.Set(cmd)
	env, err := Env(ctx, dir, nil)
	cmd.Env = env
	if err != nil {
		cmd.Err = err
	}
	return cmd
}

// Env is env (os.Environ when nil) with the settings above for a git that
// runs in dir, for a program pitwall starts that runs git itself, such as gh.
// It drops the variables that would point git at another repository or index,
// and sets git to never prompt, page or open an editor.
//
// why: the config is read just before each git and never cached, which keeps
// this window small but cannot close it. A key added between this read and git's
// own runs: overrides name their keys, so none can cover a driver not seen yet,
// and no second read makes the two atomic. This reduces the risk; it is not a
// sandbox boundary.
func Env(ctx context.Context, dir string, env []string) ([]string, error) {
	if env == nil {
		env = os.Environ()
	}
	out := gitenv.Clean(env)
	n := 0
	for _, kv := range out {
		name, value, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_CONFIG_COUNT":
			if c, err := strconv.Atoi(value); err == nil && c > 0 {
				n = c
			}
		}
	}
	out = append(out, "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "GIT_PAGER=cat")
	config, err := readConfig(ctx, dir, out)
	if err != nil {
		return out, err
	}
	conditional, err := conditionalConfig(ctx, dir, out, config)
	if err != nil {
		return out, err
	}
	config = append(config, conditional...)
	return appendConfig(out, n, append(overridesFrom(config), fixedSettings()...)), nil
}

// repoFilter is the key of a filter a config file of the repository at dir sets
// and the person's config does not, "" for none. pitwall does not run it, so a command
// that writes the index or the work tree would store or write that file's raw
// bytes: a plain secret where git-crypt keeps it encrypted. Those commands
// refuse instead, through refuseRepoFilter.
func repoFilter(ctx context.Context, dir string) (string, error) {
	entries, err := readConfig(ctx, dir, os.Environ())
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		rest, isFilter := strings.CutPrefix(e.key, "filter.")
		if e.trusted || e.value == "" || !isFilter {
			continue
		}
		_, last, named := splitKey(rest)
		if !named || last != "clean" && last != "smudge" && last != "process" {
			continue
		}
		if !slices.ContainsFunc(entries, func(t configEntry) bool { return t.trusted && t.key == e.key }) {
			return e.key, nil
		}
	}
	return "", nil
}

// notRepo is why dir has no repository root: its config cannot be read, which
// the error names, or else it is not in a repository.
func notRepo(ctx context.Context, dir string) error {
	if _, err := Env(ctx, dir, nil); err != nil {
		return err
	}
	return fmt.Errorf("%s is not in a git repository", dir)
}

// refuseRepoFilter is the error of a command that would bypass repoFilter.
func refuseRepoFilter(ctx context.Context, dir string) error {
	key, err := repoFilter(ctx, dir)
	if err != nil {
		return err
	}
	if key != "" {
		return fmt.Errorf("this repository's own config sets %s, which pitwall does not run: do this in a terminal, or set the filter in your global git config", key)
	}
	return nil
}

// fixedSettings are the settings every git gets.
func fixedSettings() [][2]string {
	return [][2]string{
		// A repository's fsmonitor names a program git runs on status and diff.
		{"core.fsmonitor", "false"},
		// No hook runs: .git/hooks and a hooks path the repository sets are
		// both replaced. On Windows os.DevNull is NUL, which holds no file.
		// overridesFrom turns off hooks named in config, which this misses.
		{"core.hooksPath", os.DevNull},
		{"diff.external", ""},
		// The ext:: transport runs a command named in the URL.
		{"protocol.ext.allow", "never"},
		// Status and diff look inside a submodule's work tree with a git of
		// its own, which reads that repository's config, where these
		// settings cannot see its drivers. A submodule's moved commit is
		// still reported.
		{"diff.ignoreSubmodules", "dirty"},
		// Restore and fetch would start that git too.
		{"submodule.recurse", "false"},
		// Automatic gc is the way to gc.recentObjectsHook.
		{"gc.auto", "0"},
		{"maintenance.auto", "false"},
	}
}

// appendConfig adds kvs to env as GIT_CONFIG_COUNT entries after the n env
// already has.
func appendConfig(env []string, n int, kvs [][2]string) []string {
	env = slices.DeleteFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "GIT_CONFIG_COUNT=") })
	for i, kv := range kvs {
		idx := strconv.Itoa(n + i)
		env = append(env, "GIT_CONFIG_KEY_"+idx+"="+kv[0], "GIT_CONFIG_VALUE_"+idx+"="+kv[1])
	}
	return append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(n+len(kvs)))
}

// configEntry is one line of git config --list.
type configEntry struct {
	key, value string
	origin     string // the file it is from, "" for no file
	trusted    bool   // from the person's config, see trustedScope
}

// readConfig reads every config git would use in dir.
func readConfig(ctx context.Context, dir string, env []string) ([]configEntry, error) {
	out, err := gitOutput(ctx, dir, env, "config", "--show-origin", "--show-scope", "-z", "--list")
	if err != nil {
		return nil, err
	}
	return parseConfig(out)
}

// parseConfig reads git config --show-origin --show-scope -z --list. Each entry
// is the scope, NUL, the origin, NUL, the key, a newline and the value, NUL; a
// key with no value has no newline.
func parseConfig(out []byte) ([]configEntry, error) {
	fields := strings.Split(string(out), "\x00")
	if len(fields)%3 != 1 || fields[len(fields)-1] != "" {
		return nil, errors.New("git config: output is not in the expected form")
	}
	var entries []configEntry
	for i := 0; i+2 < len(fields); i += 3 {
		key, value, _ := strings.Cut(fields[i+2], "\n")
		origin := ""
		if f, ok := strings.CutPrefix(fields[i+1], "file:"); ok {
			origin = f
		}
		entries = append(entries, configEntry{key: key, value: value, origin: origin, trusted: trustedScope(fields[i])})
	}
	return entries, nil
}

// gitOutput is the stdout of git args in dir, with its stderr in the error.
//
// why: the error wraps only a git that did not start. A caller of Command reads
// exit status 1 as an answer, such as no such ref, which a failed read is not.
func gitOutput(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	nowindow.Set(cmd)
	cmd.Env = gitenv.Clean(env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %s", args[0], msg)
		} else if errors.As(err, &exit) {
			return nil, fmt.Errorf("git %s: %s", args[0], err)
		}
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return stdout.Bytes(), nil
}

// conditionalConfig is the config in the files the repository's own config
// includes under a condition, whether or not it holds in dir, and in the files
// those include in turn. Each file is read by itself, so that what it holds does
// not depend on a condition in it that holds here and not for the git guarded.
//
// why: the condition can hold for the very git being guarded and not for the
// read before it: onbranch and gitdir for the new worktree of git worktree add,
// hasconfig once a remote is added.
//
// An error here stops the command: a file not read is a program not turned off.
// Only a missing file is skipped, since git skips one too.
func conditionalConfig(ctx context.Context, dir string, env []string, entries []configEntry) ([]configEntry, error) {
	var found []configEntry
	seen := map[string]bool{}
	var walk func(list []configEntry, inFile bool) error
	walk = func(list []configEntry, inFile bool) error {
		for _, e := range list {
			if e.trusted || !includeKey(e.key, inFile) {
				continue
			}
			path, err := includedFile(ctx, dir, env, e)
			if err != nil {
				return err
			}
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				continue
			}
			out, err := gitOutput(ctx, dir, env, "config", "--file", path, "--show-origin", "--show-scope", "-z", "--list")
			if err != nil {
				return fmt.Errorf("cannot read %s, which this repository's git config includes: %w", path, err)
			}
			included, err := parseConfig(out)
			if err != nil {
				return fmt.Errorf("cannot read %s, which this repository's git config includes: %w", path, err)
			}
			// git calls a --file command scope; this file is the repository's.
			for i := range included {
				included[i].trusted = false
			}
			found = append(found, included...)
			if err := walk(included, true); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(entries, false); err != nil {
		return nil, err
	}
	return found, nil
}

// includeKey reports whether key names a file to read: one under a condition,
// or with inFile a plain include.path, which git has already followed in its own
// list but not in a file read by itself.
func includeKey(key string, inFile bool) bool {
	if inFile && key == "include.path" {
		return true
	}
	rest, ok := strings.CutPrefix(key, "includeif.")
	if !ok {
		return false
	}
	_, last, named := splitKey(rest)
	return named && last == "path"
}

// includedFile is the absolute path of the file include entry e names, as git
// finds it: ~ and %(prefix) expanded, a relative one from the file e is in. It is
// "" for an entry with no value, which names no file.
func includedFile(ctx context.Context, dir string, env []string, e configEntry) (string, error) {
	p := e.value
	if strings.HasPrefix(p, "~") || strings.HasPrefix(p, "%(") {
		out, err := gitOutput(ctx, dir, env, "-c", "pitwall.include="+p, "config", "--type=path", "pitwall.include")
		if err != nil {
			return "", fmt.Errorf("cannot find %s, which this repository's git config includes: %w", p, err)
		}
		p = strings.TrimSuffix(string(out), "\n")
	}
	p = filepath.FromSlash(p)
	if p == "" || filepath.IsAbs(p) {
		return p, nil
	}
	if e.origin == "" {
		return "", fmt.Errorf("cannot find %s, which this repository's git config includes: it is relative to no file", p)
	}
	base := filepath.FromSlash(e.origin)
	if !filepath.IsAbs(base) {
		base = filepath.Join(dir, base)
	}
	return filepath.Join(filepath.Dir(base), p), nil
}

// overridesFrom is, for each key that names a program and has a value from the
// repository's own config, the value that replaces it: the person's own, or
// one that runs nothing.
func overridesFrom(entries []configEntry) [][2]string {
	var over [][2]string
	done := map[string]bool{}
	for _, e := range entries {
		// why: core.hooksPath does not reach a hook config names. No hook
		// runs here, the person's included, as with a hooks folder's.
		if rest, isHook := strings.CutPrefix(e.key, "hook."); isHook {
			// An empty name is a name to git: hook..command runs.
			if name, last, named := splitKey(rest); named && (last == "command" || last == "event") {
				if key := "hook." + name + ".enabled"; !done[key] {
					done[key] = true
					over = append(over, [2]string{key, "false"})
				}
				continue
			}
		}
		if e.trusted || done[e.key] {
			continue
		}
		neutral, multi, ok := programKey(e.key)
		if !ok {
			continue
		}
		done[e.key] = true
		var trusted []string
		for _, t := range entries {
			if t.trusted && t.key == e.key {
				trusted = append(trusted, t.value)
			}
		}
		switch {
		case multi:
			// An empty value clears the list. The person's own come back after it.
			over = append(over, [2]string{e.key, ""})
			for _, v := range trusted {
				over = append(over, [2]string{e.key, v})
			}
		case len(trusted) > 0:
			over = append(over, [2]string{e.key, trusted[len(trusted)-1]})
		default:
			over = append(over, [2]string{e.key, neutral})
		}
	}
	return over
}

// splitKey splits what follows a key's section at its last dot: "x.clean" is
// "x", "clean" and true, ".clean" is the empty subsection git accepts, "clean"
// has none. git prints a key's last part in lower case, and a subsection as
// written.
func splitKey(rest string) (sub, last string, hasSub bool) {
	i := strings.LastIndexByte(rest, '.')
	if i < 0 {
		return "", rest, false
	}
	return rest[:i], rest[i+1:], true
}

// trustedScope reports whether config of this scope is the person's: the
// system and global files and the command line. The repository's own config,
// its worktree config and anything they include are not.
func trustedScope(scope string) bool {
	switch scope {
	case "system", "global", "command":
		return true
	}
	return false
}

// programKey reports whether key names a program git may run, with the value
// that runs nothing and whether the key holds a list. git prints the section
// and the last part of a key in lower case, and the subsection as written.
func programKey(key string) (neutral string, multi, ok bool) {
	section, rest, found := strings.Cut(key, ".")
	if !found {
		return "", false, false
	}
	sub, name, hasSub := splitKey(rest)
	switch section {
	case "filter":
		switch name {
		case "clean", "smudge", "process":
			return "", false, hasSub
		case "required":
			return "false", false, hasSub
		}
	case "diff":
		if !hasSub {
			return "", false, name == "external"
		}
		return "", false, name == "textconv" || name == "command"
	case "merge":
		return "", false, hasSub && name == "driver"
	case "gpg":
		switch {
		case name == "program":
			switch sub {
			case "x509":
				return "gpgsm", false, true
			case "ssh":
				return "ssh-keygen", false, true
			}
			return "gpg", false, true
		case sub == "ssh" && name == "defaultkeycommand":
			return "", false, true
		}
	case "core":
		if hasSub {
			return "", false, false
		}
		switch name {
		case "sshcommand":
			return "ssh", false, true
		case "askpass", "alternaterefscommand":
			return "", false, true
		case "gitproxy":
			return "none", false, true
		}
	case "remote":
		switch name {
		case "uploadpack":
			return "git-upload-pack", false, hasSub
		case "receivepack":
			return "git-receive-pack", false, hasSub
		}
	case "credential":
		return "", true, name == "helper"
	case "lfs":
		// git-lfs runs from the person's own filter and reads the
		// repository's config for programs of its own.
		switch {
		case strings.HasPrefix(sub, "customtransfer.") && name == "path":
			return "", false, true
		case strings.HasPrefix(sub, "extension.") && (name == "clean" || name == "smudge"):
			return "", false, true
		}
	}
	return "", false, false
}

// guardArgs adds the flags config cannot override, after the subcommand:
// --no-ext-diff and --no-textconv so no diff driver runs, and on diff and
// status --ignore-submodules=dirty, since a .gitmodules with ignore = none
// beats the diff.ignoreSubmodules of fixedSettings.
func guardArgs(args []string) []string {
	const (
		noDrivers = "--no-ext-diff --no-textconv"
		noLook    = "--ignore-submodules=dirty"
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-c" || a == "-C":
			i++
			continue
		case strings.HasPrefix(a, "-"):
			continue
		}
		var flags string
		switch a {
		case "diff":
			flags = noDrivers + " " + noLook
		case "log", "show", "diff-tree":
			flags = noDrivers
		case "status":
			flags = noLook
		default:
			return args
		}
		return slices.Concat(args[:i+1], strings.Fields(flags), args[i+1:])
	}
	return args
}
