package errlog

import (
	"regexp"
	"strings"
)

// Fix is a likely cause and fix for the groups whose signature Match
// matches, or with Output, a line of their latest example's output, which
// keeps the paths a signature sets aside, or with Command, its whole
// command. With Exit set, the example must also have exited with it;
// EmptyOutput requires no output. Fix may use Match's groups, as $1.
type Fix struct {
	Match       *regexp.Regexp
	Fix         string
	Output      bool
	Command     bool
	Exit        int
	EmptyOutput bool
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
	{Match: regexp.MustCompile(zshLine + `can't create temp file for here document: read-only file system`),
		Fix: "The agent's sandbox makes the temp folder read-only, and zsh needs a temp file for a here-document (<<EOF). Tell the agent to use printf, or write the text to a file in the workspace."},
	{Match: regexp.MustCompile(`ModuleNotFoundError: No module named '([\w.]+)'`), Output: true,
		Fix: "The Python module $1 is not installed. Install it, or tell the agent in its instructions what to use instead."},
	{Match: regexp.MustCompile(`^([\w.+-]+) not found$`),
		Fix: "$1 was not found. If it is a program, install it, or tell the agent in its instructions what to use instead."},
	{Match: regexp.MustCompile(`GraphQL: API rate limit already exceeded|gh: API rate limit already exceeded`),
		Fix: "GitHub's API rate limit ran out, usually from agents polling gh. Tell the agent not to poll gh pr checks or gh run watch; the limit resets within the hour."},
	{Match: regexp.MustCompile(`Projects \(classic\) is being deprecated.*projectCards`),
		Fix: "gh pr view and gh pr edit ask for classic project cards, which GitHub deprecated. Update gh, or tell the agent to pass --json with the fields it needs to gh pr view."},
	{Match: regexp.MustCompile(`^Claude Code is unable to fetch from |domains are not accessible to our user agent`),
		Fix: "The site refuses the agent's fetches, and retrying does not help. Tell the agent to stop after two refusals and use another source."},
	{Match: regexp.MustCompile(`^\s*pkill\s+(?:-[A-Za-z]+\s+)*(?:-[A-Za-z]*f[A-Za-z]*|--full)(?:\s|$)`), Command: true, Exit: 144, EmptyOutput: true,
		Fix: "pkill -f matched the agent's own shell, whose command line holds the pattern, and killed it. Tell the agent to find the PID with pgrep and kill <pid>."},
}

// LikelyFix is the fix for g's cause, "" when none is known.
func LikelyFix(g Group) string {
	for _, f := range Fixes {
		if f.Exit != 0 && g.Example.Exit != f.Exit {
			continue
		}
		if f.EmptyOutput && strings.TrimSpace(g.Example.Output) != "" {
			continue
		}
		lines := []string{g.Sig}
		switch {
		case f.Output:
			lines = strings.Split(g.Example.Output, "\n")
		case f.Command:
			lines = []string{g.Example.Command}
		}
		for _, l := range lines {
			if m := f.Match.FindStringSubmatchIndex(l); m != nil {
				return string(f.Match.ExpandString(nil, f.Fix, l, m))
			}
		}
	}
	return ""
}
