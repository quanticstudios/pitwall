package app

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// On a Host the window names it and reads none of its paths here: the
// panel lists no files and watches no transcript, the folder dialog
// completes nothing and takes a full path on the host as typed.
func TestHostStaysOffLocalPaths(t *testing.T) {
	Host = "box"
	t.Cleanup(func() { Host = "" })
	u, keys := keyWindow(t, conventional)
	if got := u.windowTitle(); !strings.HasSuffix(got, " · box · pitwall") {
		t.Errorf("title %q", got)
	}
	keys(press("L", key.ModCtrl|key.ModShift))
	u.panel.mu.Lock()
	dir, watching := u.panel.filesDir, u.panel.watching
	u.panel.mu.Unlock()
	if !u.nav.panelOpen || dir != "" || watching != "" {
		t.Errorf("panel open %v, listing %q, watching %q", u.nav.panelOpen, dir, watching)
	}

	local := t.TempDir()
	if err := os.Mkdir(filepath.Join(local, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := dirMatches(local + "/"); got != nil {
		t.Errorf("completed from this machine: %v", got)
	}
	if got, err := resolveDir("/srv/not-here/"); err != nil || got != "/srv/not-here" {
		t.Errorf("resolveDir = %q, %v", got, err)
	}
	if _, err := resolveDir("~/x"); err == nil || err.Error() != "Type the folder's full path on box." {
		t.Errorf("home-relative path: %v", err)
	}
}

// hostFake is a window of two machines: calm-heron (s2) is on box.
type hostFake struct{ *FakeBackend }

func (hostFake) SessionHost(session string) string {
	if session == "s2" {
		return "box"
	}
	return ""
}

func (hostFake) Hosts() []sidebar.HostInfo {
	return []sidebar.HostInfo{{Up: true, Sessions: []string{"s1", "s3"}}, {Name: "box", Up: true, Sessions: []string{"s2"}}}
}

// In a window of several machines the session shown decides the host:
// its paths stay there, and the title, switcher and notifications name it.
func TestShownSessionPicksHost(t *testing.T) {
	t.Cleanup(func() { shownHost = "" })
	b := hostFake{NewFakeBackend()}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
	var ops op.Ops
	frame := func() {
		ops.Reset()
		u.layout(gl.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()})
	}
	frame()
	if curHost() != "" || strings.Contains(u.windowTitle(), "box") {
		t.Fatalf("on this machine's session: host %q, title %q", curHost(), u.windowTitle())
	}
	st := b.State()
	if !u.sidebar.HideSessions {
		t.Fatal("session list remains visible with sessions off")
	}
	if rows := u.sw.rows(&st); len(rows) != len(st.Sessions) {
		t.Fatalf("switcher omits sessions with the list hidden: %v", rows)
	}
	u.nav.cycleSession(&st, 1)
	if u.nav.session != "s2" {
		t.Fatalf("next session skips box: %q", u.nav.session)
	}
	u.nav.cycleSession(&st, -1)
	if u.nav.session != "s1" {
		t.Fatalf("previous session skips local: %q", u.nav.session)
	}
	u.nav.switchSession(&st, "s2")
	frame()
	if curHost() != "box" || !strings.HasSuffix(u.windowTitle(), " · box · pitwall") {
		t.Fatalf("on box's session: host %q, title %q", curHost(), u.windowTitle())
	}
	if got := dirMatches(t.TempDir() + "/"); got != nil {
		t.Errorf("completed from this machine: %v", got)
	}
	if hostPrefix(b, "s2") != "box · " || hostPrefix(b, "s1") != "" || hostPrefix(NewFakeBackend(), "s2") != "" {
		t.Error("hostPrefix")
	}
}
