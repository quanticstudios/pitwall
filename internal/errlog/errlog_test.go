package errlog

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSignature(t *testing.T) {
	for _, tc := range []struct{ out, want string }{
		{"zsh: no matches found: --include=*.go", "zsh: no matches found: <glob>"},
		{"zsh:1: command not found: rg", "zsh: command not found: rg"},
		{"src/server.ts(12,5): error TS2304: Cannot find name 'Requst'.", "<path>(N,N): error TS2304: Cannot find name <str>."},
		{"internal/a/b.go:12:3: undefined: Foo", "<path>:N:N: undefined: Foo"},
		{"Error: Cannot find module 'express'\nRequire stack:\n- /home/u/repo/index.js", "Error: Cannot find module <str>"},
		{"ls: cannot access '/tmp/x': No such file or directory", "ls: cannot access <str>: No such file or directory"},
		{"cat: /home/u/repo/missing.txt: No such file or directory", "cat: <path>: No such file or directory"},
		{"grep: unrecognized option '--foo'\nUsage: grep [OPTION]... PATTERNS [FILE]...", "grep: unrecognized option <str>"},
		{"fatal: bad object 3f9c2e1ab4d5", "fatal: bad object <hex>"},
		{"line 1\nline 2\nexited after 30s", "exited after Ns"},
		{"bash: /usr/local/bin/tool: Permission denied", "bash: <path>: Permission denied"},
		{"", "failed with no output"},
	} {
		if got := Signature(Event{Kind: Command, Output: tc.out}); got != tc.want {
			t.Errorf("Signature(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
	if got := Signature(Event{Kind: API}); got != "API error" {
		t.Errorf("empty api = %q", got)
	}
	if got := Signature(Event{Kind: Tool, Tool: "Edit"}); got != "Edit failed" {
		t.Errorf("empty tool = %q", got)
	}
	if got := Signature(Event{Kind: Command, Command: "/usr/bin/grep -q x f", Quiet: true, Exit: 1}); got != "Exit 1 with no output: grep" {
		t.Errorf("quiet = %q", got)
	}
	if got := Signature(Event{Kind: Command, Command: "sleep 99", Output: "Error: killed", Interrupted: true}); got != "Interrupted by you: sleep" {
		t.Errorf("interrupted = %q", got)
	}
}

func TestGroups(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	evs := []Event{
		{T: t0, Provider: "claude", Kind: Command, Command: "grep -rn a --include=*.go .", Output: "zsh: no matches found: --include=*.go"},
		{T: t0.Add(time.Minute), Provider: "codex", Kind: Command, Command: "cat /a/b", Output: "cat: /a/b: No such file or directory"},
		{T: t0.Add(2 * time.Minute), Provider: "codex", Kind: Command, Command: "grep x --include=*.ts", Output: "zsh: no matches found: --include=*.ts"},
		{T: t0.Add(3 * time.Minute), Provider: "claude", Kind: Command, Command: "grep y --include=*.md", Output: "zsh: no matches found: --include=*.md"},
	}
	gs := Groups(evs)
	if len(gs) != 2 {
		t.Fatalf("groups = %+v", gs)
	}
	g := gs[0]
	if g.Sig != "zsh: no matches found: <glob>" || g.Count != 3 || strings.Join(g.Providers, ",") != "claude,codex" ||
		!g.Last.Equal(t0.Add(3*time.Minute)) || g.Example.Command != "grep y --include=*.md" {
		t.Errorf("first group = %+v", g)
	}
	if gs[1].Count != 1 {
		t.Errorf("second group = %+v", gs[1])
	}
	big := []Group{{Sig: "a", Count: 5, Tokens: 10}, {Sig: "b", Count: 1, Tokens: 900}}
	if by := ByTokens(big); by[0].Sig != "b" || big[0].Sig != "a" {
		t.Errorf("ByTokens = %+v, input now %+v", by, big)
	}
	if r := Report(big, 15, "all time"); !strings.Contains(r, "Costliest: 2 (900 tokens), 1 (10 tokens).") {
		t.Errorf("costliest line missing:\n%s", r)
	}
	r := Report(gs, 1, "the last 7 days")
	for _, want := range []string{"4 errors", "## 1. zsh: no matches found: <glob>", "×3, about ", "tokens, claude, codex", "grep y --include=*.md"} {
		if !strings.Contains(r, want) {
			t.Errorf("report lacks %q:\n%s", want, r)
		}
	}
	if strings.Contains(r, "## 2.") {
		t.Errorf("report past n:\n%s", r)
	}
}

// TestAddRedactsAndCuts: the token never reaches the file, and a long
// output keeps its end.
func TestAddRedactsAndCuts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Repeat("x", 2000) + "\nError: denied for KNOWNKEY"
	l.Add(Event{T: time.Now(), Kind: Command, Command: "curl --token abcdefghijklmnop1234 " + strings.Repeat("a", 500), Output: out}, "KNOWNKEY")
	l.Close(time.Second)
	evs, err := Read(path, time.Time{})
	if err != nil || len(evs) != 1 {
		t.Fatalf("read %v, %v", evs, err)
	}
	e := evs[0]
	if strings.Contains(e.Command, "abcdefghijklmnop1234") || strings.Contains(e.Output, "KNOWNKEY") {
		t.Errorf("secret kept: %+v", e)
	}
	if n := len([]rune(e.Command)); n != commandLen {
		t.Errorf("command %d runes", n)
	}
	if e.Size < 2500 || e.Tokens() != e.Size/4 {
		t.Errorf("size %d counts the cut text, not the whole", e.Size)
	}
	if n := len([]rune(e.Output)); n != outputLen || !strings.HasSuffix(e.Output, "Error: denied for [redacted]") {
		t.Errorf("output %d runes, ends %q", n, e.Output[len(e.Output)-40:])
	}
}
