package antigravity

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// The fixture is the one agy acp 1.5.13 listed, under a temp HOME, as
// u-acli, u-conf, u-gem, p-agent, p-agents and p-sub among its own, plus
// ~/.agents/skills, which agy 1.5.18 reads too, after ~/.gemini's: it
// skipped ~/.gemini/antigravity/skills, XDG's,
// .gemini/skills, .antigravity/skills and bare skills folders in the
// project, workflows and .agents/commands.
func TestCommands(t *testing.T) {
	dir := t.TempDir()
	home, proj := filepath.Join(dir, "home"), filepath.Join(dir, "proj")
	skill := func(root, name, desc string) {
		t.Helper()
		p := filepath.Join(root, name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("---\nname: "+name+"\ndescription: "+desc+"\n---\nx\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	skill(filepath.Join(home, ".gemini/antigravity-cli/skills"), "u-acli", "from acli")
	skill(filepath.Join(home, ".gemini/config/skills"), "u-conf", "from conf")
	skill(filepath.Join(home, ".gemini/skills"), "u-gem", "from gem")
	skill(filepath.Join(home, ".agents/skills"), "u-agents", "from shared")
	skill(filepath.Join(home, ".agents/skills"), "u-conf", "loses to ~/.gemini")
	skill(filepath.Join(home, ".gemini/antigravity/skills"), "u-ag", "skipped")
	skill(filepath.Join(proj, ".agents/skills"), "p-agents", "from agents")
	skill(filepath.Join(proj, ".agent/skills"), "p-agent", "from agent")
	skill(filepath.Join(proj, ".gemini/skills"), "p-gem", "skipped")
	skill(filepath.Join(proj, "sub/.agents/skills"), "p-sub", "from sub")
	skill(filepath.Join(proj, ".agents/skills"), "compact", "a skill named as a built-in")
	skill(filepath.Join(dir, ".agents/skills"), "above", "above the repo root")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	hidden := filepath.Join(proj, ".agents/skills/hidden/SKILL.md")
	os.MkdirAll(filepath.Dir(hidden), 0o755)
	os.WriteFile(hidden, []byte("---\nname: hidden\nuser-invocable: false\n---\n"), 0o600)
	renamed := filepath.Join(proj, ".agents/skills/folder_name/SKILL.md")
	os.MkdirAll(filepath.Dir(renamed), 0o755)
	os.WriteFile(renamed, []byte("---\nname: front-name\ndescription: named by its frontmatter\n---\n"), 0o600)
	cwd := filepath.Join(proj, "sub")

	got := Commands(home, cwd)
	var custom []string
	for _, c := range got {
		if c.Source != "" {
			custom = append(custom, c.Name+"="+c.Source)
		}
	}
	want := []string{"front-name=project", "p-agent=project", "p-agents=project", "p-sub=project", "u-acli=user", "u-agents=user", "u-conf=user", "u-gem=user"}
	if !slices.Equal(custom, want) {
		t.Fatalf("skills %q, want %q", custom, want)
	}
	if len(got) != len(Builtins)+len(want) {
		t.Fatalf("%d commands, want %d", len(got), len(Builtins)+len(want))
	}
	if i := slices.IndexFunc(got, func(c session.Command) bool { return c.Name == "u-conf" }); got[i].Description != "from conf" {
		t.Errorf("u-conf = %q, want ~/.gemini's over ~/.agents/skills'", got[i].Description)
	}
	if !slices.IsSortedFunc(got, func(a, b session.Command) int { return strings.Compare(a.Name, b.Name) }) {
		t.Error("not sorted by name, as agy's menu is")
	}
	for _, c := range got {
		if c.Name == "compact" && c.Source != "" || c.Description == "" {
			t.Errorf("command %+v", c)
		}
	}

	stamp := CommandsStamp(home, cwd)
	p := filepath.Join(home, ".gemini/skills/u-gem/SKILL.md")
	os.WriteFile(p, []byte("---\nname: u-gem\ndescription: edited, longer\n---\n"), 0o600)
	os.Chtimes(p, time.Now(), time.Now().Add(time.Hour))
	if CommandsStamp(home, cwd) == stamp {
		t.Fatal("an edit left the stamp")
	}
	if got := Commands("", ""); len(got) != len(Builtins) {
		t.Fatalf("no dirs: %d commands", len(got))
	}
}

// The second probe of agy acp 1.5.13: a skill.md spelled in lower case,
// plugins' skills from the user's two plugin folders and the project's,
// each plugin needing its plugin.json, one turned off in config.json;
// and what it left out: .gemini/skills in the project, a skill nested a
// folder deeper, and a skills.json entry.
func TestCommandsPlugins(t *testing.T) {
	dir := t.TempDir()
	home, proj := filepath.Join(dir, "home"), filepath.Join(dir, "proj")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	skill := func(root, name string) {
		write(filepath.Join(root, name, "SKILL.md"), "---\nname: "+name+"\ndescription: "+name+"\n---\n")
	}
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	write(filepath.Join(home, ".gemini/antigravity-cli/skills/u-cap/skill.md"), "---\nname: u-cap\ndescription: capital file\n---\n")
	for _, p := range []struct{ root, name string }{
		{filepath.Join(home, ".gemini/config/plugins"), "plugon"},
		{filepath.Join(home, ".gemini/config/plugins"), "plugoff"},
		{filepath.Join(home, ".gemini/antigravity-cli/plugins"), "plugcli"},
		{filepath.Join(proj, ".agents/plugins"), "plugproj"},
	} {
		write(filepath.Join(p.root, p.name, "plugin.json"), `{"name":"`+p.name+`"}`)
		skill(filepath.Join(p.root, p.name, "skills"), "s-"+p.name)
	}
	skill(filepath.Join(home, ".gemini/config/plugins/nomanifest/skills"), "s-nomanifest")
	write(filepath.Join(home, ".gemini/config/config.json"), `{"plugins":{"plugoff":{"enabled":false},"plugon":{"enabled":true}}}`)
	skill(filepath.Join(proj, ".gemini/skills"), "g-proj")
	skill(filepath.Join(proj, ".agents/skills/group"), "n-nested")
	skill(filepath.Join(dir, "extra"), "x-extra")
	write(filepath.Join(proj, ".agents/skills.json"), `{"entries":[{"path":"`+filepath.Join(dir, "extra")+`"}]}`)

	var custom []string
	for _, c := range Commands(home, proj) {
		if c.Source != "" {
			custom = append(custom, c.Name+"="+c.Source)
		}
	}
	want := []string{"s-plugcli=plugin", "s-plugon=plugin", "s-plugproj=plugin", "u-cap=user"}
	if !slices.Equal(custom, want) {
		t.Fatalf("got %q, want %q", custom, want)
	}
	stamp := CommandsStamp(home, proj)
	p := filepath.Join(home, ".gemini/config/config.json")
	write(p, `{"plugins":{"plugon":{"enabled":false}}}`)
	os.Chtimes(p, time.Now(), time.Now().Add(time.Hour))
	if CommandsStamp(home, proj) == stamp {
		t.Fatal("turning a plugin off left the stamp")
	}
}

// agy 1.5.14's menu takes the aliases it shows in parentheses, lists
// /remote-control for an account that has it, and added /projects,
// /record and /thinking; it no longer takes /plugins. Names stay unique
// across names and aliases, so a typed alias finds one command.
func TestBuiltins(t *testing.T) {
	find := func(name string) (session.Command, bool) {
		i := slices.IndexFunc(Builtins, func(c session.Command) bool { return c.Is(name) })
		if i < 0 {
			return session.Command{}, false
		}
		return Builtins[i], true
	}
	for alias, name := range map[string]string{"new": "clear", "cs": "codesearch", "search": "codesearch", "settings": "config",
		"quit": "exit", "branch": "fork", "switch": "resume", "conversation": "resume", "undo": "rewind", "quota": "usage"} {
		if c, ok := find(alias); !ok || c.Name != name {
			t.Errorf("/%s finds %+v, want /%s", alias, c, name)
		}
	}
	for name, term := range map[string]bool{"remote-control": false, "projects": true, "record": true, "thinking": false} {
		if c, ok := find(name); !ok || c.Terminal != term {
			t.Errorf("/%s %+v, want Terminal %v", name, c, term)
		}
	}
	if _, ok := find("plugins"); ok {
		t.Error("/plugins listed, which agy 1.5.14 calls an unknown command")
	}
	seen := map[string]bool{}
	for _, c := range Builtins {
		for _, n := range append([]string{c.Name}, c.Aliases...) {
			if seen[n] {
				t.Errorf("/%s listed twice", n)
			}
			seen[n] = true
		}
	}
}
