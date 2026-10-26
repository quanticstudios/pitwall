package errlog

import (
	"regexp"
	"strings"
)

// Fix is a likely cause and fix for the groups whose signature Match
// matches, or with Output, a line of their latest example's output, which
// keeps the paths a signature sets aside. Fix may use Match's groups, as $1.
type Fix struct {
	Match  *regexp.Regexp
	Fix    string
	Output bool
}

// zshLine is how zsh starts an error line in a signature: "zsh:" in a
// terminal, "(eval):N:" from an agent's eval.
const zshLine = `^(?:zsh|\(eval\))(?::N)?: `

// Fixes are the known causes, first match wins. Each is one seen in real
// logs, with a fix that holds for everyone: a wrong hint costs more than
// none.
var Fixes = []Fix{
	{Match: regexp.MustCompile(zshLine + `no matches found: `),
		Fix: "zsh expands an unquoted glob such as --include=*.go itself and fails when nothing matches. Tell the agent to quote globs passed as arguments: --include='*.go'."},
	{Match: regexp.MustCompile(zshLine + `=+ not found$`),
		Fix: "zsh expands a word that starts with = to a command's path, so a line of ===== fails. Tell the agent to quote such words: echo '====='."},
	{Match: regexp.MustCompile(zshLine + `defining function based on alias`),
		Fix: "A shell function has the name of an alias, which zsh refuses. Tell the agent to give its functions other names."},
	{Match: regexp.MustCompile(zshLine + `command not found: ([\w.+-]+)$|^\w+(?:: line N)?: ([\w.+-]+): command not found$`),
		Fix: "$1$2 is not installed. Install it, or tell the agent in its instructions what to use instead."},
	{Match: regexp.MustCompile(`^String to replace not found in file`),
		Fix: "The agent edited from a stale read of the file. Harmless now and then; often means it should read a file again before editing it."},
	{Match: regexp.MustCompile(`/tmp/.*No such file or directory`), Output: true,
		Fix: "The agent used a scratch file under /tmp before writing it, or after it was cleaned up. Harmless now and then."},
	{Match: regexp.MustCompile(`^npm (?:ERR!|error) Missing script`),
		Fix: "The script is not in package.json. Tell the agent to read the scripts in package.json before running npm run."},
	{Match: regexp.MustCompile(`(?i)^API Error\b.*\b(?:beta|model)\b.*\b(?:not (?:yet )?available|not found|does not exist)`),
		Fix: "The agent asked for a model or beta your plan does not include, such as a long context window. Change the model in the agent's settings."},
	{Match: regexp.MustCompile(`(?i)^API Error\b.*(?:rate.?limit|overloaded)`),
		Fix: "The provider was busy or you hit your rate limit. Nothing to fix locally; it passes once the limit resets."},
}

// LikelyFix is the fix for g's cause, "" when none is known.
func LikelyFix(g Group) string {
	for _, f := range Fixes {
		lines := []string{g.Sig}
		if f.Output {
			lines = strings.Split(g.Example.Output, "\n")
		}
		for _, l := range lines {
			if m := f.Match.FindStringSubmatchIndex(l); m != nil {
				return string(f.Match.ExpandString(nil, f.Fix, l, m))
			}
		}
	}
	return ""
}
