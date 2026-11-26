package codex

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// Codex 0.171.0's menu also takes /quit for /exit and /btw for /side,
// and lists /apps only for an account with apps; names stay unique
// across names and aliases, so a typed alias finds one command.
func TestBuiltins(t *testing.T) {
	find := func(name string) (session.Command, bool) {
		i := slices.IndexFunc(Builtins, func(c session.Command) bool { return c.Is(name) })
		if i < 0 {
			return session.Command{}, false
		}
		return Builtins[i], true
	}
	for alias, name := range map[string]string{"quit": "exit", "btw": "side"} {
		if c, ok := find(alias); !ok || c.Name != name {
			t.Errorf("/%s finds %+v, want /%s", alias, c, name)
		}
	}
	if c, ok := find("apps"); !ok || !c.Terminal {
		t.Errorf("/apps %+v, want a terminal command", c)
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

// The fixture is the layout Codex 0.172.0's skills/list was measured on,
// under a temp HOME and CODEX_HOME: it listed the project's .agents and
// .codex skills from the repo root down, ~/.agents/skills, every folder
// under $CODEX_HOME/skills, a skill inside another included, a cached
// plugin's as plugin:name and its own under .system, sorted by name
// within project, user and plugin, then system. It skipped a lowercase
// skill.md, a dot folder and a skill above the repo root; outside a repo
// only cwd's.
func TestSkills(t *testing.T) {
	dir := t.TempDir()
	home, cx, proj := filepath.Join(dir, "home"), filepath.Join(dir, "home", ".codex"), filepath.Join(dir, "proj")
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
		write(filepath.Join(root, name, "SKILL.md"), "---\nname: "+name+"\ndescription: about "+name+"\n---\nx\n")
	}
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	skill(filepath.Join(proj, ".agents/skills"), "p-repo")
	skill(filepath.Join(proj, ".codex/skills"), "p-dotcodex")
	skill(filepath.Join(proj, "sub/.agents/skills"), "p-sub")
	skill(filepath.Join(dir, ".agents/skills"), "above")
	skill(filepath.Join(home, ".agents/skills"), "u-agents")
	skill(filepath.Join(cx, "skills"), "u-home")
	skill(filepath.Join(cx, "skills/group"), "u-nested")
	skill(filepath.Join(cx, "skills/u-home"), "u-inner")
	skill(filepath.Join(cx, "skills/.hidden"), "hidden")
	write(filepath.Join(cx, "skills/noname/SKILL.md"), "---\ndescription: no name\n---\n")
	write(filepath.Join(cx, "skills/lower/skill.md"), "---\nname: lower\n---\n")
	skill(filepath.Join(cx, "skills/.system"), "skill-creator")
	skill(filepath.Join(cx, "plugins/cache/market/pages/0.1.26/skills"), "write-page")

	names := func(cs []session.Command) []string {
		var out []string
		for _, c := range cs {
			out = append(out, c.Name+"="+c.Source)
		}
		return out
	}
	got := Skills(cx, home, filepath.Join(proj, "sub"))
	want := []string{"p-dotcodex=project", "p-repo=project", "p-sub=project",
		"noname=user", "pages:write-page=plugin", "u-agents=user", "u-home=user", "u-inner=user", "u-nested=user",
		"skill-creator=system"}
	if !slices.Equal(names(got), want) {
		t.Fatalf("skills\n got %q\nwant %q", names(got), want)
	}
	if got[0].Description != "about p-dotcodex" {
		t.Errorf("description %+v", got[0])
	}
	if c := Commands(cx, home, proj); len(c) != len(Builtins)+9 || c[0].Name != Builtins[0].Name {
		t.Errorf("Commands: %d, want the built-ins then 9 skills", len(c))
	}

	nogit := filepath.Join(dir, "nogit")
	skill(filepath.Join(nogit, ".agents/skills"), "top")
	skill(filepath.Join(nogit, "a/.agents/skills"), "here")
	if got := names(Skills("", "", filepath.Join(nogit, "a"))); !slices.Equal(got, []string{"here=project"}) {
		t.Errorf("outside a repo: %q", got)
	}

	stamp := CommandsStamp(cx, home, proj)
	skill(filepath.Join(cx, "skills"), "added")
	if CommandsStamp(cx, home, proj) == stamp {
		t.Error("the stamp did not change with a new skill")
	}
}
