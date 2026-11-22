package claude

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// The fixture is the one claude -p 2.1.330 listed as "myskill",
// "projskill", "hello", "ns:deep", "projcmd", "subcmd" ahead of its
// built-ins, run from proj/sub/deeper: a hidden skill left out, a
// subfolder's command named folder:name, the .claude folders from the
// repo root down.
func TestCommands(t *testing.T) {
	dir := t.TempDir()
	conf, proj := filepath.Join(dir, "conf"), filepath.Join(dir, "proj")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(conf, "commands", "hello.md"), "---\ndescription: Say hello\nargument-hint: [name]\n---\nhi\n")
	write(filepath.Join(conf, "commands", "ns", "deep.md"), "# Deep command body first line\n")
	write(filepath.Join(conf, "commands", "compact.md"), "---\ndescription: My compact\n---\n")
	write(filepath.Join(conf, "skills", "myskill", "SKILL.md"), "---\nname: myskill\ndescription: >-\n  My skill\n  does things\n---\nx\n")
	write(filepath.Join(conf, "skills", "hidden", "SKILL.md"), "---\nname: hidden\ndescription: no\nuser-invocable: false\n---\n")
	write(filepath.Join(proj, ".git", "HEAD"), "ref: refs/heads/main\n")
	write(filepath.Join(proj, ".claude", "commands", "projcmd.md"), "---\ndescription: Project command\n---\n")
	write(filepath.Join(proj, ".claude", "skills", "projskill", "SKILL.md"), "---\ndescription: Project skill\n---\n")
	write(filepath.Join(proj, "sub", ".claude", "commands", "subcmd.md"), "---\ndescription: Sub command\n---\n")
	write(filepath.Join(dir, ".claude", "commands", "outside.md"), "above the repo root\n")
	cwd := filepath.Join(proj, "sub", "deeper")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	got := Commands(conf, cwd)
	want := []session.Command{
		{Name: "myskill", Description: "My skill does things", Source: "user"},
		{Name: "projskill", Description: "Project skill", Source: "project"},
		{Name: "compact", Description: "My compact", Source: "user"},
		{Name: "hello", Description: "Say hello", Hint: "[name]", Source: "user"},
		{Name: "ns:deep", Description: "Deep command body first line", Source: "user"},
		{Name: "projcmd", Description: "Project command", Source: "project"},
		{Name: "subcmd", Description: "Sub command", Source: "project"},
	}
	if len(got) != len(want)+len(Builtins)-1 { // the user's compact replaces the built-in
		t.Fatalf("%d commands, want %d", len(got), len(want)+len(Builtins)-1)
	}
	for i, w := range want {
		if !got[i].Equal(w) {
			t.Errorf("command %d = %+v, want %+v", i, got[i], w)
		}
	}
	for _, c := range got[len(want):] {
		if c.Source != "" || c.Description == "" || c.Name == "compact" {
			t.Errorf("built-in %+v", c)
		}
		if c.Name == "config" && !c.Terminal || c.Name == "init" && c.Terminal {
			t.Errorf("terminal flag of %+v", c)
		}
	}

	stamp := CommandsStamp(conf, cwd)
	if CommandsStamp(conf, cwd) != stamp {
		t.Fatal("the stamp moved with nothing changed")
	}
	p := filepath.Join(proj, ".claude", "commands", "projcmd.md")
	write(p, "---\ndescription: Project command, edited\n---\n")
	os.Chtimes(p, time.Now(), time.Now().Add(time.Hour))
	if CommandsStamp(conf, cwd) == stamp {
		t.Fatal("an edit left the stamp")
	}
	if got := Commands("", ""); len(got) != len(Builtins) {
		t.Fatalf("no dirs: %d commands", len(got))
	}
}

// Skill folders linked in, as skillshare installs them, count; a broken
// link, a folder without SKILL.md and an empty SKILL.md do not break the
// list. CRLF files and quoted values read as any other.
func TestCommandsFiles(t *testing.T) {
	dir := t.TempDir()
	conf, store := filepath.Join(dir, "conf"), filepath.Join(dir, "store")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(store, "linked", "SKILL.md"), "---\ndescription: from a linked folder\n---\n")
	os.MkdirAll(filepath.Join(conf, "skills"), 0o755)
	if err := os.Symlink(filepath.Join(store, "linked"), filepath.Join(conf, "skills", "linked")); err != nil {
		t.Skip("no symlinks:", err)
	}
	os.Symlink(filepath.Join(store, "gone"), filepath.Join(conf, "skills", "broken"))
	os.MkdirAll(filepath.Join(conf, "skills", "noskill"), 0o755)
	write(filepath.Join(conf, "skills", "empty", "SKILL.md"), "")
	write(filepath.Join(conf, "skills", "crlf", "SKILL.md"), "---\r\ndescription: \"Quoted, with CRLF\"\r\nargument-hint: '<file>'\r\n---\r\nbody\r\n")
	write(filepath.Join(conf, "commands", "plain.md"), "\n\n# Heading as the first line\nmore\n")

	byName := map[string]session.Command{}
	for _, c := range Commands(conf, "") {
		if c.Source != "" {
			byName[c.Name] = c
		}
	}
	if len(byName) != 4 {
		t.Fatalf("custom commands %v, want crlf, empty, linked and plain", byName)
	}
	if c := byName["linked"]; c.Description != "from a linked folder" {
		t.Errorf("linked %+v", c)
	}
	if c := byName["crlf"]; c.Description != "Quoted, with CRLF" || c.Hint != "<file>" {
		t.Errorf("crlf %+v", c)
	}
	if c := byName["empty"]; c.Description != "" {
		t.Errorf("empty %+v", c)
	}
	if c := byName["plain"]; c.Description != "Heading as the first line" {
		t.Errorf("plain %+v", c)
	}
}

func TestBuiltinsRemoteControlAlias(t *testing.T) {
	i := slices.IndexFunc(Builtins, func(c session.Command) bool { return c.Name == "remote-control" })
	if i < 0 || !slices.Contains(Builtins[i].Aliases, "rc") {
		t.Fatal("/remote-control with alias rc missing from Builtins")
	}
}
