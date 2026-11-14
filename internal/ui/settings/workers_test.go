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

func TestCloseDoneWorkersSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	var p Page
	p.s, _ = config.LoadFile(path)
	p.th = theme.Dark()
	var ops op.Ops
	gtx := gl.Context{Ops: &ops, Constraints: gl.Exact(image.Pt(800, 600))}
	for _, tc := range []struct {
		text  string
		want  time.Duration
		bad   bool
		shown string
	}{
		{"30m", 30 * time.Minute, false, "30m0s"},
		{"off", 0, false, "off"},
		{"bad", 0, true, ""},
		{"0s", 0, true, ""},
		{"-1s", 0, true, ""},
		{"", 10 * time.Minute, false, "10m0s"},
	} {
		p.saveCloseDoneWorkers(tc.text)
		s, problems := config.LoadFile(path)
		if p.err != "" || (p.closeWorkers.err != "") != tc.bad || len(problems) > 0 || s.CloseDoneWorkers != tc.want {
			t.Fatalf("%q: duration %s, errors %s / %s, problems %v", tc.text, s.CloseDoneWorkers, p.err, p.closeWorkers.err, problems)
		}
		p.s = s
		if !tc.bad {
			p.closeDoneWorkersRow().control(gtx)
			if got := p.closeWorkers.grace.Text(); got != tc.shown {
				t.Fatalf("field %q, want %q", got, tc.shown)
			}
		}
	}
}
