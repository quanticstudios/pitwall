package config

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Profile is one [profiles.<name>] account. Empty directories leave that
// agent's environment alone. A shell gets both overrides for agents it starts.
type Profile struct {
	Label           string `toml:"label" doc:"Account label in profile pickers and tab hover cards; defaults to the profile name"`
	Color           Color  `toml:"color" doc:"Small mark on the tab row, #rrggbb or #rrggbbaa; defaults to the accent color"`
	ClaudeConfigDir string `toml:"claude_config_dir" doc:"CLAUDE_CONFIG_DIR for this account, an absolute path or ~/path"`
	CodexHome       string `toml:"codex_home" doc:"CODEX_HOME for this account, an absolute path or ~/path"`
}

func resolveProfiles(in map[string]Profile) (map[string]Profile, []issue) {
	out := map[string]Profile{}
	var issues []issue
	home, _ := os.UserHomeDir()
	for _, name := range slices.Sorted(mapKeys(in)) {
		p := in[name]
		path := "profiles." + name
		if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "\x00\r\n=") {
			issues = append(issues, issue{path, "a profile needs a nonempty name without control characters or ="})
			continue
		}
		valid := true
		for _, d := range []struct {
			key string
			dir *string
		}{{"claude_config_dir", &p.ClaudeConfigDir}, {"codex_home", &p.CodexHome}} {
			if strings.HasPrefix(*d.dir, "~/") {
				*d.dir = filepath.Join(home, (*d.dir)[2:])
			}
			if *d.dir != "" && (!filepath.IsAbs(*d.dir) || strings.ContainsAny(*d.dir, "\x00\r\n")) {
				issues = append(issues, issue{path + "." + d.key, "want an absolute directory or ~/path"})
				valid = false
			}
		}
		if p.Color != "" && !hexRe.MatchString(string(p.Color)) {
			issues = append(issues, issue{path + ".color", "want a #rrggbb or #rrggbbaa color"})
			p.Color = ""
		}
		if valid {
			out[name] = p
		}
	}
	return out, issues
}

// ProfileLabel returns the configured label or the saved profile name,
// including when a profile has since been removed from config.toml.
func (s Settings) ProfileLabel(name string) string { return cmp.Or(s.Profiles[name].Label, name) }

// ProfileEnv adds a profile's overrides after preset variables. With no
// profile the original variables are returned unchanged. Save the result
// with the pane so a restart keeps its directories even after config edits.
func (s Settings) ProfileEnv(name string, env []string) ([]string, error) {
	if name == "" {
		return env, nil
	}
	p, ok := s.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("no account profile %q", name)
	}
	out := slices.Clone(env)
	for _, e := range []struct{ key, val string }{{"CLAUDE_CONFIG_DIR", p.ClaudeConfigDir}, {"CODEX_HOME", p.CodexHome}, {"PITWALL_PROFILE", name}} {
		if e.val == "" {
			continue
		}
		out = slices.DeleteFunc(out, func(v string) bool { k, _, _ := strings.Cut(v, "="); return k == e.key })
		out = append(out, e.key+"="+e.val)
	}
	return out, nil
}
