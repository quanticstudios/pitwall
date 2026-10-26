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

// TestAddHugeOutput: a 1 MB output counts in full toward Size, and only its
// tail is kept.
func TestAddHugeOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	out := "HEAD " + strings.Repeat("é ", 1<<19) + "Error: tests failed"
	l.Add(Event{T: time.Now(), Kind: Command, Command: "go test ./...", Output: out})
	l.Close(time.Second)
	evs, err := Read(path, time.Time{})
	if err != nil || len(evs) != 1 {
		t.Fatalf("read %v, %v", evs, err)
	}
	e := evs[0]
	if want := len("go test ./...") + len([]rune(out)); e.Size != want {
		t.Errorf("size %d, want %d", e.Size, want)
	}
	if n := len([]rune(e.Output)); n != outputLen || !strings.HasSuffix(e.Output, " Error: tests failed") || strings.Contains(e.Output, "HEAD") {
		t.Errorf("output %d runes, ends %q", n, string([]rune(e.Output)[n-30:]))
	}
}

// TestAddCutSecret: a secret or header line that straddles the edge of
// what Add redacts is dropped whole, even when redaction then shrinks the
// rest below the kept length.
func TestAddCutSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "errors.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	key := "ghp_" + strings.Repeat("A", 200)
	// keys is n runes of whole keys that redact to a tenth of their size.
	keys := func(n int) string {
		s := strings.Repeat(" "+key, n/(len(key)+1))
		return strings.Repeat(" ", n-len(s)) + s
	}
	end := " Error: push rejected"
	// Past the 4800-rune edge only the key's last 30 runes remain, too
	// short to match, and the whole keys after them redact to under 600.
	cut := key[len(key)-30:] + " " + keys(8*outputLen-31-len(end)) + end
	l.Add(Event{T: time.Now(), Kind: Command, Command: "git push", Output: strings.Repeat("x", 100) + key[:len(key)-30] + cut})
	// The edge falls in a header line, after its name.
	line := "kie: sess a b c\n"
	l.Add(Event{T: time.Now(), Kind: Command, Command: "curl", Output: strings.Repeat("y\n", 3000) + "Coo" + line + keys(8*outputLen-len(line)-len(end)) + end})
	l.Add(Event{T: time.Now(), Kind: Command, Command: "curl -H 'Cookie: session=" + strings.Repeat("s", 2*commandLen) + " more'", Output: "x"})
	l.Add(Event{T: time.Now(), Kind: Command, Command: "echo", Output: strings.Repeat("z", 9000)})
	// The edge falls in a private key's body.
	body := strings.Repeat("MIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n", 200) + "-----END PRIVATE KEY-----\n" + end
	l.Add(Event{T: time.Now(), Kind: Command, Command: "cat k", Output: "-----BEGIN PRIVATE KEY-----\n" + strings.Repeat("MIIEvQIBADANBgkqhkiG9w0BAQEFAASC\n", 100) + body[len(body)-8*outputLen:]})
	l.Close(time.Second)
	evs, err := Read(path, time.Time{})
	if err != nil || len(evs) != 5 {
		t.Fatalf("read %v, %v", evs, err)
	}
	for _, e := range evs[:2] {
		if strings.Contains(e.Output, "AAAAAAAAAA") || strings.Contains(e.Output, "sess") || !strings.HasSuffix(e.Output, end) || !strings.Contains(e.Output, "[redacted]") {
			t.Errorf("cut secret kept: %q", e.Output)
		}
	}
	if e := evs[2]; strings.Contains(e.Command, "sss") || e.Command != "curl -H 'Cookie:" {
		t.Errorf("command cut mid-word: %q", e.Command)
	}
	if e := evs[3]; e.Output != "" || e.Size != 9004 {
		t.Errorf("one word past the edge: %+v", e)
	}
	if e := evs[4]; e.Output != strings.TrimSpace(end) {
		t.Errorf("a cut key block kept: %q", e.Output)
	}
}

func TestDays(t *testing.T) {
	loc := time.FixedZone("x", 2*3600)
	from := time.Date(2026, 10, 1, 9, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 2)
	evs := []Event{
		{T: from.Add(-time.Hour), Kind: Command, Size: 400},                // before the window
		{T: from.Add(time.Hour), Kind: Command, Size: 400},                 // Oct 1
		{T: time.Date(2026, 10, 1, 23, 30, 0, 0, time.UTC), Kind: API},     // Oct 2 in loc
		{T: time.Date(2026, 10, 2, 12, 0, 0, 0, loc), Kind: Tool, Size: 8}, // Oct 2
	}
	ds := Days(evs, from, to, loc)
	if len(ds) != 3 || !ds[0].Date.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, loc)) {
		t.Fatalf("days = %+v", ds)
	}
	if ds[0].Count != 1 || ds[0].Kinds[Command] != 1 || ds[0].Tokens != 100 {
		t.Errorf("Oct 1 = %+v", ds[0])
	}
	if ds[1].Count != 2 || ds[1].Kinds[API] != 1 || ds[1].Kinds[Tool] != 1 || ds[1].Tokens != 2 {
		t.Errorf("Oct 2 = %+v", ds[1])
	}
	if ds[2].Count != 0 {
		t.Errorf("Oct 3 = %+v", ds[2])
	}
}

func TestTop(t *testing.T) {
	gs := []Group{{Sig: "a", Count: 5, Tokens: 50}, {Sig: "b", Count: 3, Tokens: 30}, {Sig: "c", Count: 2, Tokens: 20}, {Sig: "d", Count: 1, Tokens: 10}}
	top, rest := Top(gs, 2)
	if len(top) != 2 || top[1].Sig != "b" || rest.Sig != "" || rest.Count != 3 || rest.Tokens != 30 {
		t.Errorf("Top(2) = %+v, %+v", top, rest)
	}
	if top, rest := Top(gs, 9); len(top) != 4 || rest.Count != 0 {
		t.Errorf("Top(9) = %+v, %+v", top, rest)
	}
}
