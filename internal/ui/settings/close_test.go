package settings

import (
	"image"
	"path/filepath"
	"testing"

	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestConfirmCloseSetting(t *testing.T) {
	var p Page
	path := filepath.Join(t.TempDir(), "config.toml")
	p.s, _ = config.LoadFile(path)
	p.th = theme.Dark()
	var ops op.Ops
	for _, section := range p.appearance() {
		for _, row := range section.rows {
			if row.label == "Confirm close" {
				p.btn("appearance.confirm_close").Click()
				row.control(gl.Context{Ops: &ops, Constraints: gl.Exact(image.Pt(800, 600))})
				s, problems := config.LoadFile(path)
				if p.err != "" || len(problems) != 0 || s.ConfirmClose {
					t.Fatalf("toggle: confirm close %v, problems %v, error %s", s.ConfirmClose, problems, p.err)
				}
				return
			}
		}
	}
	t.Fatal("Confirm close missing from Settings")
}
