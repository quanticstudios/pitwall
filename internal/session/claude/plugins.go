package claude

// Adapted from T3 Code (MIT): apps/server/src/provider/Drivers/ClaudeSkills.ts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// Plugins: the commands and skills of the plugins Claude Code 2.1.330
// loads for a session, as its -p list showed them for plugins installed
// with `claude plugin install`, as far as that list confirmed it. A plugin is on when the settings
// say true for its id; its record in installed_plugins.json applies when
// it is the user's or managed, or a project's whose folder is the
// session's. Its commands are plugin:name (plugin:folder:name in a
// subfolder), its skills plugin:name, from skills/ and the folders its
// manifest's "skills" names. Plugins synced from claude.ai and folders
// passed with --plugin-dir are not read.

// claudeSettings is what the settings files say about the menu.
type claudeSettings struct {
	plugins map[string]bool // enabledPlugins, by plugin@marketplace
	off     map[string]bool // skills skillOverrides turns off
}

// settingsFiles are the settings files Claude Code reads for cwd under
// conf, lowest precedence first.
func settingsFiles(conf, cwd string) []string {
	var out []string
	if conf != "" {
		out = append(out, filepath.Join(conf, "settings.json"))
	}
	if cwd != "" {
		out = append(out, filepath.Join(cwd, ".claude", "settings.json"), filepath.Join(cwd, ".claude", "settings.local.json"))
		if root := repoRoot(cwd); root != "" && root != filepath.Clean(cwd) {
			out = append(out, filepath.Join(root, ".claude", "settings.local.json"))
		}
	}
	return append(out, managedSettings()...)
}

// managedSettings is where an administrator's settings live.
func managedSettings() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/ClaudeCode/managed-settings.json"}
	case "windows":
		if d := os.Getenv("ProgramData"); d != "" {
			return []string{filepath.Join(d, "ClaudeCode", "managed-settings.json")}
		}
		return nil
	}
	return []string{"/etc/claude-code/managed-settings.json"}
}

// repoRoot is the folder from cwd up that holds .git, "" for none.
func repoRoot(cwd string) string {
	for d := filepath.Clean(cwd); ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// readSettings merges files, a later one's value winning. A file that
// is not JSON is skipped whole; Claude Code also takes comments and
// trailing commas, which this reads as not JSON.
func readSettings(files []string) claudeSettings {
	s := claudeSettings{plugins: map[string]bool{}, off: map[string]bool{}}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var v struct {
			EnabledPlugins map[string]any    `json:"enabledPlugins"`
			SkillOverrides map[string]string `json:"skillOverrides"`
		}
		if json.Unmarshal(b, &v) != nil {
			continue
		}
		for id, on := range v.EnabledPlugins {
			if b, ok := on.(bool); ok {
				s.plugins[id] = b
			} else {
				delete(s.plugins, id) // why: another form replaces a lower file's value without turning the plugin on
			}
		}
		for name, o := range v.SkillOverrides {
			s.off[name] = o == "off"
		}
	}
	return s
}

// pluginFiles are the commands, then the skills, of the plugins s turns
// on for cwd, from conf's installed_plugins.json.
func pluginFiles(conf, cwd string, s claudeSettings) []cmdFile {
	b, err := os.ReadFile(filepath.Join(conf, "plugins", "installed_plugins.json"))
	if conf == "" || err != nil {
		return nil
	}
	var reg struct {
		Plugins json.RawMessage `json:"plugins"`
	}
	var byID map[string][]struct {
		Scope       string `json:"scope"`
		InstallPath string `json:"installPath"`
		ProjectPath string `json:"projectPath"`
	}
	if json.Unmarshal(b, &reg) != nil || json.Unmarshal(reg.Plugins, &byID) != nil {
		return nil
	}
	here := realPath(cwd)
	var cmds, skills []cmdFile
	for _, id := range objectKeys(reg.Plugins) { // why: Claude Code lists plugins in the registry's order
		if !s.plugins[id] {
			continue
		}
		install := ""
		for _, r := range byID[id] {
			applies := r.Scope == "user" || r.Scope == "managed" || (r.Scope == "project" || r.Scope == "local") && here != "" && realPath(r.ProjectPath) == here
			if _, err := os.Stat(r.InstallPath); applies && err == nil && r.InstallPath != "" {
				install = r.InstallPath
				break
			}
		}
		if install == "" {
			continue
		}
		name, dirs := pluginManifest(install)
		if name == "" {
			name, _, _ = strings.Cut(id, "@")
		}
		cmds = commandDir(cmds, filepath.Join(install, "commands"), name+":", "plugin", 0)
		for _, d := range dirs {
			ents, _ := os.ReadDir(d)
			for _, e := range ents {
				p := filepath.Join(d, e.Name(), "SKILL.md")
				if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
					skills = append(skills, cmdFile{name: name + ":" + e.Name(), path: p, source: "plugin", skill: true, mod: fi.ModTime().UnixNano(), size: fi.Size()})
				}
			}
		}
	}
	return append(cmds, skills...)
}

// pluginManifest is the name in install's .claude-plugin/plugin.json and
// its skill folders: skills/, and the ones "skills" names inside install.
func pluginManifest(install string) (name string, dirs []string) {
	dirs = []string{filepath.Join(install, "skills")}
	b, err := os.ReadFile(filepath.Join(install, ".claude-plugin", "plugin.json"))
	if err != nil {
		return "", dirs
	}
	var m struct {
		Name   string          `json:"name"`
		Skills json.RawMessage `json:"skills"`
	}
	if json.Unmarshal(b, &m) != nil {
		return "", dirs
	}
	var list []string
	var one string
	if json.Unmarshal(m.Skills, &one) == nil {
		list = []string{one}
	} else {
		json.Unmarshal(m.Skills, &list)
	}
	base := realPath(install)
	for _, rel := range list {
		d := filepath.Join(install, rel)
		if r := realPath(d); base != "" && r != "" && (r == base || strings.HasPrefix(r, base+string(filepath.Separator))) && !slices.Contains(dirs, d) {
			dirs = append(dirs, d) // why: Claude Code refuses a manifest folder that leaves the plugin, through a link too
		}
	}
	return m.Name, dirs
}

// realPath is p with its links resolved, "" when it does not exist.
func realPath(p string) string {
	if p == "" {
		return ""
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return ""
	}
	return r
}

// objectKeys are the keys of the JSON object raw, in its order.
func objectKeys(raw json.RawMessage) []string {
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for d.More() {
		t, err := d.Token()
		k, ok := t.(string)
		if err != nil || !ok {
			return keys
		}
		keys = append(keys, k)
		var skip json.RawMessage
		if d.Decode(&skip) != nil {
			return keys
		}
	}
	return keys
}
