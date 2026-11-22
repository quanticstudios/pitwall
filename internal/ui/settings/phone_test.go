package settings

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/remote"
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

// TestPhoneTailscaleSettings verifies the built-in Tailscale controls in
// Settings -> Phone across off, needs_login, running, error, and saved-state
// states.
func TestPhoneTailscaleSettings(t *testing.T) {
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

	// Off by default: Tailscale section is present with the opt-in toggle.
	secs := p.phone()
	if len(secs) < 2 || secs[len(secs)-1].title != "Tailscale" || len(secs[len(secs)-1].rows) != 1 {
		t.Fatalf("off tailscale section = %+v", secs)
	}
	draw()

	// Enabled + needs_login shows the login URL row and access summary.
	os.WriteFile(path, []byte("[remote.tailscale]\nenabled = true\nhostname = \"pitwall-box\"\nallow_users = [\"bob@example.com\"]\nallow_tags = [\"tag:phones\"]\n"), 0o644)
	p.s, _ = config.LoadFile(path)
	if err := remote.WriteTailscaleStatus(p.ph.dir, remote.TailscaleStatus{
		Enabled:  true,
		State:    remote.TailscaleNeedsLogin,
		Hostname: "pitwall-box",
		AuthURL:  "https://login.tailscale.com/a/abc123",
	}); err != nil {
		t.Fatal(err)
	}
	secs = p.phone()
	tsSec := secs[len(secs)-1]
	if len(tsSec.rows) != 4 || tsSec.rows[1].label != "Sign in to Tailscale" || !strings.Contains(tsSec.rows[2].desc, "bob@example.com") || !strings.Contains(tsSec.rows[2].desc, "tag:phones") {
		t.Fatalf("needs_login rows: %+v", tsSec.rows)
	}
	draw()

	// Running shows the HTTPS tailnet URL, owner, and supports QR toggle.
	if err := remote.WriteTailscaleStatus(p.ph.dir, remote.TailscaleStatus{
		Enabled:  true,
		State:    remote.TailscaleRunning,
		Hostname: "pitwall-box",
		DNSName:  "pitwall-box.tail1.ts.net",
		URL:      "https://pitwall-box.tail1.ts.net/",
		SelfUser: "alice@example.com",
		IPs:      []string{"100.64.0.5"},
	}); err != nil {
		t.Fatal(err)
	}
	p.ph.tsShowQR = true
	secs = p.phone()
	tsSec = secs[len(secs)-1]
	if tsSec.rows[1].label != "Tailnet address" || !strings.Contains(tsSec.rows[1].desc, "pitwall-box") || !strings.Contains(tsSec.rows[2].desc, "alice@example.com (owner)") {
		t.Fatalf("running rows: %+v", tsSec.rows)
	}
	draw()
	if p.ph.tsQR == nil {
		t.Error("expected QR code to be encoded when tsShowQR is true")
	}

	// Off with saved state on disk offers Delete state.
	os.WriteFile(path, []byte("[remote.tailscale]\nenabled = false\n"), 0o644)
	p.s, _ = config.LoadFile(path)
	os.MkdirAll(remote.TailscaleDir(p.ph.dir), 0o700)
	os.WriteFile(filepath.Join(remote.TailscaleDir(p.ph.dir), "tailscaled.state"), []byte("{}"), 0o600)
	secs = p.phone()
	tsSec = secs[len(secs)-1]
	if len(tsSec.rows) != 2 || tsSec.rows[1].label != "Saved Tailscale state" {
		t.Fatalf("saved state rows: %+v", tsSec.rows)
	}
	draw()
}

// TestPhoneTailscaleShots renders headless offscreen PNGs of Settings -> Phone
// in off, needs_login, and running states to $PITWALL_SETTINGS_SHOTS.
func TestPhoneTailscaleShots(t *testing.T) {
	outDir := os.Getenv("PITWALL_SETTINGS_SHOTS")
	if outDir == "" {
		t.Skip("PITWALL_SETTINGS_SHOTS is not set")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	render := func(name, cfgTOML string, st *remote.TailscaleStatus, showQR bool) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		os.WriteFile(path, []byte(cfgTOML), 0o644)
		var p Page
		p.ph.dir = filepath.Join(dir, "remote")
		if st != nil {
			if err := remote.WriteTailscaleStatus(p.ph.dir, *st); err != nil {
				t.Fatal(err)
			}
		}
		p.Show(path)
		p.s, _ = config.LoadFile(path)
		p.cat = catPhone
		p.ph.tsShowQR = showQR

		sz := image.Pt(1100, 860)
		w, err := headless.NewWindow(sz.X, sz.Y)
		if err != nil {
			t.Fatalf("headless.NewWindow: %v", err)
		}
		defer w.Release()
		var ops op.Ops
		th := theme.Dark()
		// Two frames so layout settles.
		for range 2 {
			ops.Reset()
			gtx := gl.Context{
				Ops:         &ops,
				Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Constraints: gl.Exact(sz),
				Now:         time.Now(),
			}
			p.Layout(gtx, th, p.s, nil)
			if err := w.Frame(&ops); err != nil {
				t.Fatalf("Frame: %v", err)
			}
		}
		img := image.NewRGBA(image.Rectangle{Max: sz})
		if err := w.Screenshot(img); err != nil {
			t.Fatalf("Screenshot: %v", err)
		}
		f, err := os.Create(filepath.Join(outDir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	render("phone-tailscale-off", "[remote.tailscale]\nhostname = \"pitwall-workstation\"\n", nil, false)
	render("phone-tailscale-login", "[remote.tailscale]\nenabled = true\nhostname = \"pitwall-workstation\"\n", &remote.TailscaleStatus{
		Enabled:  true,
		State:    remote.TailscaleNeedsLogin,
		Hostname: "pitwall-workstation",
		AuthURL:  "https://login.tailscale.com/a/0123456789abcdef",
	}, false)
	render("phone-tailscale-running", "[remote.tailscale]\nenabled = true\nhostname = \"pitwall-workstation\"\nallow_users = [\"teammate@example.com\"]\nallow_tags = [\"tag:phones\"]\n", &remote.TailscaleStatus{
		Enabled:  true,
		State:    remote.TailscaleRunning,
		Hostname: "pitwall-workstation",
		DNSName:  "pitwall-workstation.tail1234.ts.net",
		URL:      "https://pitwall-workstation.tail1234.ts.net/",
		SelfUser: "alice@example.com",
		IPs:      []string{"100.64.0.42"},
	}, true)
}
