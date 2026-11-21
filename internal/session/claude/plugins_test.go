package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// The fixture is what `claude plugin install` wrote for five plugins in a
// temp config dir, and want is what claude -p 2.1.330 then listed in the
// project's folder: a disabled plugin left out, a project's plugin only
// in its own folder, nested commands as plugin:folder:name, a manifest's
// skills folder read, and a skill the settings turn off hidden.
func TestPluginCommands(t *testing.T) {
	dir := t.TempDir()
	conf, proj := filepath.Join(dir, "conf"), filepath.Join(dir, "proj")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	install := func(name string) string { return filepath.Join(conf, "plugins", "cache", "mkt", name, "1.0.0") }
	for _, p := range []string{"demo", "off", "named", "proj"} {
		write(filepath.Join(install(p), ".claude-plugin", "plugin.json"), `{"name":"`+p+`","version":"1.0.0"}`)
		write(filepath.Join(install(p), "skills", p+"-skill", "SKILL.md"), "---\ndescription: skill of "+p+"\n---\n")
		write(filepath.Join(install(p), "commands", p+"-cmd.md"), "---\ndescription: command of "+p+"\n---\n")
		write(filepath.Join(install(p), "commands", "grp", p+"-deep.md"), "---\ndescription: nested command of "+p+"\n---\n")
	}
	write(filepath.Join(install("extra"), ".claude-plugin", "plugin.json"), `{"name":"extra","version":"1.0.0","skills":["./more","../../../../../../outside"]}`)
	write(filepath.Join(install("extra"), "more", "xs", "SKILL.md"), "---\ndescription: manifest skills dir\n---\n")
	write(filepath.Join(dir, "outside", "escaped", "SKILL.md"), "---\ndescription: outside the plugin\n---\n") // a manifest folder outside the plugin is refused
	record := func(scope, name, project string) map[string]string {
		r := map[string]string{"scope": scope, "installPath": install(name), "version": "1.0.0"}
		if project != "" {
			r["projectPath"] = project
		}
		return r
	}
	// The registry's key order is the order claude installed them in.
	reg := `{"version":2,"plugins":{` +
		`"demo@mkt":[` + mustJSON(t, record("user", "demo", "")) + `],` +
		`"off@mkt":[` + mustJSON(t, record("user", "off", "")) + `],` +
		`"named@mkt":[` + mustJSON(t, record("user", "named", "")) + `],` +
		`"extra@mkt":[` + mustJSON(t, record("user", "extra", "")) + `],` +
		`"proj@mkt":[` + mustJSON(t, record("project", "proj", proj)) + `]}}`
	write(filepath.Join(conf, "plugins", "installed_plugins.json"), reg)
	write(filepath.Join(conf, "settings.json"), `{"enabledPlugins":{"demo@mkt":true,"off@mkt":false,"named@mkt":true,"extra@mkt":true},"skillOverrides":{"hideme":"off"}}`)
	write(filepath.Join(proj, ".claude", "settings.json"), `{"enabledPlugins":{"proj@mkt":true}}`)
	write(filepath.Join(conf, "skills", "hideme", "SKILL.md"), "---\ndescription: hidden by override\n---\n")
	write(filepath.Join(conf, "skills", "keepme", "SKILL.md"), "---\ndescription: kept\n---\n")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	os.MkdirAll(filepath.Join(proj, "sub"), 0o755)

	custom := func(cwd string) []string {
		var out []string
		for _, c := range Commands(conf, cwd) {
			if c.Source != "" {
				out = append(out, c.Name)
			}
		}
		return out
	}
	want := []string{"keepme", "demo:demo-cmd", "demo:grp:demo-deep", "named:grp:named-deep", "named:named-cmd",
		"proj:grp:proj-deep", "proj:proj-cmd", "demo:demo-skill", "named:named-skill", "extra:xs", "proj:proj-skill"}
	if got := custom(proj); !slices.Equal(got, want) {
		t.Fatalf("in the project:\n got %q\nwant %q", got, want)
	}
	sub := slices.DeleteFunc(slices.Clone(want), func(n string) bool { return len(n) > 5 && n[:5] == "proj:" })
	if got := custom(filepath.Join(proj, "sub")); !slices.Equal(got, sub) {
		t.Fatalf("in a subfolder, without the project's plugin:\n got %q\nwant %q", got, sub)
	}
	for _, c := range Commands(conf, proj) {
		if c.Name == "demo:demo-skill" && (c.Source != "plugin" || c.Description != "skill of demo") {
			t.Errorf("plugin skill %+v", c)
		}
	}

	// Turning a plugin off moves the stamp, and the list follows.
	stamp := CommandsStamp(conf, proj)
	p := filepath.Join(conf, "settings.json")
	write(p, `{"enabledPlugins":{"demo@mkt":false,"named@mkt":true,"extra@mkt":true}}`)
	os.Chtimes(p, time.Now(), time.Now().Add(time.Hour))
	if CommandsStamp(conf, proj) == stamp {
		t.Fatal("a settings change left the stamp")
	}
	if got := custom(proj); slices.Contains(got, "demo:demo-cmd") || !slices.Contains(got, "hideme") {
		t.Fatalf("after turning demo off and dropping the override: %q", got)
	}
	// Unreadable settings or registry: the plugins go, nothing breaks.
	write(filepath.Join(conf, "plugins", "installed_plugins.json"), "{not json")
	if got := custom(proj); slices.ContainsFunc(got, func(n string) bool { return slices.Contains([]string{"named:named-cmd", "extra:xs"}, n) }) {
		t.Fatalf("with a broken registry: %q", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
