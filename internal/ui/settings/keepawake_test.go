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
	for _, sec := range p.agents() {
		for _, row := range sec.rows {
			if row.label == "Keep awake" {
				p.btn("agents.keep_awake").Click()
				row.control(gtx)
			}
		}
	}
	p.s, _ = config.LoadFile(path)
	if !p.s.KeepAwake {
		t.Fatal("keep awake toggle did not save")
	}
	for _, tc := range []struct {
		text string
		want time.Duration
		bad  bool
	}{
		{"48h", 48 * time.Hour, false},
		{"10s", 10 * time.Second, false},
		{"-1s", 10 * time.Second, true},
		{"bad", 10 * time.Second, true},
		{"0s", 0, false},
		{"15m", config.DefaultKeepAwakeGrace, false},
		{"10s", 10 * time.Second, false},
		{"", config.DefaultKeepAwakeGrace, false},
	} {
		p.saveAwakeGrace(tc.text)
		s, probs := config.LoadFile(path)
		if p.err != "" || (p.aw.err != "") != tc.bad || len(probs) != 0 || !s.KeepAwake || s.KeepAwakeGrace != tc.want {
			t.Fatalf("%s: error %s / %s, on %v, grace %s, problems %v", tc.text, p.err, p.aw.err, s.KeepAwake, s.KeepAwakeGrace, probs)
		}
		p.s = s
		if !tc.bad {
			p.keepAwakeGraceField()(gtx)
			if text := p.aw.grace.Text(); text != tc.want.String() {
				t.Errorf("shown %q, want %s", text, tc.want)
			}
		}
	}
}
