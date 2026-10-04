package decide

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Call is a tool call an agent asks permission for.
type Call struct {
	Tool  string          // tool_name: Bash, Write, apply_patch, mcp__x__y, ...
	Input json.RawMessage // tool_input
	Cwd   string          // the agent's working directory
	Root  string          // the session's repo root, or "" outside a repo
}

// Hard rule names, as the advice and the audit trail show them. None of
// them carries the call's own text.
const (
	RuleSudo        = "sudo"
	RuleRmRf        = "rm -rf"
	RuleForcePush   = "force push"
	RuleResetHard   = "git reset --hard"
	RuleSecrets     = "secrets path"
	RuleOutside     = "write outside the repo"
	RuleAgentConfig = "git or agent config"
	RuleUserPrefix  = "never_allow #" // then the entry's 1-based index
)

// Check is what pitwall can tell about a call before any model answers.
type Check struct {
	// Rules are the hard rules the call breaks.
	Rules []string
	// Unverified says why pitwall cannot fully check the call, "" when it
	// can. An unverified call is never approved automatically.
	Unverified string
}

// CanAllow reports whether auto mode may approve the call: it was fully
// checked and broke no rule.
func (c Check) CanAllow() bool { return c.Unverified == "" && len(c.Rules) == 0 }

// Label is the rule or reason to show next to a recommendation, "" for
// none.
func (c Check) Label() string {
	if len(c.Rules) > 0 {
		return c.Rules[0]
	}
	return c.Unverified
}

// Tools pitwall can check. Every other tool is unverified.
var (
	shellTools = []string{"Bash"}
	writeTools = map[string]string{"Write": "file_path", "Edit": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path"}
	readTools  = map[string]string{"Read": "file_path"}
)

// runners take other code to run, in arguments or on stdin, so the words
// alone do not say what happens.
var runners = []string{
	"sh", "bash", "zsh", "dash", "ksh", "mksh", "fish", "csh", "tcsh", "nu", "pwsh", "powershell", "cmd",
	"env", "nice", "nohup", "time", "timeout", "xargs", "exec", "command", "builtin", "eval", "source", "watch",
	"strace", "ltrace", "chroot", "unshare", "nsenter", "setsid", "stdbuf", "parallel", "script", "ssh", "su",
	"python", "python2", "python3", "pypy", "perl", "ruby", "node", "nodejs", "deno", "bun", "php", "lua", "tclsh",
	"awk", "gawk", "mawk", "nawk", "sed", "osascript", "make", "npx", "bunx", "pnpx", "uvx",
}

var sudoers = []string{"sudo", "doas", "pkexec", "run0"}

var (
	secretPathRe = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(?:\.(?:ssh|aws|gnupg|gpg|netrc|git-credentials|pgpass)(?:[/\\]|$)|\.kube[/\\]config|\.docker[/\\]config\.json|\.env[A-Za-z0-9_.-]*$|\.env[A-Za-z0-9_.-]*[/\\]|keychains?(?:[/\\]|$)|\.keychain(?:-db)?$|secret-tool|kwallet|gnome-keyring)`)
	patchFileRe  = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$|^\*\*\* Move to: (.+)$`)
)

// CheckCall classifies call. It fails closed: a tool it does not know,
// input it cannot read, shell syntax beyond plain words, or a path it
// cannot resolve leaves the call unverified. neverAllow are the user's
// extra rules, matched as substrings of the input's strings.
func CheckCall(c Call, neverAllow []string) Check {
	var chk Check
	rule := func(r string) {
		if !slices.Contains(chk.Rules, r) {
			chk.Rules = append(chk.Rules, r)
		}
	}
	unverified := func(why string) {
		if chk.Unverified == "" {
			chk.Unverified = why
		}
	}
	var input map[string]any
	if err := json.Unmarshal(c.Input, &input); err != nil || input == nil {
		unverified("unreadable input")
		input = nil
	}
	texts := stringsIn(input)
	for i, e := range neverAllow {
		if e == "" {
			continue
		}
		for _, t := range texts {
			if strings.Contains(t, e) {
				rule(fmt.Sprint(RuleUserPrefix, i+1))
				break
			}
		}
	}
	root, cwd := resolvedBase(c.Root, c.Cwd), resolvedBase(c.Cwd, "")
	switch {
	case slices.Contains(shellTools, c.Tool):
		cmd, ok := input["command"].(string)
		if !ok {
			unverified("command is not a string")
			break
		}
		checkShell(cmd, c.Cwd, root, cwd, rule, unverified)
	case writeTools[c.Tool] != "" || c.Tool == "apply_patch":
		var paths []string
		if c.Tool == "apply_patch" {
			cmd, _ := input["command"].(string)
			if !strings.HasPrefix(strings.TrimSpace(cmd), "*** Begin Patch") {
				unverified("not a patch")
				break
			}
			for _, m := range patchFileRe.FindAllStringSubmatch(cmd, -1) {
				paths = append(paths, strings.TrimSpace(m[1]+m[2]))
			}
		} else if p, ok := input[writeTools[c.Tool]].(string); ok && p != "" {
			paths = append(paths, p)
		}
		if len(paths) == 0 {
			unverified("no file to check")
		}
		for _, p := range paths {
			r, err := resolvePath(p, c.Cwd)
			if err != nil {
				unverified("path cannot be resolved")
				continue
			}
			if secretPathRe.MatchString(r) || secretPathRe.MatchString(p) {
				rule(RuleSecrets)
			}
			if !under(r, root) && !under(r, cwd) {
				rule(RuleOutside)
			}
			if inAgentConfig(r) {
				rule(RuleAgentConfig)
			}
		}
	case readTools[c.Tool] != "":
		p, ok := input[readTools[c.Tool]].(string)
		if !ok || p == "" {
			unverified("no file to check")
			break
		}
		r, err := resolvePath(p, c.Cwd)
		if err != nil {
			unverified("path cannot be resolved")
			break
		}
		if secretPathRe.MatchString(r) || secretPathRe.MatchString(p) {
			rule(RuleSecrets)
		}
	default:
		unverified("tool pitwall cannot check")
	}
	return chk
}

// checkShell checks a shell command that must be a plain list of words.
func checkShell(cmd, cwdRaw, root, cwd string, rule, unverified func(string)) {
	ws, why := shellWords(cmd)
	if why != "" {
		unverified(why)
		return
	}
	name := filepath.Base(ws[0])
	switch {
	case strings.Contains(ws[0], "="):
		unverified("a variable assignment")
	case slices.Contains(sudoers, name):
		rule(RuleSudo)
	case slices.Contains(runners, name) || strings.HasPrefix(name, "python"):
		unverified("runs other code: " + name)
	}
	args := ws[1:]
	switch name {
	case "rm":
		r, f := false, false
		for _, a := range args {
			switch {
			case a == "--recursive":
				r = true
			case a == "--force":
				f = true
			case strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--"):
				r = r || strings.ContainsAny(a, "rR")
				f = f || strings.Contains(a, "f")
			}
		}
		if r && f {
			rule(RuleRmRf)
		}
	case "git":
		sub, rest := "", []string(nil)
		for i := 0; i < len(args); i++ {
			a := args[i]
			switch {
			case a == "-c" || strings.HasPrefix(a, "--config-env") || strings.HasPrefix(a, "--exec-path") || strings.HasPrefix(a, "-c"):
				unverified("git config on the command line")
			case a == "-C" || a == "--git-dir" || a == "--work-tree" || a == "--namespace":
				i++
			case strings.HasPrefix(a, "-"):
			default:
				sub, rest = a, args[i+1:]
				i = len(args)
			}
		}
		switch sub {
		case "push":
			for _, a := range rest {
				if strings.HasPrefix(a, "--force") || strings.HasPrefix(a, "+") ||
					strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "f") {
					rule(RuleForcePush)
				}
			}
		case "reset":
			if slices.Contains(rest, "--hard") {
				rule(RuleResetHard)
			}
		}
	case "find":
		for _, a := range args {
			if slices.Contains([]string{"-exec", "-execdir", "-ok", "-okdir", "-delete", "-fprint", "-fprintf", "-fls"}, a) {
				unverified("find runs or deletes")
			}
		}
	}
	for i, w := range ws {
		parts := []string{w}
		if _, v, ok := strings.Cut(w, "="); ok {
			parts = append(parts, v)
		}
		for _, p := range parts {
			if secretPathRe.MatchString(p) {
				rule(RuleSecrets)
			}
			if i == 0 || !strings.Contains(p, "/") && p != ".." {
				continue
			}
			r, err := resolvePath(p, cwdRaw)
			switch {
			case err != nil:
				unverified("path cannot be resolved")
			case secretPathRe.MatchString(r):
				rule(RuleSecrets)
			case inAgentConfig(r):
				rule(RuleAgentConfig)
			case !under(r, root) && !under(r, cwd):
				unverified("a path outside the repo")
			}
		}
	}
}

// plainRune reports characters a plain shell word may hold: letters,
// digits and -_./:,+=@%^. Everything else (spaces aside) is shell syntax
// pitwall does not try to follow: $ ` ( ) { } < > | ; & * ? [ ] ~ ! # \
// and newlines.
func plainRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_./:,+=@%^", r)
}

// shellWords splits cmd into words when it is a plain list of words, with
// single or double quotes only grouping. why says what else it holds.
func shellWords(cmd string) (words []string, why string) {
	var cur strings.Builder
	inWord := false
	var quote rune
	for _, r := range cmd {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0 && (plainRune(r) || r == ' ' || r == '\t' || r == '\'' || r == '"'):
			cur.WriteRune(r)
		case quote != 0:
			return nil, fmt.Sprintf("shell syntax %q", r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case plainRune(r):
			cur.WriteRune(r)
			inWord = true
		default:
			return nil, fmt.Sprintf("shell syntax %q", r)
		}
	}
	if quote != 0 {
		return nil, "an unclosed quote"
	}
	if inWord {
		words = append(words, cur.String())
	}
	if len(words) == 0 {
		return nil, "an empty command"
	}
	return words, ""
}

// resolvedBase is base with symlinks resolved, or fallback's when base is
// empty; "" when neither resolves.
func resolvedBase(base, fallback string) string {
	if base == "" {
		base = fallback
	}
	if base == "" || !filepath.IsAbs(base) {
		return ""
	}
	r, err := resolvePath(base, "")
	if err != nil {
		return ""
	}
	return r
}

// resolvePath makes p absolute against cwd and resolves every symlink in
// it, dangling ones included: a link's target is followed as text, so a
// link to a file that does not exist yet still names where a write would
// land. An unreadable link or a loop is an error.
func resolvePath(p, cwd string) (string, error) {
	if !filepath.IsAbs(p) {
		if !filepath.IsAbs(cwd) {
			return "", errors.New("relative path without a working directory")
		}
		p = filepath.Join(cwd, p)
	}
	p = filepath.Clean(p)
	for hops := 0; hops <= 40; hops++ {
		vol := filepath.VolumeName(p)
		parts := strings.Split(strings.TrimPrefix(p[len(vol):], string(filepath.Separator)), string(filepath.Separator))
		cur := vol + string(filepath.Separator)
		link := false
		for i, part := range parts {
			if part == "" {
				continue
			}
			next := filepath.Join(cur, part)
			fi, err := os.Lstat(next)
			if errors.Is(err, fs.ErrNotExist) {
				return filepath.Join(append([]string{next}, parts[i+1:]...)...), nil
			}
			if err != nil {
				return "", err
			}
			if fi.Mode()&fs.ModeSymlink != 0 {
				t, err := os.Readlink(next)
				if err != nil {
					return "", err
				}
				if !filepath.IsAbs(t) {
					t = filepath.Join(cur, t)
				}
				p = filepath.Clean(filepath.Join(append([]string{t}, parts[i+1:]...)...))
				link = true
				break
			}
			cur = next
		}
		if !link {
			return cur, nil
		}
	}
	return "", errors.New("too many symlinks")
}

// under reports whether path is base or below it; both are resolved.
func under(path, base string) bool {
	if base == "" {
		return false
	}
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// inAgentConfig reports a path in .git, .claude or .codex: writing there
// can run code (git hooks) or grant the agent new permissions.
func inAgentConfig(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".git" || part == ".claude" || part == ".codex" || part == ".mcp.json" {
			return true
		}
	}
	return false
}

// stringsIn collects every string in v.
func stringsIn(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			for _, x := range t {
				walk(x)
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(v)
	return out
}
