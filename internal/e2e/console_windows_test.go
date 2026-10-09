package e2e_test

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/pane"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// TestGUIBinaryPrints builds pitwall.exe as the release does, a GUI
// program, and runs pitwall --version from cmd in a pane: the command
// attaches to cmd's console and prints there. Piped, it prints to the pipe.
func TestGUIBinaryPrints(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "pitwall.exe")
	run(t, time.Minute, filepath.Join("..", ".."), "go", "build", "-ldflags=-H=windowsgui", "-o", bin, "./cmd/pitwall")
	if out := run(t, 10*time.Second, ".", bin, "--version"); !strings.HasPrefix(out, "pitwall ") {
		t.Fatalf("piped: %q", out)
	}
	// cmd outlives pitwall by two seconds, so pitwall finds its parent's
	// console. The temp path has no spaces to quote for cmd.
	p, err := pane.Start(pane.Config{ID: "gui", Cmd: []string{"cmd.exe", "/c", bin + " --version & ping -n 3 127.0.0.1 >nul"},
		Cols: 80, Rows: 10, NewVT: vt.New})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var screen string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		g := p.Snapshot()
		var b bytes.Buffer
		for _, c := range g.Cells {
			b.WriteString(c.Content)
		}
		if screen = b.String(); strings.Contains(screen, "pitwall ") {
			return
		}
	}
	t.Fatalf("no version on the console:\n%s", screen)
}
