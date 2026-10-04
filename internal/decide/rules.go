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
	// Rules are the hard rules the call breaks, for the advice text.
	Rules []string
	// Unverified says why the call is not on the allowlist, "" when it
	// is. Only an allowlisted call is ever approved automatically.
	Unverified string
}

// CanAllow reports whether auto mode may approve the call: it is on the
// allowlist and broke no rule.
func (c Check) CanAllow() bool { return c.Unverified == "" && len(c.Rules) == 0 }

// Label is the rule or reason to show next to a recommendation, "" for
// none.
func (c Check) Label() string {
	if len(c.Rules) > 0 {
		return c.Rules[0]
	}
	return c.Unverified
}

var (
	secretPathRe = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(?:\.(?:ssh|aws|gnupg|gpg|netrc|git-credentials|pgpass)(?:[/\\]|$)|\.kube[/\\]config|\.docker[/\\]config\.json|\.env[A-Za-z0-9_.-]*$|\.env[A-Za-z0-9_.-]*[/\\]|keychains?(?:[/\\]|$)|\.keychain(?:-db)?$|secret-tool|kwallet|gnome-keyring)`)
	patchFileRe  = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$|^\*\*\* Move to: (.+)$`)
)

// fileTools name the input field holding the path each one touches.
var fileTools = map[string]string{"Read": "file_path", "Write": "file_path", "Edit": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path"}

var sudoers = []string{"sudo", "doas", "pkexec", "run0"}

// CheckCall classifies call against the allowlist (allowShell and
// pathAllowed) and the hard rules. Anything the allowlist does not name
// exactly is unverified, so auto mode leaves it to the user. neverAllow
// are the user's extra rules, matched as substrings of the input's
// strings; allowPrograms are extra bare program names the user allows.
func CheckCall(c Call, neverAllow, allowPrograms []string) Check {
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
	for i, e := range neverAllow {
		if e == "" {
			continue
		}
		for _, t := range stringsIn(input) {
			if strings.Contains(t, e) {
				rule(fmt.Sprint(RuleUserPrefix, i+1))
				break
			}
		}
	}
	path := func(p string) {
		if why := pathAllowed(p, c.Cwd, c.Root); why != "" {
			unverified(why)
		}
		if secretPathRe.MatchString(p) {
			rule(RuleSecrets)
		}
		if inAgentConfig(p) {
			rule(RuleAgentConfig)
		}
	}
	switch {
	case c.Tool == "Bash":
		cmd, ok := input["command"].(string)
		if !ok {
			unverified("command is not a string")
			break
		}
		ws, why := shellWords(cmd)
		if why != "" {
			unverified(why)
			break
		}
		denyRules(ws, rule)
		if why := allowShell(ws, allowPrograms, path, c.Cwd); why != "" {
			unverified(why)
		}
	case c.Tool == "apply_patch":
		cmd, _ := input["command"].(string)
		if !strings.HasPrefix(strings.TrimSpace(cmd), "*** Begin Patch") {
			unverified("not a patch")
			break
		}
		ms := patchFileRe.FindAllStringSubmatch(cmd, -1)
		if len(ms) == 0 {
			unverified("no file to check")
		}
		for _, m := range ms {
			path(strings.TrimSpace(m[1] + m[2]))
		}
	case fileTools[c.Tool] != "":
		p, ok := input[fileTools[c.Tool]].(string)
		if !ok || p == "" {
			unverified("no file to check")
			break
		}
		path(p)
	default:
		unverified("not on the allowlist: " + c.Tool)
	}
	return chk
}

// protected are path components auto mode never approves touching, with
// any component starting .env.
var protected = []string{".git", ".claude", ".codex", ".mcp.json", ".ssh", ".aws", ".gnupg", ".netrc", ".git-credentials", ".kube", ".docker", ".pgpass"}

// pathAllowed reports why auto mode may not approve touching p, "" when
// it may. p, as given and never cleaned, must lie in the repo root or
// cwd and hold no ".." and no protected component (.git, .claude, .env*,
// .ssh, ...); and no component below that base may be a symlink, checked
// with Lstat one component at a time. A link anywhere, dangling or not,
// means no decision.
func pathAllowed(p, cwd, root string) string {
	if p == "" {
		return "an empty path"
	}
	sep := func(r rune) bool { return r == '/' || r == filepath.Separator }
	var base, rest string
	switch {
	case filepath.IsAbs(p):
		for _, b := range []string{root, cwd} {
			if b == "" || !filepath.IsAbs(b) {
				continue
			}
			b = strings.TrimRightFunc(b, sep)
			if r, ok := strings.CutPrefix(p, b); ok && (r == "" || sep(rune(r[0]))) {
				base, rest = b, r
				break
			}
		}
		if base == "" {
			return "a path outside the repo"
		}
	case filepath.IsAbs(cwd):
		base, rest = strings.TrimRightFunc(cwd, sep), p
	default:
		return "no working directory"
	}
	cur, exists := base, true
	for _, part := range strings.FieldsFunc(rest, sep) {
		low := strings.ToLower(part)
		switch {
		case part == "..":
			return "a path with .."
		case slices.Contains(protected, low) || strings.HasPrefix(low, ".env"):
			return "a protected path: " + part
		}
		cur += string(filepath.Separator) + part
		if !exists || part == "." {
			continue
		}
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			exists = false
		case err != nil:
			return "a path that cannot be read"
		case fi.Mode()&fs.ModeSymlink != 0:
			return "a path through a symlink"
		}
	}
	return ""
}

// denyRules adds the hard rules plain shell words break, for the advice.
// A long option counts by any unambiguous prefix, as getopt takes it.
func denyRules(ws []string, rule func(string)) {
	name, args := filepath.Base(ws[0]), ws[1:]
	if slices.Contains(sudoers, name) {
		rule(RuleSudo)
	}
	long := func(a, opt string) bool {
		a, _, _ = strings.Cut(a, "=")
		return strings.HasPrefix(a, "--") && len(a) > 3 && strings.HasPrefix(opt, a)
	}
	short := func(a, letters string) bool {
		return strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], letters)
	}
	switch name {
	case "rm":
		r, f := false, false
		for _, a := range args {
			r = r || long(a, "--recursive") || short(a, "rR")
			f = f || long(a, "--force") || short(a, "f")
		}
		if r && f {
			rule(RuleRmRf)
		}
	case "git":
		for i, a := range args {
			switch a {
			case "push":
				for _, b := range args[i+1:] {
					if long(b, "--force") || long(b, "--force-with-lease") || long(b, "--force-if-includes") || strings.HasPrefix(b, "+") || short(b, "f") {
						rule(RuleForcePush)
					}
				}
			case "reset":
				for _, b := range args[i+1:] {
					if long(b, "--hard") {
						rule(RuleResetHard)
					}
				}
			}
		}
	}
	for _, w := range ws {
		if secretPathRe.MatchString(w) {
			rule(RuleSecrets)
		}
	}
}

// prog is how auto mode treats one allowlisted program: the subcommands
// it may run, the flags it may take (exact strings, plus digits attached
// to num, as in -j8 or git log -5), and an extra check over its words.
type prog struct {
	subs  []string
	flags []string
	num   string
	check func(args []string) string
	free  bool // flags are not checked: a program the user added
}

var pkgFlags = []string{"--frozen-lockfile", "--no-audit", "--prefer-offline"}

var progs = map[string]prog{
	"ls":    {flags: []string{"-l", "-a", "-A", "-la", "-al", "-lh", "-lah", "-alh", "-h", "-R", "-1", "-t", "-S", "-r", "-d", "-F"}},
	"cat":   {flags: []string{"-n", "-A"}},
	"head":  {flags: []string{"-n", "-c"}},
	"tail":  {flags: []string{"-n", "-c"}},
	"wc":    {flags: []string{"-l", "-w", "-c", "-m"}},
	"grep":  {flags: []string{"-r", "-R", "-n", "-i", "-l", "-L", "-c", "-v", "-w", "-E", "-F", "-H", "-h", "-o", "-s", "-q", "-e", "-rn", "-ri", "-rni", "--color=never"}},
	"rg":    {flags: []string{"-n", "-i", "-l", "-c", "-w", "-F", "-S", "-e", "-t", "--hidden", "--files", "--no-heading"}},
	"fd":    {flags: []string{"-H", "-I", "-t", "-e", "-d"}},
	"find":  {flags: []string{"-name", "-iname", "-type", "-maxdepth", "-mindepth", "-path", "-ipath", "-print", "-size", "-mtime", "-newer", "-empty", "-not", "-o", "-a", "-and", "-or"}},
	"tree":  {flags: []string{"-L", "-a", "-d"}},
	"stat":  {},
	"file":  {flags: []string{"-b", "-i"}},
	"du":    {flags: []string{"-s", "-h", "-sh", "-d"}},
	"diff":  {flags: []string{"-u", "-r", "-q", "-N", "-ru", "-ur"}},
	"pwd":   {},
	"echo":  {flags: []string{"-n"}},
	"which": {flags: []string{"-a"}},
	"go": {subs: []string{"build", "test", "vet", "fmt", "list", "version", "env", "doc", "mod", "run"},
		flags: []string{"-v", "-race", "-run", "-count", "-timeout", "-short", "-cover", "-json", "-tags", "-o", "-bench", "-benchmem", "-failfast", "-p", "-cpu", "-trimpath"},
		check: func(args []string) string {
			if args[0] == "mod" && (len(args) != 2 || !slices.Contains([]string{"tidy", "download", "verify"}, args[1])) {
				return "go mod " + strings.Join(args[1:], " ")
			}
			return ""
		}},
	"cargo": {subs: []string{"build", "test", "check", "clippy", "fmt", "doc", "tree"},
		flags: []string{"--release", "--all", "--workspace", "--all-targets", "--all-features", "--lib", "--bins", "--tests", "-p", "--package", "-q", "--quiet", "-v", "--verbose", "--no-deps", "--check", "--", "--nocapture"}},
	"npm":  {subs: []string{"test", "run", "install", "ci", "ls"}, flags: pkgFlags, check: packageCheck},
	"pnpm": {subs: []string{"test", "run", "install", "ci", "ls"}, flags: pkgFlags, check: packageCheck},
	"yarn": {subs: []string{"test", "run", "install", "ci", "ls"}, flags: pkgFlags, check: packageCheck},
	"bun":  {subs: []string{"test", "run", "install", "ci", "ls"}, flags: pkgFlags, check: packageCheck},
	"make": {flags: []string{"-s", "-k", "-n"}, num: "-j", check: func(args []string) string {
		target := false
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if strings.Contains(a, "=") {
				return "a make variable"
			}
			target = true
		}
		if !target {
			return "make without a target"
		}
		return ""
	}},
	"pytest": {flags: []string{"-q", "-v", "-x", "-k", "-s", "--lf", "--ff"}},
	"mise":   {subs: []string{"ls", "which", "current"}},
	"git":    {subs: []string{"status", "diff", "log", "show", "branch", "blame", "rev-parse", "ls-files", "grep", "describe", "remote"}, check: gitCheck, free: true},
}

// packageCheck allows test, ls, run with one script name, and install or
// ci with no package names (the lockfile's own dependencies).
func packageCheck(args []string) string {
	var rest []string
	for _, a := range args[1:] {
		if !strings.HasPrefix(a, "-") {
			rest = append(rest, a)
		}
	}
	switch {
	case args[0] == "run" && len(rest) != 1:
		return "run needs one script name"
	case args[0] != "run" && args[0] != "test" && len(rest) > 0:
		return args[0] + " with package names"
	}
	return ""
}

// gitFlags are the flags each git subcommand may take; nothing goes
// before the subcommand.
var gitFlags = map[string][]string{
	"status":    {"-s", "--short", "-b", "--branch", "--porcelain"},
	"diff":      {"--stat", "--cached", "--staged", "--name-only", "--name-status", "-w", "--no-color", "--color=never", "--"},
	"log":       {"--oneline", "-n", "-p", "--stat", "--graph", "--decorate", "--all", "--no-color", "--name-only", "--"},
	"show":      {"--stat", "--name-only", "--oneline", "--no-color", "--"},
	"branch":    {"-a", "-r", "-v", "-vv", "--list", "--show-current", "--no-color"},
	"blame":     {"-L", "-w", "-s", "--"},
	"rev-parse": {"--show-toplevel", "--abbrev-ref", "--short"},
	"ls-files":  {"-m", "-o", "-d", "--others", "--exclude-standard", "-s", "--"},
	"grep":      {"-n", "-i", "-l", "-w", "-c", "-e", "-E", "-F", "--"},
	"describe":  {"--tags", "--always", "--long", "--dirty"},
	"remote":    {"-v"},
}

// gitCheck limits git to reading: only the listed flags of each
// subcommand, branch only lists, remote only -v.
func gitCheck(args []string) string {
	sub, rest := args[0], args[1:]
	for _, a := range rest {
		if strings.HasPrefix(a, "-") && a != "-" && !slices.Contains(gitFlags[sub], a) && !(sub == "log" && isNum(a[1:])) {
			return "git " + sub + " flag " + a
		}
		if sub == "branch" && !strings.HasPrefix(a, "-") {
			return "git branch with a name"
		}
	}
	if sub == "remote" && len(rest) != 1 {
		return "git remote other than -v"
	}
	return ""
}

func isNum(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }

// allowShell reports why plain shell words ws are not on the allowlist,
// "" when they are. The program must be a bare name from progs or
// allowPrograms; every flag must be one its entry lists; every other word
// that contains a slash or names something in cwd goes to path, which
// applies pathAllowed.
func allowShell(ws, allowPrograms []string, path func(string), cwd string) string {
	name, args := ws[0], ws[1:]
	if strings.ContainsAny(name, `/\=`) {
		return "not a bare program name: " + name
	}
	pr, ok := progs[name]
	if (name == "python" || name == "python3") && len(args) >= 2 && args[0] == "-m" && args[1] == "pytest" {
		pr, ok, args = progs["pytest"], true, args[2:]
	}
	if !ok && slices.Contains(allowPrograms, name) {
		pr, ok = prog{free: true}, true
	}
	if !ok {
		return "not on the allowlist: " + name
	}
	if len(pr.subs) > 0 && (len(args) == 0 || !slices.Contains(pr.subs, args[0])) {
		return name + " subcommand not on the allowlist"
	}
	if pr.check != nil {
		if why := pr.check(args); why != "" {
			return why
		}
	}
	words := args
	if len(pr.subs) > 0 {
		words = args[1:]
	}
	for _, a := range words {
		if strings.HasPrefix(a, "-") && a != "-" {
			if _, v, ok := strings.Cut(a, "="); ok && strings.Contains(v, "/") {
				path(v)
			}
			if pr.free || slices.Contains(pr.flags, a) || pr.num != "" && strings.HasPrefix(a, pr.num) && isNum(a[len(pr.num):]) {
				continue
			}
			return name + " flag " + a
		}
		if strings.Contains(a, "/") || exists(a, cwd) {
			path(a)
		}
	}
	return ""
}

// exists reports whether word names something in cwd.
func exists(word, cwd string) bool {
	if !filepath.IsAbs(cwd) {
		return false
	}
	_, err := os.Lstat(filepath.Join(cwd, word))
	return err == nil
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
