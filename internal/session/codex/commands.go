package codex

import (
	"cmp"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/skillshare"
)

// Builtins are the commands Codex 0.171.0's / menu lists for a ChatGPT
// login, in its order and words, read from its binary and its menu. It
// lists nothing else: neither $CODEX_HOME/prompts nor skills show there;
// Codex takes a skill as $name, anywhere in the prompt.
// Aliases are the names its menu also takes. /apps is in only for an
// account with apps; left out are /debug-config, which the menu never
// lists, the Desktop app's command (macOS and Windows), Windows' sandbox
// setup and debug builds' own. Terminal marks one that opens a picker or
// a view of its own in the TUI.
var Builtins = []session.Command{
	{Name: "model", Description: "choose what model and reasoning effort to use", Terminal: true},
	{Name: "fast", Description: "2x speed, increased usage"},
	{Name: "ultrafast", Description: "The fastest available responses for latency-sensitive work."},
	{Name: "presets", Description: "run or save reusable prompts and slash commands", Terminal: true},
	{Name: "daybreak", Description: "Learn about broader access for cybersecurity work"},
	{Name: "ide", Description: "include current selection, open files, and other context from your IDE"},
	{Name: "permissions", Description: "choose what Codex is allowed to do", Terminal: true},
	{Name: "keymap", Description: "remap TUI shortcuts", Terminal: true},
	{Name: "vim", Description: "toggle Vim mode for the composer"},
	{Name: "experimental", Description: "toggle experimental features", Terminal: true},
	{Name: "approve", Description: "approve one retry of a recent auto-review denial"},
	{Name: "memories", Description: "configure memory use and generation", Terminal: true},
	{Name: "skills", Description: "use skills to improve how Codex performs specific tasks", Terminal: true},
	{Name: "import", Description: "import setup, this project, and recent chats from Claude Code", Terminal: true},
	{Name: "hooks", Description: "view and manage lifecycle hooks", Terminal: true},
	{Name: "review", Description: "review my current changes and find issues"},
	{Name: "rename", Description: "rename the current thread"},
	{Name: "new", Description: "start a new chat during a conversation"},
	{Name: "archive", Description: "archive this session"},
	{Name: "delete", Description: "permanently delete this session"},
	{Name: "resume", Description: "resume a saved chat", Terminal: true},
	{Name: "fork", Description: "fork the current chat"},
	{Name: "worktree", Description: "start or continue a conversation in a new worktree"},
	{Name: "init", Description: "create an AGENTS.md file with instructions for Codex"},
	{Name: "compact", Description: "summarize conversation to prevent hitting the context limit"},
	{Name: "config", Description: "configure Codex preferences", Terminal: true},
	{Name: "recap", Description: "summarize the current conversation now"},
	{Name: "plan", Description: "switch to Plan mode"},
	{Name: "voice", Description: "start or stop voice; use /voice settings to choose a voice"},
	{Name: "goal", Description: "set or view the goal for a long-running task"},
	{Name: "agents", Description: "open the agent command center", Terminal: true},
	{Name: "side", Description: "start a side conversation", Aliases: []string{"btw"}},
	{Name: "copy", Description: "copy the last response or part of it"},
	{Name: "export", Description: "export the conversation as markdown"},
	{Name: "raw", Description: "toggle raw scrollback mode for copy-friendly terminal selection"},
	{Name: "tui", Description: "choose the TUI mode for the next launch", Terminal: true},
	{Name: "diff", Description: "show git diff (including untracked files)"},
	{Name: "mention", Description: "mention a file"},
	{Name: "status", Description: "show current session configuration and token usage"},
	{Name: "daemon", Description: "Manage the local background server", Terminal: true},
	{Name: "rc", Description: "manage remote control for the local background server", Terminal: true},
	{Name: "warnings", Description: "view retained warnings and diagnostic details", Terminal: true},
	{Name: "cd", Description: "change the current working directory"},
	{Name: "pwd", Description: "show the current working directory"},
	{Name: "usage", Description: "view account usage or use a usage limit reset"},
	{Name: "title", Description: "configure which items appear in the terminal title", Terminal: true},
	{Name: "statusline", Description: "configure which items appear in the status line", Terminal: true},
	{Name: "theme", Description: "choose a syntax highlighting theme", Terminal: true},
	{Name: "pets", Description: "choose or hide the terminal pet", Terminal: true},
	{Name: "mcp", Description: "list MCP tools; /mcp verbose or /mcp login <name> [--environment <id>]"},
	{Name: "apps", Description: "manage apps", Terminal: true},
	{Name: "plugins", Description: "browse plugins", Terminal: true},
	{Name: "logout", Description: "log out of Codex"},
	{Name: "exit", Description: "exit Codex", Aliases: []string{"quit"}},
	{Name: "feedback", Description: "send logs to maintainers or save a ZIP", Terminal: true},
	{Name: "ps", Description: "list background terminals"},
	{Name: "stop", Description: "stop all background terminals"},
	{Name: "clear", Description: "clear the terminal and start a new chat"},
	{Name: "subagents", Description: "switch between this session's subagents", Terminal: true},
}

// Commands is a Codex TUI's / menu for a session in cwd, with codexHome
// as CODEX_HOME and home as HOME: Builtins, then Skills.
func Commands(codexHome, home, cwd string) []session.Command {
	return append(slices.Clone(Builtins), Skills(codexHome, home, cwd)...)
}

// Skills is what Codex 0.172.0's skills/list says for a session in cwd:
// the project's, the user's with the plugins', then Codex's own, each by
// name, a name listed twice kept the first time. It does not read
// config.toml, so a skill or plugin turned off there still shows.
func Skills(codexHome, home, cwd string) []session.Command {
	files := skillFiles(codexHome, home, cwd)
	out := make([]session.Command, 0, len(files))
	seen := map[string]bool{}
	for _, f := range files {
		if c, ok := f.read(); ok && !seen[c.Name] {
			seen[c.Name] = true
			out = append(out, c)
		}
	}
	rank := map[string]int{"project": 0, "user": 1, "plugin": 1, "system": 2}
	slices.SortStableFunc(out, func(a, b session.Command) int {
		return cmp.Or(rank[a.Source]-rank[b.Source], strings.Compare(a.Name, b.Name))
	})
	return out
}

// CommandsStamp changes when a skill Skills reads is added, removed or
// written. It stats the files and reads none.
func CommandsStamp(codexHome, home, cwd string) string {
	var b strings.Builder
	for _, f := range skillFiles(codexHome, home, cwd) {
		fmt.Fprintf(&b, "%s %d %d\n", f.path, f.mod, f.size)
	}
	return b.String()
}

type skillFile struct {
	path, prefix, source string // prefix names a plugin's skill plugin:name
	mod, size            int64
}

// skillFiles are the SKILL.md files Codex 0.172.0 loads, as measured
// against its skills/list: .agents/skills and .codex/skills in each
// folder from the repo root down to cwd (only cwd outside a repo),
// ~/.agents/skills, $CODEX_HOME/skills, the plugins' in its cache, and
// its own in $CODEX_HOME/skills/.system. It looks in every folder below a
// root, a skill's own included, but none whose name starts with a dot.
func skillFiles(codexHome, home, cwd string) []skillFile {
	type root struct{ dir, prefix, source string }
	var roots []root
	if cwd != "" {
		var dirs []string
		for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
			dirs = append(dirs, d)
			if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
				break
			}
			if filepath.Dir(d) == d {
				dirs = dirs[:1]
				break
			}
		}
		for _, d := range slices.Backward(dirs) {
			roots = append(roots, root{filepath.Join(d, ".agents", "skills"), "", "project"}, root{filepath.Join(d, ".codex", "skills"), "", "project"})
		}
	}
	if home != "" {
		roots = append(roots, root{filepath.Join(home, ".agents", "skills"), "", "user"})
	}
	if codexHome != "" {
		roots = append(roots, root{filepath.Join(codexHome, "skills"), "", "user"})
		markets, _ := filepath.Glob(filepath.Join(codexHome, "plugins", "cache", "*", "*"))
		for _, p := range markets {
			if v := newest(p); v != "" {
				roots = append(roots, root{filepath.Join(v, "skills"), filepath.Base(p) + ":", "plugin"})
			}
		}
		roots = append(roots, root{filepath.Join(codexHome, "skills", ".system"), "", "system"})
	}
	var out []skillFile
	for _, r := range roots {
		filepath.WalkDir(r.dir, func(p string, e fs.DirEntry, err error) error {
			switch {
			case err != nil:
				return nil
			case e.IsDir() && p != r.dir && strings.HasPrefix(e.Name(), "."):
				return filepath.SkipDir
			case e.Name() != "SKILL.md" || !e.Type().IsRegular():
				return nil
			}
			if fi, err := e.Info(); err == nil {
				out = append(out, skillFile{path: p, prefix: r.prefix, source: r.source, mod: fi.ModTime().UnixNano(), size: fi.Size()})
			}
			return nil
		})
	}
	return out
}

// newest is the version folder of a cached plugin written last, "" for none.
func newest(plugin string) string {
	ents, _ := os.ReadDir(plugin)
	var best string
	var at int64
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && e.IsDir() && fi.ModTime().UnixNano() > at {
			best, at = filepath.Join(plugin, e.Name()), fi.ModTime().UnixNano()
		}
	}
	return best
}

// read is f's skill: named by its frontmatter, else its folder; false
// for one it cannot read.
func (f skillFile) read() (session.Command, bool) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return session.Command{}, false
	}
	meta, _ := skillshare.SplitFrontmatter(string(b))
	c := session.Command{Name: filepath.Base(filepath.Dir(f.path)), Source: f.source}
	for _, kv := range meta {
		switch v := strings.TrimSpace(kv.Value); kv.Key {
		case "name":
			c.Name = cmp.Or(v, c.Name)
		case "description":
			c.Description = v
		}
	}
	c.Name = f.prefix + c.Name
	return c, true
}
