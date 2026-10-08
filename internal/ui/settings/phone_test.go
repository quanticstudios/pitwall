package settings

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestPhonePair: Pair shows a QR code of the pairing URL until the code
// is used, and the page lays out in each state.
func TestPhonePair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	var p Page
	p.ph.dir = filepath.Join(dir, "remote")
	p.Show(path)
	p.th = theme.Dark()
	var ops op.Ops
	draw := func() {
		t.Helper()
		ops.Reset()
		p.cat = catPhone
		gtx := gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1000, 700)), Now: time.Now()}
		p.Layout(gtx, theme.Dark(), p.s, nil)
	}
	draw()
	if rows := p.phone()[0].rows; len(rows) != 1 {
		t.Fatalf("off: %d rows", len(rows))
	}
	os.WriteFile(path, []byte("[remote]\nenabled = true\nurl = \"https://box.tail1.ts.net\"\n"), 0o644)
	p.s, _ = config.LoadFile(path)
	p.pairPhone()
	if p.ph.code == nil || !strings.HasPrefix(p.ph.url, "https://box.tail1.ts.net/#pair=") || p.ph.name != "phone" {
		t.Fatalf("pairing %q %q %v", p.ph.url, p.ph.name, p.ph.code == nil)
	}
	if got := p.phone()[0].rows[1]; got.label != "Scan with your phone" || !strings.Contains(got.desc, p.ph.url) {
		t.Errorf("pair row %+v", got)
	}
	draw()
	// Once the code is used, the QR code goes.
	os.Remove(filepath.Join(p.ph.dir, "pair.json"))
	draw()
	if p.ph.code != nil {
		t.Error("used code still shown")
	}
}
