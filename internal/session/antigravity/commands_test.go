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
// u-acli, u-conf, u-gem, p-agent, p-agents and p-sub among its own: it
// skipped ~/.agents/skills, ~/.gemini/antigravity/skills, XDG's,
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
	skill(filepath.Join(home, ".agents/skills"), "u-agents", "skipped")
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
	want := []string{"front-name=project", "p-agent=project", "p-agents=project", "p-sub=project", "u-acli=user", "u-conf=user", "u-gem=user"}
	if !slices.Equal(custom, want) {
		t.Fatalf("skills %q, want %q", custom, want)
	}
	if len(got) != len(Builtins)+len(want) {
		t.Fatalf("%d commands, want %d", len(got), len(Builtins)+len(want))
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
