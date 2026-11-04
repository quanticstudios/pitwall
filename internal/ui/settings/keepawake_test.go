package settings

import (
	"image"
	"os"
	"path/filepath"
	"testing"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestKeepAwakeSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var p Page
	p.s, _ = config.LoadFile(path)
	p.th = theme.Dark()
	var ops op.Ops
	gtx := gl.Context{Ops: &ops, Constraints: gl.Exact(image.Pt(800, 600))}
	for _, tc := range []struct{ label, button string }{
		{"Keep awake", "agents.keep_awake"},
		{"Keep awake grace", "agents.keep_awake_grace+"},
		{"Keep awake grace", "agents.keep_awake_gracereset"},
	} {
		found := false
		for _, sec := range p.agents() {
			for _, row := range sec.rows {
				if row.label == tc.label {
					p.btn(tc.button).Click()
					row.control(gtx)
					found = true
				}
			}
		}
		s, probs := config.LoadFile(path)
		grace := 15 * time.Minute
		if tc.button == "agents.keep_awake_grace+" {
			grace = 20 * time.Minute
		}
		if !found || p.err != "" || len(probs) != 0 || !s.KeepAwake || s.KeepAwakeGrace != grace {
			t.Fatalf("%s: found %v, error %s, on %v, grace %s, problems %v", tc.button, found, p.err, s.KeepAwake, s.KeepAwakeGrace, probs)
		}
		p.s = s
	}
}
