package mobile

import (
	"encoding/json"
	"fmt"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func recoveryApp(t *testing.T) *App {
	t.Helper()
	old := Version
	Version = "v0.2.9"
	t.Cleanup(func() { Version = old })
	a := &App{w: new(app.Window), th: theme.Dark(), dir: t.TempDir(), ui: make(chan func(), 1), closed: make(chan struct{})}
	a.updates.settings.CheckUpdates = true
	if err := json.Unmarshal([]byte(apkReleaseJSON), &a.updates.release); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestInstallMessageYieldsToNewRelease(t *testing.T) {
	a := recoveryApp(t)
	a.applyInstallState(installState{cache: t.TempDir(), message: "Update installed."})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, apkReleaseJSON) }))
	defer srv.Close()
	oldURL := releaseURL
	releaseURL = srv.URL
	t.Cleanup(func() { releaseURL = oldURL })
	a.startUpdateCheck(time.Now(), false)
	select {
	case done := <-a.ui:
		done()
	case <-time.After(5 * time.Second):
		t.Fatal("no check result")
	}
	if got := a.updates.lineText(); got != "pitwall v0.3.0 is out" {
		t.Fatalf("stale install result masks release: %q", got)
	}
	a.applyInstallState(installState{cache: a.updates.cache, message: "Update installed."})
	if got := a.updates.lineText(); got != "pitwall v0.3.0 is out" {
		t.Fatalf("late success masks release: %q", got)
	}
}

func TestStaleInstallerUnlocksInstall(t *testing.T) {
	a := recoveryApp(t)
	cache := t.TempDir()
	a.applyInstallState(installState{cache: cache, message: "Confirm installation in Android.", busy: true})
	if a.updates.canInstall() {
		t.Fatal("offered a second active install")
	}
	a.applyInstallState(installState{cache: cache})
	if !a.updates.canInstall() || a.updates.installNote != "" {
		t.Fatal("stale busy state still locks Install")
	}
}

func TestClearInstallNoteDoesNotDismissRelease(t *testing.T) {
	a := recoveryApp(t)
	a.updates.installNote = "Update failed: x"
	a.updates.dismiss.Click()
	gtx := layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(300, 100)}}
	a.updateLine(gtx)
	if a.updates.installNote != "" || a.updates.settings.Dismissed != "" {
		t.Fatal("clearing a note dismissed the release")
	}
	a.updates.installing = true
	a.updates.installNote = "Downloading update…"
	var router input.Router
	gtx.Ops.Reset()
	gtx.Source = router.Source()
	a.updateLine(gtx)
	router.Frame(gtx.Ops)
	for _, n := range router.AppendSemantics(nil) {
		if strings.Contains(n.Desc.Description, "Dismiss") || strings.Contains(n.Desc.Description, "Clear update") {
			t.Fatal("installing row exposes a dismiss control")
		}
	}
}

func TestProgressDoesNotWaitForFrames(t *testing.T) {
	a := recoveryApp(t)
	a.ui <- func() {}
	done := make(chan struct{})
	go func() {
		for n := int64(1); n <= 10000; n++ {
			a.reportDownloadProgress(n)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("progress blocked on a full UI queue")
	}
	if a.updates.progress.Load() != 10000 || len(a.ui) != 1 {
		t.Fatal("progress did not retain the latest byte count")
	}
}

func TestSweepPartialAPKsWhenIdle(t *testing.T) {
	a := recoveryApp(t)
	cache := t.TempDir()
	for _, name := range []string{"pitwall-update-first.apk", "pitwall-update-second.apk", "keep.png"} {
		if err := os.WriteFile(filepath.Join(cache, name), []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a.updates.downloadCancel = func() {}
	a.updates.installing = true
	a.applyInstallState(installState{cache: cache})
	files, _ := os.ReadDir(cache)
	if len(files) != 3 || !a.updates.installing {
		t.Fatal("startup report disturbed an active download")
	}
	a.updates.downloadCancel = nil
	a.applyInstallState(installState{cache: cache})
	files, err := os.ReadDir(cache)
	if err != nil || len(files) != 1 || files[0].Name() != "keep.png" {
		t.Fatalf("partial APK sweep: %v, %v", files, err)
	}
}

func TestUpdateStatusCopy(t *testing.T) {
	a := recoveryApp(t)
	u := &a.updates
	u.release = phoneRelease{Tag: "v0.2.10"}
	u.checked = true
	if got := u.checkText(); got != "v0.2.10 is out; its APK is not attached yet" {
		t.Fatalf("missing APK: %q", got)
	}
	u.cancel = func() {}
	if got := u.checkText(); got != "Checking…" {
		t.Fatalf("checking: %q", got)
	}
	u.cancel = nil
	u.release.Tag = "v0.2.9"
	if got := u.checkText(); got != "Up to date (v0.2.9)" {
		t.Fatalf("current release: %q", got)
	}
	if err := json.Unmarshal([]byte(apkReleaseJSON), &u.release); err != nil {
		t.Fatal(err)
	}
	u.release.Tag = "v0.2.10"
	if !u.release.offered("v0.2.9", u.settings) {
		t.Fatal("v0.2.10 not offered over v0.2.9")
	}
	u.cache = ""
	a.startInstall()
	if !strings.Contains(u.installNote, "Close and reopen pitwall") {
		t.Fatalf("no recovery action: %q", u.installNote)
	}
}
