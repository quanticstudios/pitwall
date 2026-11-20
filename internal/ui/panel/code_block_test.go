package panel

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestRunnable(t *testing.T) {
	for _, c := range []struct{ lang, code, want string }{
		{"bash", "# build\ngo build ./...\n\ngo test ./...", "go build ./...\ngo test ./..."},
		{"console", "$ make test\nok  pitwall 1.2s\n$ git push \\\n  origin main\nEverything up-to-date", "make test\ngit push \\\norigin main"},
		{"", "$ ls -la", "ls -la"},
		{"sh {title=x}", "echo hi", "echo hi"},
		{"", "ls -la", ""},
		{"python", "$ not shell", ""},
		{"go", "fmt.Println()", ""},
		{"bash", "# only a comment", ""},
	} {
		if got := runnable(c.lang, c.code); got != c.want {
			t.Errorf("runnable(%q, %q) = %q, want %q", c.lang, c.code, got, c.want)
		}
	}
}

func TestRunOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"cd web\nnpm i":                           "cd web; npm i",
		"git push \\\norigin main":                "git push origin main",
		"for f in *.go; do\ngofmt -l $f\ndone":    "for f in *.go; do gofmt -l $f; done",
		"cat x |\ngrep y":                         "cat x | grep y",
		"if true; then\necho a\nelse\necho b\nfi": "if true; then echo a; else echo b; fi",
		"single": "single",
	} {
		if got := OneLine(in); got != want {
			t.Errorf("OneLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func codeReply(text string) *session.Snapshot {
	return &session.Snapshot{SessionID: "s", SessionState: session.StateReady, Content: []session.Item{
		{ID: "p", Kind: session.KindUserPrompt, Text: "show"},
		{ID: "a", Kind: session.KindAssistantText, Text: text},
	}}
}

// TestCodeBlockFold: a block over foldAt lines shows its first ones
// until Show all opens it, Copy takes all of it, and the tokens come
// from one lex however many frames draw it.
func TestCodeBlockFold(t *testing.T) {
	var b strings.Builder
	b.WriteString("```go\n")
	for i := range 45 {
		fmt.Fprintf(&b, "x%d := f(%d) // c\n", i, i)
	}
	b.WriteString("```\n")
	s := codeReply(b.String())
	th := theme.Dark()
	var tr Transcript
	var ops op.Ops
	now := time.Now()
	transcriptFrame(&tr, th, s, &ops, now)
	transcriptFrame(&tr, th, s, &ops, now)
	key := "a/0"
	sl := tr.sels[key]
	if sl == nil {
		t.Fatalf("no code block drawn; sels %v", keys(tr.sels))
	}
	if n := strings.Count(sl.Text(), "\n") + 1; n != foldAt {
		t.Fatalf("folded block shows %d lines, want %d", n, foldAt)
	}
	h := tr.docs["a"].code[key]
	if len(sl.Colors) == 0 || len(sl.Styles) == 0 || sl.Styles[0].Font.Style != font.Italic {
		t.Fatalf("no highlighting: %d colors, styles %+v", len(sl.Colors), sl.Styles)
	}
	for _, c := range sl.Colors {
		if c.End > len([]rune(sl.Text())) {
			t.Fatalf("a color runs past the folded text: %+v", c)
		}
	}
	tr.clicks["fold:"+key].Click()
	transcriptFrame(&tr, th, s, &ops, now)
	transcriptFrame(&tr, th, s, &ops, now)
	if n := strings.Count(sl.Text(), "\n") + 1; n != 45 {
		t.Fatalf("Show all shows %d lines, want 45", n)
	}
	if tr.docs["a"].code[key] != h {
		t.Fatal("the block lexed again")
	}
	tr.clicks["copy:"+key].Click()
	var copied string
	tr.Copy = func(_ layout.Context, s string) { copied = s }
	transcriptFrame(&tr, th, s, &ops, now)
	if strings.Count(copied, "\n")+1 != 45 {
		t.Fatalf("Copy took %d lines", strings.Count(copied, "\n")+1)
	}
}

// TestCodeBlockRun: a shell block's Run hands its command to Run, and
// a Go block has no Run.
func TestCodeBlockRun(t *testing.T) {
	s := codeReply("```bash\n# test\ngo test ./...\n```\n\n```go\nx := 1\n```\n")
	th := theme.Dark()
	var tr Transcript
	var got []string
	tr.Run = func(cmd string) { got = append(got, cmd) }
	var ops op.Ops
	now := time.Now()
	transcriptFrame(&tr, th, s, &ops, now)
	transcriptFrame(&tr, th, s, &ops, now)
	if tr.clicks["run:a/0"] == nil || tr.clicks["run:a/2"] != nil {
		t.Fatalf("Run buttons: %v", keys(tr.clicks))
	}
	tr.clicks["run:a/0"].Click()
	transcriptFrame(&tr, th, s, &ops, now)
	if len(got) != 1 || got[0] != "go test ./..." {
		t.Fatalf("Run got %q", got)
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
