package gitstat

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

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
//     own config (local or worktree scope, includes counted), the value the
//     person's system, global or command-line config gives it, or a value that
//     runs nothing. A filter git-lfs installed in ~/.gitconfig keeps working.
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
// ponytail: one more git per git, so a config edit counts on the next command.
// A cache per folder would need to notice .git/config and every file it
// includes changing.
func Env(ctx context.Context, dir string, env []string) ([]string, error) {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+16)
	n := 0
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE":
			continue
		case "GIT_CONFIG_COUNT":
			if c, err := strconv.Atoi(value); err == nil && c > 0 {
				n = c
			}
		}
		out = append(out, kv)
	}
	out = append(out, "GIT_TERMINAL_PROMPT=0", "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true", "GIT_PAGER=cat")
	config, err := readConfig(ctx, dir, out)
	if err != nil {
		return out, err
	}
	return appendConfig(out, n, append(overridesFrom(config), fixedSettings()...)), nil
}

// repoFilter is a filter a config file of the repository at dir sets and the
// person's config does not, "" for none. pitwall does not run it, so a command
// that writes the index or the work tree would store or write that file's raw
// bytes: a plain secret where git-crypt keeps it encrypted. Those commands
// refuse instead, through refuseRepoFilter.
func repoFilter(ctx context.Context, dir string) (string, error) {
	entries, err := readConfig(ctx, dir, os.Environ())
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.trusted || e.value == "" || !strings.HasPrefix(e.key, "filter.") {
			continue
		}
		_, last := splitKey(e.key)
		if last != "clean" && last != "smudge" && last != "process" {
			continue
		}
		if !slices.ContainsFunc(entries, func(t configEntry) bool { return t.trusted && t.key == e.key }) {
			sub, _ := splitKey(strings.TrimPrefix(e.key, "filter."))
			return sub, nil
		}
	}
	return "", nil
}

// refuseRepoFilter is the error of a command that would bypass repoFilter.
func refuseRepoFilter(ctx context.Context, dir string) error {
	name, err := repoFilter(ctx, dir)
	if err != nil {
		return err
	}
	if name != "" {
		return fmt.Errorf("this repository's own config sets the %s filter, which pitwall does not run: do this in a terminal, or set the filter in your global git config", name)
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
	trusted    bool // from the person's config, see trustedScope
}

// readConfig reads every config git would use in dir.
func readConfig(ctx context.Context, dir string, env []string) ([]configEntry, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "config", "--show-scope", "-z", "--list")
	nowindow.Set(cmd)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git config: %s", msg)
		}
		return nil, fmt.Errorf("git config: %w", err)
	}
	// Each entry is the scope, NUL, the key, a newline and the value, NUL; a
	// key with no value has no newline.
	var entries []configEntry
	fields := strings.Split(stdout.String(), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		key, value, _ := strings.Cut(fields[i+1], "\n")
		entries = append(entries, configEntry{key: key, value: value, trusted: trustedScope(fields[i])})
	}
	return entries, nil
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
		if name, last := splitKey(strings.TrimPrefix(e.key, "hook.")); strings.HasPrefix(e.key, "hook.") && name != "" && (last == "command" || last == "event") {
			if key := "hook." + name + ".enabled"; !done[key] {
				done[key] = true
				over = append(over, [2]string{key, "false"})
			}
			continue
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

// splitKey splits a key at its last dot: "x.clean" is "x" and "clean". git
// prints a key's last part in lower case, and a subsection as written.
func splitKey(key string) (rest, last string) {
	i := strings.LastIndexByte(key, '.')
	if i < 0 {
		return "", key
	}
	return key[:i], key[i+1:]
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
	sub, name := splitKey(rest)
	switch section {
	case "filter":
		switch name {
		case "clean", "smudge", "process":
			return "", false, sub != ""
		case "required":
			return "false", false, sub != ""
		}
	case "diff":
		if sub == "" {
			return "", false, name == "external"
		}
		return "", false, name == "textconv" || name == "command"
	case "merge":
		return "", false, sub != "" && name == "driver"
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
		if sub != "" {
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
			return "git-upload-pack", false, sub != ""
		case "receivepack":
			return "git-receive-pack", false, sub != ""
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
