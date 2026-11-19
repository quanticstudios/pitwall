package claude

import (
	"regexp"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
)

// Claude Code wraps what it adds to a conversation in tags: a slash
// command and its output, a ! command and its output, a background task's
// notification, the reminders it gives the model. harness reads them so a
// chat shows each as what it is and never the markup.

// tag is the text inside the first <name>…</name> in s, and whether s has
// one.
func tag(s, name string) (string, bool) {
	_, rest, ok := strings.Cut(s, "<"+name+">")
	if !ok {
		return "", false
	}
	in, _, _ := strings.Cut(rest, "</"+name+">")
	return in, true
}

// note is what one harness text says, for a chat.
type note struct {
	phase string // a session.Phase*, "" for nothing to show
	text  string
	// notified is a <task-notification>'s tool call, "" for none.
	notified, status, summary, result string
}

var compactLead = "This session is being continued from a previous conversation"

var ansiCodes = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// compacted reports whether a local command's output is /compact's own
// "Compacted", which the compaction's divider already says.
func compacted(out string) bool {
	return strings.HasPrefix(strings.TrimSpace(ansiCodes.ReplaceAllString(out, "")), "Compacted")
}

// harness reads text a user line carries that the user did not type.
func harness(text string) note {
	t := strings.TrimSpace(text)
	if name, ok := tag(t, "command-name"); ok {
		args, _ := tag(t, "command-args")
		if !strings.HasPrefix(name, "/") {
			name = "/" + name
		}
		return note{phase: session.PhaseCommand, text: strings.TrimSpace(name + " " + strings.TrimSpace(args))}
	}
	if in, ok := tag(t, "bash-input"); ok {
		return note{phase: session.PhaseCommand, text: "!" + strings.TrimSpace(in)}
	}
	if out, ok := tag(t, "local-command-stdout"); ok {
		return note{phase: session.PhaseOutput, text: strings.TrimSpace(out)}
	}
	if out, ok := tag(t, "local-command-stderr"); ok {
		return note{phase: session.PhaseOutput, text: strings.TrimSpace(out)}
	}
	if out, ok := tag(t, "bash-stdout"); ok {
		errs, _ := tag(t, "bash-stderr")
		return note{phase: session.PhaseOutput, text: strings.TrimSpace(strings.TrimRight(out, "\n") + "\n" + errs)}
	}
	if in, ok := tag(t, "task-notification"); ok {
		n := note{}
		n.notified, _ = tag(in, "tool-use-id")
		n.status, _ = tag(in, "status")
		n.summary, _ = tag(in, "summary")
		n.result, _ = tag(in, "result")
		return n
	}
	if strings.HasPrefix(t, compactLead) {
		return note{phase: session.PhaseCompact, text: compactSummary(t)}
	}
	for _, p := range hookFeedback {
		if strings.HasPrefix(t, p) {
			return note{phase: session.PhaseHook, text: t}
		}
	}
	// <local-command-caveat>, <system-reminder>, <user-prompt-submit-hook>
	// and the like speak to the model, not to the user.
	return note{}
}

// hookFeedback starts the text a hook's blocking feedback comes back as.
var hookFeedback = []string{"Stop hook feedback:", "SubagentStop hook feedback:", "UserPromptSubmit operation blocked by hook:"}

var reminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>\s*`)

// compactSummary is a compaction's summary without the lead line, the
// reminders and the instructions to the model after it.
func compactSummary(t string) string {
	t = reminder.ReplaceAllString(t, "")
	if _, rest, ok := strings.Cut(t, "\n"); ok && strings.HasPrefix(t, compactLead) {
		t = rest
	}
	if i := strings.Index(t, "\nIf you need specific details from before compaction"); i >= 0 {
		t = t[:i]
	}
	if i := strings.Index(t, "\nContinue the conversation from where it left off"); i >= 0 {
		t = t[:i]
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(t), "Summary:"))
}

// subagentTool reports whether a call runs a subagent: its card shows the
// subagent's calls and its result.
func subagentTool(name string) bool {
	switch name {
	case "Task", "Agent", "Workflow", "SubagentHandback":
		return true
	}
	return false
}

// settingNote is a /model or /effort command's output as the divider it
// becomes: Switched to Opus 5.5, Effort set to high. ok is false for any
// other output, an error say, which shows as the command's output.
func settingNote(out string) (string, bool) {
	out = strings.ReplaceAll(out, "`", "")
	if m, ok := strings.CutPrefix(out, "Set model to "); ok {
		m, _, _ = strings.Cut(m, " and ")
		m, _, _ = strings.Cut(m, " (")
		return "Switched to " + strings.TrimSpace(m), true
	}
	if e, ok := strings.CutPrefix(out, "Set effort level to "); ok {
		e, _, _ = strings.Cut(e, " ")
		return "Effort set to " + strings.TrimRight(strings.TrimSpace(e), ":."), true
	}
	return "", false
}

// localCommands are Claude Code's built-ins that run in the TUI and start
// no turn; their line, with or without an origin, is a command chip or a
// divider, never a prompt. Skills and user or plugin commands start a
// turn and stay prompts.
var localCommands = map[string]bool{
	"model": true, "effort": true, "compact": true, "clear": true, "context": true, "cost": true, "status": true,
	"usage": true, "config": true, "help": true, "login": true, "logout": true, "doctor": true, "memory": true,
	"permissions": true, "hooks": true, "mcp": true, "agents": true, "resume": true, "rewind": true, "export": true,
	"fast": true, "plugin": true, "ide": true, "terminal-setup": true, "vim": true, "theme": true, "add-dir": true,
	"release-notes": true, "bug": true, "feedback": true, "upgrade": true, "statusline": true, "privacy-settings": true,
	"todos": true, "tasks": true, "bashes": true, "output-style": true, "exit": true, "quit": true, "skills": true,
	"reload-plugins": true, "color": true, "focus": true, "rename": true, "recap": true,
}

// localCommand is whether "/name args" is one of localCommands.
func localCommand(cmd string) bool {
	name, _, _ := strings.Cut(strings.TrimPrefix(cmd, "/"), " ")
	return localCommands[name]
}
