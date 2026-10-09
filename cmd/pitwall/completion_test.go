package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// mainCommands is every command main's switch dispatches, read from
// main.go.
func mainCommands(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			continue
		}
		for _, s := range fn.Body.List {
			sw, ok := s.(*ast.SwitchStmt)
			if !ok {
				continue
			}
			for _, c := range sw.Body.List {
				for _, e := range c.(*ast.CaseClause).List {
					if lit, ok := e.(*ast.BasicLit); ok {
						s, _ := strconv.Unquote(lit.Value)
						out = append(out, s)
					}
				}
			}
		}
	}
	return out
}

// Every command main runs, but the internal ones and version's aliases,
// is completed in each shell, with its subcommands, and each script
// parses in its shell when that is installed.
func TestCompletion(t *testing.T) {
	top, subs := commands()
	cmds := mainCommands(t)
	if len(cmds) < 20 {
		t.Fatalf("read %d commands from main.go: %q", len(cmds), cmds)
	}
	for _, c := range cmds {
		switch c {
		case "", "-v", "version", "remote-start": // the bare window, aliases of --version, and an internal one
			continue
		}
		if !slices.Contains(top, c) {
			t.Errorf("%q is not completed (is it in usage?)", c)
		}
	}
	for name, want := range map[string][]string{
		"session": {"ls", "new", "attach", "rename", "kill"}, "worktree": {"ls", "new", "rm", "prune"},
		"tab": {"new", "rename", "close"}, "hooks": {"install", "uninstall"}, "completion": {"bash", "zsh", "fish"},
		"config": {"path", "default", "init", "check", "schema"}, "remote": {"pair", "devices", "revoke"},
		"jev": {"login", "status", "logout", "report"},
	} {
		if !slices.Equal(subs[name], want) {
			t.Errorf("%s completes %q, want %q", name, subs[name], want)
		}
	}
	if subs["hook"] != nil || subs["-s"] != nil {
		t.Errorf("prose read as subcommands: hook %q, -s %q", subs["hook"], subs["-s"])
	}
	for shell, check := range map[string][]string{"bash": {"bash", "--norc", "-n"}, "zsh": {"zsh", "-f", "-n"}, "fish": {"fish", "--no-config", "-n"}} {
		s, err := completion(shell)
		if err != nil {
			t.Fatal(err)
		}
		words := strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(" \t\n'\"()=;", r) })
		for _, c := range top {
			if !slices.Contains(words, c) {
				t.Errorf("%s: no %q", shell, c)
			}
		}
		for name, ss := range subs {
			for _, c := range ss {
				if !slices.Contains(words, c) {
					t.Errorf("%s: no %s %q", shell, name, c)
				}
			}
		}
		if _, err := exec.LookPath(check[0]); err != nil {
			continue
		}
		cmd := exec.Command(check[0], check[1:]...)
		cmd.Stdin = strings.NewReader(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s does not parse its script: %v\n%s\n%s", shell, err, out, s)
		}
	}
	if _, err := completion("tcsh"); err == nil {
		t.Error("tcsh has a script")
	}
}
