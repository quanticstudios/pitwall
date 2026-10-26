// Package guard denies the shell commands an agent in a guarded tab must
// leave to the human: pushes, merges, applies and deletes. It is a seatbelt,
// not a sandbox: it reads the command an agent asks to run, and a command
// that hides git push from a plain reading gets through.
package guard

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Env turns the guard on for the agents of a pane: pitwall sets it to 1 in a
// guarded tab, and the hooks check commands while it is set.
const Env = "PITWALL_GUARD"

// Adapted from forge (MIT): internal/pipeline/permissions.go

// Deny is the built-in list. Config adds to it and never removes from it.
var Deny = []string{
	// why: no `git * push *`, it would deny `git stash push`.
	"git push *", "git -C * push *", "git -c * push *", "git --* push *",
	"gh pr merge *", "gh pr create *", "gh * pr merge *", "gh * pr create *",
	// why: the `<bin> * <sub> *` forms catch global flags (`tofu -chdir=d apply`, `kubectl --context c delete`, `gh -R o/r pr merge`).
	"tofu apply *", "tofu destroy *", "tofu * apply *", "tofu * destroy *",
	"terraform apply *", "terraform destroy *", "terraform * apply *", "terraform * destroy *",
	"just *apply *", "just *destroy *",
	"kubectl apply *", "kubectl delete *", "kubectl * apply *", "kubectl * delete *",
	"helm install *", "helm upgrade *", "helm uninstall *",
	"helm * install *", "helm * upgrade *", "helm * uninstall *",
}

// Adapted from forge (MIT): internal/pipeline/guard.go

// Match reports whether value matches pattern: `*` is any run, `?` one
// character, the whole value must match, and a trailing " *" also matches
// the bare command.
func Match(pattern, value string) bool {
	var re strings.Builder
	for _, c := range pattern {
		switch c {
		case '*':
			re.WriteString(".*")
		case '?':
			re.WriteString(".")
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	expr := re.String()
	if bare, ok := strings.CutSuffix(expr, " .*"); ok {
		expr = "(?:" + expr + "|" + bare + ")"
	}
	return regexp.MustCompile("^(?s:" + expr + ")$").MatchString(value)
}

// Check returns the first simple command of a tool call's input that Deny
// or extra denies, "" when it denies none.
func Check(extra []string, toolInput json.RawMessage) string {
	for _, cmd := range shellCommands(toolInput) {
		for _, list := range [][]string{Deny, extra} {
			for _, p := range list {
				if Match(p, cmd) {
					return cmd
				}
			}
		}
	}
	return ""
}

// Reason is what the agent reads when the guard denies cmd.
func Reason(cmd string) string {
	return "pitwall guard: `" + cmd + "` is for the human; stop and ask."
}

var (
	segmentSep = regexp.MustCompile(`&&|\|\||[;|&\n]`)
	envAssign  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=\S*$`)
)

// shellCommands splits a tool call's command into simple commands: one per
// `&&`, `||`, `;`, `|`, `&` or newline, leading VAR=val dropped, spaces
// collapsed. A `sh -c <script>` argv is replaced by its script.
// ponytail: no shell parser, so `$(git push)`, `eval`, `sh -c '...'` in a string, or a script file slip through; a seatbelt, not a sandbox.
func shellCommands(toolInput json.RawMessage) []string {
	var in map[string]json.RawMessage
	if json.Unmarshal(toolInput, &in) != nil {
		return nil
	}
	raw, ok := in["command"]
	if !ok {
		raw = in["cmd"]
	}
	script := ""
	var argv []string
	if json.Unmarshal(raw, &script) != nil {
		if json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
			return nil
		}
		script = strings.Join(argv, " ")
		if len(argv) >= 3 && strings.HasPrefix(argv[len(argv)-2], "-") && strings.Contains(argv[len(argv)-2], "c") {
			script = argv[len(argv)-1]
		}
	}
	var out []string
	for _, seg := range segmentSep.Split(script, -1) {
		f := strings.Fields(seg)
		for len(f) > 0 && envAssign.MatchString(f[0]) {
			f = f[1:]
		}
		if len(f) > 0 {
			out = append(out, strings.Join(f, " "))
		}
	}
	return out
}
