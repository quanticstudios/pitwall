package decide

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Call is a tool call an agent asks permission for.
type Call struct {
	Tool  string          // tool_name: Bash, Write, apply_patch, mcp__x__y, ...
	Input json.RawMessage // tool_input
	Cwd   string          // the agent's working directory
	Root  string          // the session's repo root, or "" outside a repo
}

// Hard rule names, as the audit trail shows them.
const (
	RuleSudo         = "sudo"
	RuleRmRf         = "rm -rf"
	RuleForcePush    = "force push"
	RuleResetHard    = "git reset --hard"
	RulePipeShell    = "pipe to shell"
	RuleSecrets      = "secrets path"
	RuleOutside      = "write outside the repo"
	RuleAgentConfig  = "git or agent config"
	RuleUserPrefix   = "never_allow: "
	shellBoundary    = `(?:^|[;&|({\x60\n]|\$\(|\s)\s*`
	shellSegmentRest = `[^;&|\n]*`
)

var (
	sudoRe = regexp.MustCompile(shellBoundary + `(?:sudo|doas|pkexec|run0)\b`)
	// rm with its arguments up to the end of the command segment.
	rmRe        = regexp.MustCompile(shellBoundary + `(?:\\|command\s+|builtin\s+)?rm\s+(` + shellSegmentRest + `)`)
	recursiveRe = regexp.MustCompile(`(?:^|\s)(?:-[A-Za-z]*[rR][A-Za-z]*|--recursive)\b`)
	forceRe     = regexp.MustCompile(`(?:^|\s)(?:-[A-Za-z]*f[A-Za-z]*|--force)\b`)
	pushRe      = regexp.MustCompile(`\bgit\b` + shellSegmentRest + `\bpush\b(` + shellSegmentRest + `)`)
	forcePushRe = regexp.MustCompile(`(?:^|\s)(?:--force(?:-with-lease|-if-includes)?(?:=\S*)?|-[A-Za-z]*f[A-Za-z]*|\+\S+)(?:\s|$)`)
	resetRe     = regexp.MustCompile(`\bgit\b` + shellSegmentRest + `\breset\b` + shellSegmentRest + `--hard\b`)
	pipeShellRe = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(?:curl|wget|fetch|iwr|irm|invoke-webrequest|invoke-restmethod)\b[^\n]*\|\s*(?:sudo\s+(?:-\S+\s+)*)?(?:env\s+)?(?:\S*/)?(?:ba|z|da|k|fi|c|tc|mk)?sh\b`),
		regexp.MustCompile(`(?i)\b(?:curl|wget|fetch|iwr|irm|invoke-webrequest|invoke-restmethod)\b[^\n]*\|\s*(?:sudo\s+)?(?:\S*/)?(?:python[0-9.]*|perl|ruby|node|php|iex|invoke-expression)\b`),
		regexp.MustCompile(`\b(?:(?:ba|z|da|k)?sh|source|eval|\.)\s+(?:-c\s+)?["']?(?:<\(|\$\()\s*(?:curl|wget)\b`),
	}
	secretPathRe = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(?:\.(?:ssh|aws|gnupg|gpg|kube/config|docker/config\.json|netrc|git-credentials|pgpass)(?:[/\\\s"':;|&)]|$)|\.env[A-Za-z0-9_.-]*(?:[/\\\s"':;|&)]|$)|keychains?(?:[/\\\s"']|$)|\.keychain(?:-db)?\b|\bsecurity\s+(?:find|dump|export|unlock)-|\bsecret-tool\b|\bkwallet|\bgnome-keyring)`)
	patchFileRe  = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$|^\*\*\* Move to: (.+)$`)
)

// writeTools are tool names whose path argument is written.
var writeTools = []string{"Write", "Edit", "MultiEdit", "NotebookEdit", "apply_patch", "Edit_file", "write_file"}

// HardRules lists the rules call breaks; any one means the call is never
// approved automatically, whatever a model says. extra are the user's
// never_allow strings, matched as plain substrings of the call's input.
//
// rm -rf counts anywhere, not only outside the repo: a shell line can
// reach outside through a variable, a glob, a symlink or an earlier cd,
// which the command text does not show.
func HardRules(c Call, extra []string) []string {
	var hits []string
	add := func(r string) {
		if !slices.Contains(hits, r) {
			hits = append(hits, r)
		}
	}
	var input map[string]any
	_ = json.Unmarshal(c.Input, &input)
	texts := stringsIn(input)
	cmd, _ := input["command"].(string)
	if a, ok := input["command"].([]any); ok { // Codex can send argv
		for _, x := range a {
			if s, ok := x.(string); ok {
				cmd += " " + s
			}
		}
	}
	if c.Tool != "apply_patch" && cmd != "" {
		if sudoRe.MatchString(cmd) {
			add(RuleSudo)
		}
		for _, m := range rmRe.FindAllStringSubmatch(cmd, -1) {
			if recursiveRe.MatchString(m[1]) && forceRe.MatchString(m[1]) {
				add(RuleRmRf)
			}
		}
		for _, m := range pushRe.FindAllStringSubmatch(cmd, -1) {
			if forcePushRe.MatchString(m[1]) {
				add(RuleForcePush)
			}
		}
		if resetRe.MatchString(cmd) {
			add(RuleResetHard)
		}
		for _, re := range pipeShellRe {
			if re.MatchString(cmd) {
				add(RulePipeShell)
			}
		}
	}
	var paths []string
	if c.Tool == "apply_patch" {
		for _, m := range patchFileRe.FindAllStringSubmatch(cmd, -1) {
			paths = append(paths, strings.TrimSpace(m[1]+m[2]))
		}
	}
	for _, k := range []string{"file_path", "notebook_path", "path"} {
		if s, ok := input[k].(string); ok && s != "" {
			paths = append(paths, s)
		}
	}
	for _, t := range append(texts, paths...) {
		if secretPathRe.MatchString(t) {
			add(RuleSecrets)
		}
	}
	if slices.Contains(writeTools, c.Tool) {
		for _, p := range paths {
			abs := p
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(c.Cwd, abs)
			}
			abs = filepath.Clean(abs)
			if !within(abs, c.Root) && !within(abs, c.Cwd) {
				add(RuleOutside)
			}
			if inAgentConfig(abs) {
				add(RuleAgentConfig)
			}
		}
	}
	for _, e := range extra {
		if e == "" {
			continue
		}
		for _, t := range append(texts, paths...) {
			if strings.Contains(t, e) {
				add(RuleUserPrefix + e)
				break
			}
		}
	}
	return hits
}

// within reports whether path is base or under it, also after resolving
// symlinks in both, so a link inside the repo to ~/.ssh is outside.
func within(path, base string) bool {
	if base == "" || !filepath.IsAbs(base) {
		return false
	}
	if !under(path, filepath.Clean(base)) {
		return false
	}
	rb, err := filepath.EvalSymlinks(base)
	if err != nil {
		return true
	}
	return under(resolveExisting(path), rb)
}

func under(path, base string) bool {
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// resolveExisting resolves symlinks in the longest existing prefix of path
// and joins the rest back on.
func resolveExisting(path string) string {
	rest := ""
	for p := path; ; {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		if _, err := os.Lstat(p); err == nil {
			return path // exists but cannot be resolved: a loop, say
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// inAgentConfig reports a path in .git, .claude or .codex: writing there
// can run code (git hooks) or grant the agent new permissions.
func inAgentConfig(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".git" || part == ".claude" || part == ".codex" {
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
