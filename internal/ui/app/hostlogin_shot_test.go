package app

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

type loginHosts struct {
	FakeShared
	down bool // box has the first tab and had been connected
}

func (f loginHosts) HostOf(id string) string {
	if f.down {
		return f.FakeShared.HostOf(id)
	}
	return ""
}

func (f loginHosts) Hosts() []sidebar.HostInfo {
	box := sidebar.HostInfo{Name: "box", SSH: "me@box", Note: "Tailscale SSH wants an approval. Run pitwall host login -- 'box'",
		Retry: time.Now().Add(5 * time.Minute),
		Login: &sidebar.Login{Reason: "Tailscale SSH wants an approval", Command: "pitwall host login -- 'box'", URL: "https://login.tailscale.com/a/1f2e3d4c5b6a7"}}
	if f.down {
		box.Since = time.Now().Add(-time.Hour)
	}
	st := f.State()
	here := sidebar.HostInfo{Up: true}
	for _, s := range st.Sessions {
		here.Sessions = append(here.Sessions, s.ID)
	}
	return []sidebar.HostInfo{here, box}
}

// linkAsks is a pitwall --host window whose dropped link waits on a login.
type linkAsks struct{ *FakeBackend }

func (linkAsks) Link() Link {
	return Link{State: LinkDown, Note: "Tailscale SSH wants an approval. Run pitwall host login -- 'box'",
		Login: &sidebar.Login{Reason: "Tailscale SSH wants an approval", Command: "pitwall host login -- 'box'", URL: "https://login.tailscale.com/a/1f2e3d4c5b6a7"}}
}
func (linkAsks) Reconnect() {}
func (linkAsks) Restart()   {}

// TestHostLoginShots draws a window whose host waits on a login, never
// connected, dropped, and as a --host window's dialog, and with PITWALL_SHOTS set writes the PNGs.
func TestHostLoginShots(t *testing.T) {
	win, err := headless.NewWindow(1280, 800)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	t.Cleanup(func() { shownHost = "" }) // layout sets it from the shown tab
	for _, name := range []string{"never-connected", "dropped", "host-window"} {
		down := name == "dropped"
		fb := NewFakeBackend()
		st := fb.State()
		tab := st.Workspaces[0]
		var b Backend = loginHosts{FakeShared{fb, tab.ID}, down}
		if name == "host-window" {
			b = linkAsks{fb}
		}
		u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, tab.ID, "")
		var ops op.Ops
		for range 3 {
			ops.Reset()
			gtx := gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now().Add(time.Second)}
			u.layout(gtx)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rect(0, 0, 1280, 800))
		if err := win.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		dir := os.Getenv("PITWALL_SHOTS")
		if dir == "" {
			continue
		}
		f, err := os.Create(filepath.Join(dir, "host-login-"+name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
