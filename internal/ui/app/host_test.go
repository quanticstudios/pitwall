package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/io/key"
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
