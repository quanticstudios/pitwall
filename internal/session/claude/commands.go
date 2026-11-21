package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/skillshare"
)

// Builtins are the commands Claude Code 2.1.330's own / menu lists before
// a project's or user's: its built-ins and bundled skills, as its binary
// describes them, less aliases, hidden ones and ones only some accounts
// have. A description the binary builds at runtime is written here by
// hand.
var Builtins = []session.Command{
	{Name: "add-dir", Description: "Add a working directory"},
	{Name: "advisor", Description: "Let Claude consult a stronger model at key moments"},
	{Name: "auto-mode-setup", Description: "Teach auto mode about your environment"},
	{Name: "autocompact", Description: "Set how full the context gets before auto-summarizing"},
	{Name: "batch", Description: "Plan a large change; background agents each open a PR"},
	{Name: "branch", Description: "Create a branch of the current conversation at this point"},
	{Name: "btw", Description: "Ask a side question without interrupting Claude"},
	{Name: "cd", Description: "Move this session to a new working directory"},
	{Name: "claude-api", Description: "Build and debug apps that use the Claude API"},
	{Name: "clear", Description: "Start a new session with empty context"},
	{Name: "code-review", Description: "Review the current diff or a PR for bugs"},
	{Name: "color", Description: "Set the prompt bar color for this session"},
	{Name: "compact", Description: "Free up context by summarizing the conversation so far"},
	{Name: "config", Description: "Open settings"},
	{Name: "context", Description: "Show current context usage"},
	{Name: "copy", Description: "Copy the last response to clipboard"},
	{Name: "debug", Description: "Turn on debug logging and investigate problems"},
	{Name: "doctor", Description: "Health-check your setup and fix issues"},
	{Name: "effort", Description: "Set the model's effort level"},
	{Name: "exit", Description: "Exit the CLI"},
	{Name: "export", Description: "Export this session to a file or clipboard"},
	{Name: "fast", Description: "Toggle fast mode"},
	{Name: "feedback", Description: "Send feedback to Anthropic or report a bug"},
	{Name: "fewer-permission-prompts", Description: "Pre-approve safe read-only commands based on your usage"},
	{Name: "focus", Description: "Toggle focus view: just your prompt, summary, and response"},
	{Name: "fork", Description: "Copy this conversation into a new background session and keep working here"},
	{Name: "goal", Description: "Set a goal Claude checks before stopping"},
	{Name: "help", Description: "Show help and available commands"},
	{Name: "hooks", Description: "View hook configurations for tool events"},
	{Name: "ide", Description: "Manage IDE integrations and show status"},
	{Name: "import", Description: "Import config from another AI coding agent"},
	{Name: "init", Description: "Initialize a new CLAUDE.md file with codebase documentation"},
	{Name: "insights", Description: "Generate a report analyzing your Claude Code sessions"},
	{Name: "install-github-app", Description: "Set up Claude GitHub Actions for a repository"},
	{Name: "keybindings", Description: "Open your keyboard shortcuts file"},
	{Name: "list-agents", Description: "List subagents, teammates, and other Claude sessions you can message"},
	{Name: "login", Description: "Sign in with your Anthropic account"},
	{Name: "logout", Description: "Sign out from your Anthropic account"},
	{Name: "loop", Description: "Run a prompt or slash command on a recurring interval"},
	{Name: "mcp", Description: "Manage MCP servers"},
	{Name: "memory", Description: "Edit CLAUDE.md files and memory settings"},
	{Name: "model", Description: "Set the AI model for Claude Code"},
	{Name: "output-style", Description: "List output styles or switch to one"},
	{Name: "permissions", Description: "Manage allow and deny tool permission rules"},
	{Name: "plan", Description: "Turn on plan mode or view the plan"},
	{Name: "plugin", Description: "Manage Claude Code plugins"},
	{Name: "privacy-settings", Description: "View and update your privacy settings"},
	{Name: "recap", Description: "Generate a one-line session recap now"},
	{Name: "release-notes", Description: "View release notes"},
	{Name: "reload-plugins", Description: "Activate pending plugin changes in the current session"},
	{Name: "reload-skills", Description: "Pick up skills added or changed on disk during this session"},
	{Name: "rename", Description: "Rename the current conversation", Hint: "[name]"},
	{Name: "resume", Description: "Resume a previous session"},
	{Name: "rewind", Description: "Restore earlier code, conversation, or both"},
	{Name: "run", Description: "Launch this project's app to see your change working"},
	{Name: "run-skill-generator", Description: "Create a skill that knows how to run this project's app"},
	{Name: "schedule", Description: "Create and manage scheduled remote Claude Code agents"},
	{Name: "security-review", Description: "Review the branch's pending changes for security issues"},
	{Name: "simplify", Description: "Review the changed code for reuse and simplification, then fix it"},
	{Name: "skill-doctor", Description: "Show which loaded skills are unused and costing context"},
	{Name: "skills", Description: "List available skills"},
	{Name: "status", Description: "Show version, account, model, MCP servers, and setup warnings"},
	{Name: "statusline", Description: "Set up Claude Code's status line"},
	{Name: "tasks", Description: "View and manage this session's background work"},
	{Name: "team-onboarding", Description: "Help teammates ramp on Claude Code with a guide from your usage"},
	{Name: "terminal-setup", Description: "Set up your terminal's keys for newlines"},
	{Name: "theme", Description: "Change the theme"},
	{Name: "ultrareview", Description: "Find and verify bugs in your branch using a cloud session"},
	{Name: "update-config", Description: "Change settings: hooks, permissions, environment variables"},
	{Name: "usage", Description: "Show session cost, plan usage, and what's contributing to your limits"},
	{Name: "usage-credits", Description: "Configure usage credits or request them from your admin when you hit a limit"},
}

// headless are the built-ins Claude Code 2.1.330 runs under -p, as its
// system/init's slash_commands lists them.
var headless = map[string]bool{
	"advisor": true, "auto-mode-setup": true, "autocompact": true, "batch": true, "claude-api": true, "clear": true,
	"code-review": true, "color": true, "compact": true, "config": true, "context": true, "debug": true, "doctor": true,
	"effort": true, "fast": true, "fewer-permission-prompts": true, "focus": true, "goal": true, "import": true,
	"init": true, "insights": true, "list-agents": true, "loop": true, "mcp": true, "model": true, "output-style": true,
	"recap": true, "reload-plugins": true, "reload-skills": true, "rename": true, "run": true, "run-skill-generator": true,
	"schedule": true, "security-review": true, "simplify": true, "skill-doctor": true, "team-onboarding": true,
	"ultrareview": true, "update-config": true, "usage": true, "usage-credits": true,
}

// Headless is what a chat running claude -p offers before its first
// system/init says: Commands less the built-ins that need the TUI, except
// the ones the chat opens the terminal for.
func Headless(conf, cwd string) []session.Command {
	all := Commands(conf, cwd)
	out := all[:0:0]
	for _, c := range all {
		if c.Source != "" || headless[c.Name] || c.Terminal {
			out = append(out, c)
		}
	}
	return out
}

// ConfigDir is the config dir Claude Code reads with env over the
// daemon's own: CLAUDE_CONFIG_DIR, its last value in env winning, else
// ~/.claude; a relative one is taken from cwd. "" with no home.
func ConfigDir(env []string, cwd string) string {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "CLAUDE_CONFIG_DIR="); ok {
			dir = v
		}
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".claude")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(cwd, dir)
	}
	return dir
}

// Commands is what Claude Code's / menu lists for a session in cwd with
// config dir conf: skills, then custom commands, the user's before the
// project's, then Builtins. A name listed twice keeps its first. Plugins'
// commands are left out: which are on depends on settings merged from
// several files.
func Commands(conf, cwd string) []session.Command {
	files := commandFiles(conf, cwd)
	out := make([]session.Command, 0, len(files)+len(Builtins))
	seen := map[string]bool{}
	add := func(c session.Command) {
		if c.Name != "" && !seen[c.Name] {
			seen[c.Name] = true
			out = append(out, c)
		}
	}
	for _, f := range files {
		if c, ok := f.read(); ok {
			add(c)
		}
	}
	for _, c := range Builtins {
		c.Terminal = session.TerminalCommand(c.Name)
		add(c)
	}
	return out
}

// CommandsStamp changes when a skill or command file Commands reads is
// added, removed or written. It stats the files and reads none.
func CommandsStamp(conf, cwd string) string {
	var b strings.Builder
	for _, f := range commandFiles(conf, cwd) {
		fmt.Fprintf(&b, "%s %d %d\n", f.path, f.mod, f.size)
	}
	return b.String()
}

// Builtin is the built-in command name's description, "" for none.
func Builtin(name string) string {
	if i := slices.IndexFunc(Builtins, func(c session.Command) bool { return c.Name == name }); i >= 0 {
		return Builtins[i].Description
	}
	return ""
}

// cmdFile is a skill's SKILL.md or a custom command's file.
type cmdFile struct {
	name   string // the command's name, from its path
	path   string
	source string // user or project
	skill  bool
	mod    int64
	size   int64
}

// commandFiles are the skill files, then the command files, under conf
// and in each .claude folder from cwd's repo root (or the top folder)
// down to cwd.
func commandFiles(conf, cwd string) []cmdFile {
	type root struct{ dir, source string }
	roots := []root{}
	if conf != "" {
		roots = append(roots, root{conf, "user"})
	}
	var project []root
	for d := filepath.Clean(cwd); cwd != ""; d = filepath.Dir(d) {
		if dot := filepath.Join(d, ".claude"); dot != filepath.Clean(conf) {
			project = append(project, root{dot, "project"})
		}
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil || filepath.Dir(d) == d {
			break
		}
	}
	slices.Reverse(project)
	roots = append(roots, project...)
	var skills, cmds []cmdFile
	for _, r := range roots {
		ents, _ := os.ReadDir(filepath.Join(r.dir, "skills"))
		for _, e := range ents {
			p := filepath.Join(r.dir, "skills", e.Name(), "SKILL.md")
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				skills = append(skills, cmdFile{name: e.Name(), path: p, source: r.source, skill: true, mod: fi.ModTime().UnixNano(), size: fi.Size()})
			}
		}
		cmds = commandDir(cmds, filepath.Join(r.dir, "commands"), "", r.source, 0)
	}
	return append(skills, cmds...)
}

// commandDir adds dir's .md files as commands, a subfolder's as
// folder:name, following links, three folders deep at most.
func commandDir(out []cmdFile, dir, prefix, source string, depth int) []cmdFile {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		p := filepath.Join(dir, e.Name())
		fi, err := os.Stat(p)
		switch {
		case err != nil:
		case fi.IsDir() && depth < 3:
			out = commandDir(out, p, prefix+e.Name()+":", source, depth+1)
		case fi.Mode().IsRegular() && strings.HasSuffix(e.Name(), ".md"):
			out = append(out, cmdFile{name: prefix + strings.TrimSuffix(e.Name(), ".md"), path: p, source: source, mod: fi.ModTime().UnixNano(), size: fi.Size()})
		}
	}
	return out
}

// read is f's command: its frontmatter's description, else a command's
// first line; false for a skill marked user-invocable: false, which the
// menu leaves out, or a file that cannot be read.
func (f cmdFile) read() (session.Command, bool) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return session.Command{}, false
	}
	meta, body := skillshare.SplitFrontmatter(string(b))
	c := session.Command{Name: f.name, Source: f.source}
	for _, kv := range meta {
		switch kv.Key {
		case "description":
			c.Description = strings.TrimSpace(kv.Value)
		case "argument-hint":
			c.Hint = strings.TrimSpace(kv.Value)
		case "user-invocable":
			if strings.TrimSpace(kv.Value) == "false" {
				return session.Command{}, false
			}
		}
	}
	if c.Description == "" && !f.skill {
		for l := range strings.Lines(body) {
			if l = strings.TrimSpace(strings.TrimLeft(l, "# ")); l != "" {
				c.Description = l
				break
			}
		}
	}
	return c, true
}

// initCommands is the list a system/init's slash_commands make: in their
// order, the terminal ones marked, with menu's descriptions and sources;
// then menu's built-ins that open the terminal, which -p leaves out and
// the chat opens its terminal view for.
func initCommands(names, terminal []string, menu []session.Command) []session.Command {
	byName := map[string]session.Command{}
	for _, c := range menu {
		byName[c.Name] = c
	}
	out := make([]session.Command, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		c := byName[name]
		c.Name, seen[name] = name, true
		c.Terminal = session.TerminalCommand(name) || slices.Contains(terminal, name)
		out = append(out, c)
	}
	for _, c := range menu {
		if c.Terminal && !seen[c.Name] {
			out = append(out, c)
		}
	}
	return out
}
