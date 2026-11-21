package antigravity

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/skillshare"
)

// Builtins are agy 1.5.13's own slash commands, in the words of its CLI
// reference (antigravity.google/docs/cli/reference.md), less aliases and
// /swarm board, plus the skills agy ships that its menu lists. agy acp
// lists only the ones that run without the TUI: goal, compact, schedule,
// browser, plan, grill-me, teamwork, learn, boost and the skills. Terminal
// marks one that opens a panel, picker or editor of its own in the TUI.
var Builtins = []session.Command{
	{Name: "add-dir", Description: "Add a directory path to the active workspace.", Hint: "<path>"},
	{Name: "agents", Description: "Open the Agent Manager panel to switch custom agents and monitor background subagents.", Terminal: true},
	{Name: "agy-customizations", Description: "Load the customization reference so the agent can create, fix, or explain rules, skills, plugins, hooks, and MCP servers.", Hint: "[request]"},
	{Name: "antigravity-guide", Description: "Ask how to use or configure Antigravity and get an answer from the official documentation.", Hint: "[question]"},
	{Name: "artifact", Description: "Open the Artifact Review panel.", Terminal: true},
	{Name: "boost", Description: "Run on-demand multi-agent deep reasoning loops.", Hint: "<task>"},
	{Name: "browser", Description: "Delegate web browsing, UI verification, and in-browser debugging to the browser subagent.", Hint: "<task>"},
	{Name: "btw", Description: "Ask a side question in the background without interrupting the main conversation.", Hint: "<query>"},
	{Name: "changelog", Description: "Open the interactive release notes viewer or print CLI changes in headless mode.", Terminal: true},
	{Name: "clear", Description: "Clear the terminal and reset active conversation contexts."},
	{Name: "codesearch", Description: "Open the interactive Code Search panel to search code across the workspace.", Hint: "<query>", Terminal: true},
	{Name: "compact", Description: "Summarize the conversation so far into a compaction checkpoint to free up context, and optionally continue with <message>.", Hint: "[message]"},
	{Name: "config", Description: "Open the interactive Settings Editor overlay.", Terminal: true},
	{Name: "context", Description: "Open the context usage visualization panel.", Terminal: true},
	{Name: "copy", Description: "Copy the last agent response, the n-th most recent response, or the latest /btw answer.", Hint: "[n|btw]"},
	{Name: "credits", Description: "View remaining AI credits and purchase links."},
	{Name: "diff", Description: "Open the interactive diff viewer to view changes, turns, and commits.", Terminal: true},
	{Name: "effort", Description: "Set or inspect the active model's reasoning effort level (low through max, depending on the model).", Hint: "[level]"},
	{Name: "exit", Description: "Close the TUI session and restore your host shell."},
	{Name: "feedback", Description: "Open the feedback submission panel.", Terminal: true},
	{Name: "fork", Description: "Clone the current conversation thread into a new parallel session."},
	{Name: "goal", Description: "Run autonomously until the goal is verified complete, without intermediate pauses.", Hint: "<objective>"},
	{Name: "grill-me", Description: "Have the agent interview you one question at a time, with a recommended answer for each, before it starts work.", Hint: "[task]"},
	{Name: "help", Description: "Open the help panel showing commands and shortcuts.", Terminal: true},
	{Name: "hooks", Description: "Browse active pre-flight/post-format script hooks.", Terminal: true},
	{Name: "keybindings", Description: "Open the interactive Keyboard Shortcut Editor.", Terminal: true},
	{Name: "learn", Description: "Capture session corrections as persistent rules or skills through a reviewable proposal.", Hint: "[instruction]"},
	{Name: "logout", Description: "Disconnect your profile and purge authentication tokens from the secure keyring."},
	{Name: "mcp", Description: "Open the Model Context Protocol (MCP) server manager.", Terminal: true},
	{Name: "model", Description: "Choose your preferred reasoning model (persists across sessions).", Terminal: true},
	{Name: "open", Description: "Force the path to open inside your default system editor.", Hint: "<path>"},
	{Name: "permissions", Description: "Open the interactive tool permissions manager panel.", Terminal: true},
	{Name: "plan", Description: "Explore the codebase, clarify requirements, and draft a reviewable Implementation Plan.", Hint: "[task]"},
	{Name: "plugin", Description: "Open the interactive Plugins Manager or run inline install, uninstall, enable, disable, and list commands.", Terminal: true},
	{Name: "remote-control", Description: "Toggle Remote Control for the active terminal session.", Hint: "[on/off]"},
	{Name: "rename", Description: "Rename the current session thread.", Hint: "<name>"},
	{Name: "resume", Description: "Open the conversation picker overlay to select and load previous threads.", Terminal: true},
	{Name: "rewind", Description: "Roll back your conversation history to a previous message.", Terminal: true},
	{Name: "run-workflow", Description: "Write and run a dynamic workflow script that orchestrates many agents."},
	{Name: "schedule", Description: "Set a one-shot timer or recurring cron schedule that wakes the agent with an instruction.", Hint: "<when> <task>"},
	{Name: "skills", Description: "Browse loaded local and global Agent Skills. Run /skills reload to reload skills from disk.", Terminal: true},
	{Name: "statusline", Description: "Open the Status Bar customization overlay.", Terminal: true},
	{Name: "swarm", Description: "Run a swarm of named workers and a verifier that coordinate on a shared message board.", Hint: "<task>"},
	{Name: "tasks", Description: "Open the Task Manager panel to monitor background shell execution logs.", Terminal: true},
	{Name: "teamwork", Description: "Launch collaborative multi-agent teams for long-horizon projects (paid plans).", Hint: "<task>"},
	{Name: "title", Description: "Toggle or set terminal window title updates.", Hint: "[on/off]"},
	{Name: "usage", Description: "Display model quota usage."},
	{Name: "voice", Description: "Talk with the agent by voice (/voice live) or dictate a prompt (/voice dictate).", Terminal: true},
}

// userSkills are the folders under home agy 1.5.13 reads the user's
// skills from, and projectSkills the ones in each folder from the repo
// root down to the session's. It lists neither workflows nor
// .agents/commands.
var (
	userSkills    = []string{".gemini/antigravity-cli/skills", ".gemini/config/skills", ".gemini/skills"}
	projectSkills = []string{".agents/skills", ".agent/skills"}
)

// Commands is what agy's / menu lists for a session in cwd with home as
// HOME: Builtins and the user's and project's skills, by name. A name
// listed twice keeps the built-in, then the user's.
func Commands(home, cwd string) []session.Command {
	out := slices.Clone(Builtins)
	seen := map[string]bool{}
	for _, c := range out {
		seen[c.Name] = true
	}
	for _, f := range skillFiles(home, cwd) {
		c, ok := f.read()
		if ok && !seen[c.Name] {
			seen[c.Name] = true
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b session.Command) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// CommandsStamp changes when a skill Commands reads is added, removed or
// written. It stats the files and reads none.
func CommandsStamp(home, cwd string) string {
	var b strings.Builder
	for _, f := range skillFiles(home, cwd) {
		fmt.Fprintf(&b, "%s %d %d\n", f.path, f.mod, f.size)
	}
	return b.String()
}

type skillFile struct {
	dir, path, source string
	mod, size         int64
}

// skillFiles are the SKILL.md files under home's skill folders, then the
// project's from the repo root (or the top folder) down to cwd.
func skillFiles(home, cwd string) []skillFile {
	type root struct{ dir, source string }
	var roots, project []root
	if home != "" {
		for _, d := range userSkills {
			roots = append(roots, root{filepath.Join(home, d), "user"})
		}
	}
	for d := filepath.Clean(cwd); cwd != ""; d = filepath.Dir(d) {
		for _, s := range slices.Backward(projectSkills) {
			project = append(project, root{filepath.Join(d, s), "project"})
		}
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil || filepath.Dir(d) == d {
			break
		}
	}
	slices.Reverse(project)
	var out []skillFile
	for _, r := range append(roots, project...) {
		ents, _ := os.ReadDir(r.dir)
		for _, e := range ents {
			p := filepath.Join(r.dir, e.Name(), "SKILL.md")
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				out = append(out, skillFile{dir: e.Name(), path: p, source: r.source, mod: fi.ModTime().UnixNano(), size: fi.Size()})
			}
		}
	}
	return out
}

// read is f's command: named by its frontmatter, else its folder; false
// for one marked user-invocable: false or unreadable.
func (f skillFile) read() (session.Command, bool) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return session.Command{}, false
	}
	meta, _ := skillshare.SplitFrontmatter(string(b))
	c := session.Command{Name: f.dir, Source: f.source}
	for _, kv := range meta {
		v := strings.TrimSpace(kv.Value)
		switch kv.Key {
		case "name":
			c.Name = cmp.Or(v, c.Name)
		case "description":
			c.Description = v
		case "argument-hint":
			c.Hint = v
		case "user-invocable":
			if v == "false" {
				return session.Command{}, false
			}
		}
	}
	return c, true
}
