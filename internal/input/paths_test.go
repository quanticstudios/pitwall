package input

import (
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/vt"
)

func TestPastePaths(t *testing.T) {
	paths := []string{"/tmp/screen shot.png", "/tmp/it's $a `file`; (1).png", `C:\Users\me\screen shot.png`, "/tmp/画像.png"}
	for _, bracketed := range []bool{false, true} {
		got := string(PastePaths(paths, vt.Modes{BracketedPaste: bracketed}))
		if bracketed {
			if !strings.HasPrefix(got, "\x1b[200~") || !strings.HasSuffix(got, "\x1b[201~") {
				t.Fatalf("missing paste boundaries: %q", got)
			}
			got = strings.TrimSuffix(strings.TrimPrefix(got, "\x1b[200~"), "\x1b[201~")
		}
		if runtime.GOOS != "windows" {
			// Exercise the quoted paths with a real shell. No path becomes
			// a command, expansion or a second argument because of spaces.
			out, err := exec.Command("sh", "-c", "printf '%s\\000' "+got).Output()
			if err != nil || !slices.Equal(strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"), paths) {
				t.Errorf("shell decoded %q, %v; want %q", out, err, paths)
			}
		}
	}
	for _, bad := range []string{"/tmp/a\nb.png", "\x1b[201~x", "a\x00b", "a\rb", "a\x7fb"} {
		if got := PastePaths([]string{bad}, vt.Modes{BracketedPaste: true}); len(got) != 0 {
			t.Errorf("unsafe path %q produced %q", bad, got)
		}
	}
	if got := PastePaths(nil, vt.Modes{BracketedPaste: true}); len(got) != 0 {
		t.Errorf("empty drop produced %q", got)
	}
}

func TestQuotePath(t *testing.T) {
	for _, c := range []struct {
		path    string
		windows bool
		want    string
	}{
		{"/tmp/a b.png", false, "'/tmp/a b.png'"},
		{"/tmp/a'b.png", false, "'/tmp/a'\\''b.png'"},
		{`C:\Users\O'Brien\a b.png`, true, `'C:\Users\O''Brien\a b.png'`},
	} {
		if got := quotePath(c.path, c.windows); got != c.want {
			t.Errorf("quote %q: %q, want %q", c.path, got, c.want)
		}
	}
}
