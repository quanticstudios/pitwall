package app

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/model"
)

// TestOpenReader checks a Ctrl+clicked markdown path opens in the reader
// from the pane's folder, a web link does not, and a window on a Host
// says the file is not here.
func TestOpenReader(t *testing.T) {
	defer func(h string) { Host = h }(Host)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := &ui{b: NewFakeBackend()}
	u.nav.workspace = "w"
	st := model.State{Panes: []model.Pane{{ID: "p", WorkspaceID: "w", Cwd: dir}}}
	var ops op.Ops
	gtx := gl.Context{Ops: &ops}
	r := image.Rect(0, 0, 100, 100)
	if u.openReader(gtx, &st, "p", "https://example.com/README.md", r) {
		t.Error("a web link opened in the reader")
	}
	Host = "box"
	if !u.openReader(gtx, &st, "p", "README.md", r) || u.reader.On() || !strings.Contains(u.notice, "on box") {
		t.Errorf("host: reader %v, notice %q", u.reader.On(), u.notice)
	}
	Host = ""
	u.boardOn = true
	if !u.openReader(gtx, &st, "p", "./README.md:4", r) || !u.reader.On() || u.reader.Path() != filepath.Join(dir, "README.md") || u.readerWS != "w" || u.boardOn {
		t.Errorf("local: reader %v at %q, tab %q, board %v", u.reader.On(), u.reader.Path(), u.readerWS, u.boardOn)
	}
	u.reader.Close()
	if !u.openReader(gtx, &st, "p", "missing.md", r) || u.reader.On() || !strings.Contains(u.notice, "Cannot open missing.md") {
		t.Errorf("missing: reader %v, notice %q", u.reader.On(), u.notice)
	}
}
