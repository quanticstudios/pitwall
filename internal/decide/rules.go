package decide

import (
	"encoding/json"
	"fmt"
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

// Flag names, as the advice shows them next to a recommendation. None of
// them carries the call's own text.
const (
	RuleSudo        = "sudo"
	RuleRmRf        = "rm -rf"
	RuleForcePush   = "force push"
	RuleResetHard   = "git reset --hard"
	RulePipeShell   = "pipe to shell"
	RuleSecrets     = "secrets path"
	RuleOutside     = "write outside the repo"
	RuleAgentConfig = "git or agent config"
	RuleUserPrefix  = "never_allow #" // then the entry's 1-based index
)

var (
	secretPathRe = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(?:\.(?:ssh|aws|gnupg|gpg|netrc|git-credentials|pgpass)(?:[/\\]|$)|\.kube[/\\]config|\.docker[/\\]config\.json|\.env[A-Za-z0-9_.-]*$|\.env[A-Za-z0-9_.-]*[/\\]|keychains?(?:[/\\]|$)|\.keychain(?:-db)?$|secret-tool|kwallet|gnome-keyring)`)
	patchFileRe  = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$|^\*\*\* Move to: (.+)$`)
	pipeShellRe  = regexp.MustCompile(`(?i)\b(?:curl|wget|fetch|iwr|irm|invoke-webrequest|invoke-restmethod)\b[^\n]*\|\s*(?:sudo\s+)?(?:\S*/)?['"]?(?:(?:ba|z|da|k|fi)?sh|python[0-9.]*|perl|ruby|node|iex|invoke-expression)\b|\b(?:(?:ba|z)?sh|source|eval)\s+["']?(?:<\(|\$\()\s*(?:curl|wget)\b`)
	segmentRe    = regexp.MustCompile(`&&|\|\||[;&|\n()]|\$\(|` + "`")
)

// fileTools name the input field holding the path each one touches.
var fileTools = map[string]string{"Read": "file_path", "Write": "file_path", "Edit": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path"}

var sudoers = []string{"sudo", "doas", "pkexec", "run0"}

// Flags lists the risks pitwall sees in call, for the advice next to a
// model's recommendation ("Jev: allow 96% · sudo"), so a risky call is
// marked even when the model says allow. They are hints, read from the
// call's text: they decide nothing, and a command's text cannot show
// everything it will run. neverAllow are the user's extra flags, matched
// as substrings of the input's strings and shown by number only.
func Flags(c Call, neverAllow []string) []string {
	var out []string
	flag := func(r string) {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	var input map[string]any
	_ = json.Unmarshal(c.Input, &input)
	for i, e := range neverAllow {
		if e == "" {
			continue
		}
		for _, t := range stringsIn(input) {
			if strings.Contains(t, e) {
				flag(fmt.Sprint(RuleUserPrefix, i+1))
				break
			}
		}
	}
	switch {
	case c.Tool == "Bash":
		cmd, _ := input["command"].(string)
		if pipeShellRe.MatchString(cmd) {
			flag(RulePipeShell)
		}
		for _, seg := range segmentRe.Split(cmd, -1) {
			if ws := looseWords(seg); len(ws) > 0 {
				shellFlags(ws, flag)
			}
		}
	case c.Tool == "apply_patch":
		cmd, _ := input["command"].(string)
		for _, m := range patchFileRe.FindAllStringSubmatch(cmd, -1) {
			pathFlags(strings.TrimSpace(m[1]+m[2]), c, true, flag)
		}
	case fileTools[c.Tool] != "":
		if p, _ := input[fileTools[c.Tool]].(string); p != "" {
			pathFlags(p, c, c.Tool != "Read", flag)
		}
	}
	return out
}

// pathFlags flags a secrets path, a write into git or agent config, and a
// write that leaves the repo and cwd.
func pathFlags(p string, c Call, write bool, flag func(string)) {
	if secretPathRe.MatchString(p) {
		flag(RuleSecrets)
	}
	if !write {
		return
	}
	abs := p
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(c.Cwd, abs)
	}
	abs = filepath.Clean(abs)
	if inAgentConfig(abs) {
		flag(RuleAgentConfig)
	}
	if !under(abs, c.Root) && !under(abs, c.Cwd) {
		flag(RuleOutside)
	}
}

// looseWords splits one command segment into words with quotes and
// backslashes removed, so 'sudo' and "--force" read as sudo and --force.
func looseWords(seg string) []string {
	var out []string
	for _, w := range strings.Fields(seg) {
		w = strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(w)
		if w != "" {
			out = append(out, w)
		}
	}
	// Skip leading VAR=value assignments and wrappers that run the next word.
	for len(out) > 0 && (strings.Contains(out[0], "=") || slices.Contains([]string{"env", "nice", "nohup", "time", "command", "exec", "xargs"}, filepath.Base(out[0]))) {
		out = out[1:]
	}
	return out
}

// shellFlags flags the risks in one command's words. A long option counts
// by any unambiguous prefix, as getopt takes it.
func shellFlags(ws []string, flag func(string)) {
	name, args := filepath.Base(ws[0]), ws[1:]
	if slices.Contains(sudoers, name) {
		flag(RuleSudo)
		if len(args) > 0 {
			shellFlags(args, flag)
		}
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
			flag(RuleRmRf)
		}
	case "git":
		for i, a := range args {
			switch a {
			case "push":
				for _, b := range args[i+1:] {
					if long(b, "--force") || long(b, "--force-with-lease") || long(b, "--force-if-includes") || strings.HasPrefix(b, "+") || short(b, "f") {
						flag(RuleForcePush)
					}
				}
			case "reset":
				for _, b := range args[i+1:] {
					if long(b, "--hard") {
						flag(RuleResetHard)
					}
				}
			}
		}
	}
	for _, w := range ws {
		if secretPathRe.MatchString(w) {
			flag(RuleSecrets)
		}
	}
}

// under reports whether path is base or below it, lexically.
func under(path, base string) bool {
	if base == "" || !filepath.IsAbs(base) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(base), path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// inAgentConfig reports a path in .git, .claude or .codex, or .mcp.json:
// writing there can run code (git hooks) or grant the agent new
// permissions.
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
