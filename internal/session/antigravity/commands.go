package antigravity

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/skillshare"
)

// Builtins are agy 1.5.14's own slash commands, in the words of its /
// menu for a signed-in Google AI Ultra account, read from its binary and
// that menu, with the aliases the menu takes. The skills agy ships that
// its menu lists come last, each with the first sentence agy acp gives
// it. agy hides /remote-control and /plugin from some accounts. agy acp
// lists only the ones that run without the TUI: goal, compact, schedule,
// browser, plan, grill-me, teamwork, learn, boost and the skills.
// Terminal marks one that opens a panel, picker or editor of its own in
// the TUI.
var Builtins = []session.Command{
	{Name: "add-dir", Description: "Add a directory to the workspace", Hint: "<path>"},
	{Name: "agents", Description: "List available custom agents", Terminal: true},
	{Name: "artifact", Description: "View and review artifacts", Terminal: true},
	{Name: "btw", Description: "Ask a side question without interrupting the current task", Hint: "<query>"},
	{Name: "changelog", Description: "Show release notes and changes", Terminal: true},
	{Name: "clear", Description: "Clear conversation and start a new one", Aliases: []string{"new"}},
	{Name: "codesearch", Description: "Search code in the workspace (usage: /codesearch <query>)", Hint: "<query>", Terminal: true, Aliases: []string{"cs", "search"}},
	{Name: "config", Description: "Open settings panel", Terminal: true, Aliases: []string{"settings"}},
	{Name: "context", Description: "Visualize current context usage", Terminal: true},
	{Name: "copy", Description: "Copy the last planner response to the clipboard, the n-th with '/copy n', or the /btw answer with '/copy btw'", Hint: "[n|btw]"},
	{Name: "credits", Description: "Show remaining G1 credits and purchase link", Terminal: true},
	{Name: "diff", Description: "View uncommitted changes and per-turn diffs", Terminal: true},
	{Name: "effort", Description: "Set the reasoning effort", Hint: "[level]"},
	{Name: "exit", Description: "Exit the CLI", Aliases: []string{"quit"}},
	{Name: "feedback", Description: "Submit qualitative feedback to improve the agent", Terminal: true},
	{Name: "fork", Description: "Create a branch of the current conversation at this point, optionally specifying a project ID to fork into", Aliases: []string{"branch"}},
	{Name: "help", Description: "Show available commands and keybindings", Terminal: true},
	{Name: "hooks", Description: "Manage hook configurations for tool events", Terminal: true},
	{Name: "keybindings", Description: "Set custom keybindings", Terminal: true},
	{Name: "logout", Description: "Log out"},
	{Name: "mcp", Description: "Manage MCP servers", Terminal: true},
	{Name: "model", Description: "Set a model, or run a single prompt on another model", Terminal: true},
	{Name: "open", Description: "Open a file or view opened/edited files", Hint: "<path>"},
	{Name: "permissions", Description: "Manage tool permissions", Terminal: true},
	{Name: "plugin", Description: "Manage, discover, install, uninstall, enable, and disable plugins", Terminal: true},
	{Name: "projects", Description: "Create and edit projects", Terminal: true},
	{Name: "record", Description: "Dictate a prompt using your microphone", Terminal: true},
	{Name: "remote-control", Description: "Turn on remote control for this session (off to disable)", Hint: "[on/off]"},
	{Name: "rename", Description: "Rename the current conversation", Hint: "<name>"},
	{Name: "resume", Description: "Browse and resume past conversations", Terminal: true, Aliases: []string{"switch", "conversation"}},
	{Name: "rewind", Description: "Rewind conversation to a previous message", Terminal: true, Aliases: []string{"undo"}},
	{Name: "skills", Description: "List available skills", Terminal: true},
	{Name: "statusline", Description: "Toggle the statusline"},
	{Name: "tasks", Description: "View background tasks", Terminal: true},
	{Name: "thinking", Description: "Toggle streaming the model's full thinking in the transcript"},
	{Name: "title", Description: "Toggle custom terminal window title", Hint: "[on/off]"},
	{Name: "usage", Description: "View model quota usage", Terminal: true, Aliases: []string{"quota"}},
	{Name: "voice", Description: "Dictate a prompt or talk with the agent by voice", Terminal: true},
	{Name: "goal", Description: "Run until the specified goal is completely finished.", Hint: "<objective>"},
	{Name: "compact", Description: "Compact the conversation now (summarize history to free up the context window).", Hint: "[message]"},
	{Name: "schedule", Description: "Run an instruction on a recurring schedule or as a one-time timer.", Hint: "<when> <task>"},
	{Name: "browser", Description: "Invoke a browser agent for web tasks.", Hint: "<task>"},
	{Name: "plan", Description: "Plan carefully before executing a task.", Hint: "[task]"},
	{Name: "grill-me", Description: "Interview me to align on a plan.", Hint: "[task]"},
	{Name: "teamwork", Description: "Invoke a team of agents to autonomously tackle large projects.", Hint: "<task>"},
	{Name: "learn", Description: "Reflect on recent successes or corrections to capture reusable skills or rules.", Hint: "[instruction]"},
	{Name: "boost", Description: "Invoke the Boost multi-agent orchestrator for complex tasks.", Hint: "<task>"},
	{Name: "agy-customizations", Description: "Comprehensive guide and reference for the Antigravity Customization System.", Hint: "[request]"},
	{Name: "antigravity-guide", Description: "Provides a comprehensive guide, quick reference, and sitemap for Google Antigravity (AGY), including the Antigravity CLI (agy), Antigravity 2.0, Antigravity IDE, Python SDK, slash commands, keybindings, and customizations (skills, rules, MCP, sidecars).", Hint: "[question]"},
	{Name: "run-workflow", Description: "How to write and run a dynamic workflow script with the `run_workflow` tool: a Python script that orchestrates many agents with `agent`, `parallel`, `pipeline`, and `phase`."},
	{Name: "swarm", Description: "(Preview & Quota Heavy) Run a swarm: several agents working in parallel on one task, each in its own conversation, collaborating through a shared message board.", Hint: "<task>"},
}

// userSkills are the folders under home agy 1.5.13 reads the user's
// skills from, and projectSkills the ones in each folder from the repo
// root down to the session's. It lists neither workflows nor
// .agents/commands.
var (
	userSkills    = []string{".gemini/antigravity-cli/skills", ".gemini/config/skills", ".gemini/skills"}
	projectSkills = []string{".agents/skills", ".agent/skills"}
)

// userPlugins are the folders under home agy 1.5.13 loads plugins from,
// and projectPlugins the one in each folder from the repo root down: each
// plugin a folder with a plugin.json, its skills in skills/, listed under
// their own names. ~/.gemini/config/config.json turns one off with
// plugins.<name>.enabled false.
var (
	userPlugins    = []string{".gemini/config/plugins", ".gemini/antigravity-cli/plugins"}
	projectPlugins = []string{".agents/plugins"}
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
// written, or the config that turns plugins off is. It stats the files
// and reads only that config.
func CommandsStamp(home, cwd string) string {
	var b strings.Builder
	for _, f := range skillFiles(home, cwd) {
		fmt.Fprintf(&b, "%s %d %d\n", f.path, f.mod, f.size)
	}
	if fi, err := os.Stat(filepath.Join(home, ".gemini", "config", "config.json")); home != "" && err == nil {
		fmt.Fprintf(&b, "config %d %d\n", fi.ModTime().UnixNano(), fi.Size())
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
	roots = append(roots, project...)
	off := pluginsOff(home)
	for _, dir := range pluginRoots(home, cwd) {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "plugin.json")); err == nil && !off[e.Name()] {
				roots = append(roots, root{filepath.Join(dir, e.Name(), "skills"), "plugin"})
			}
		}
	}
	var out []skillFile
	for _, r := range roots {
		ents, _ := os.ReadDir(r.dir)
		for _, e := range ents {
			if p, fi := skillMD(filepath.Join(r.dir, e.Name())); p != "" {
				out = append(out, skillFile{dir: e.Name(), path: p, source: r.source, mod: fi.ModTime().UnixNano(), size: fi.Size()})
			}
		}
	}
	return out
}

// skillMD is dir's SKILL.md, which agy also takes spelled skill.md, and
// its info; "" for none.
func skillMD(dir string) (string, os.FileInfo) {
	for _, n := range []string{"SKILL.md", "skill.md"} {
		p := filepath.Join(dir, n)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
			return p, fi
		}
	}
	return "", nil
}

// pluginRoots are the plugin folders under home, then the project's
// from the repo root down to cwd.
func pluginRoots(home, cwd string) []string {
	var out, project []string
	if home != "" {
		for _, d := range userPlugins {
			out = append(out, filepath.Join(home, d))
		}
	}
	for d := filepath.Clean(cwd); cwd != ""; d = filepath.Dir(d) {
		for _, p := range projectPlugins {
			project = append(project, filepath.Join(d, p))
		}
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil || filepath.Dir(d) == d {
			break
		}
	}
	slices.Reverse(project)
	return append(out, project...)
}

// pluginsOff are the plugins ~/.gemini/config/config.json turns off.
func pluginsOff(home string) map[string]bool {
	off := map[string]bool{}
	b, err := os.ReadFile(filepath.Join(home, ".gemini", "config", "config.json"))
	if home == "" || err != nil {
		return off
	}
	var c struct {
		Plugins map[string]struct {
			Enabled *bool `json:"enabled"`
		} `json:"plugins"`
	}
	if json.Unmarshal(b, &c) == nil {
		for name, p := range c.Plugins {
			off[name] = p.Enabled != nil && !*p.Enabled
		}
	}
	return off
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
