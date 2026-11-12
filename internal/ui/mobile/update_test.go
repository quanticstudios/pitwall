package mobile

import (
	"context"
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
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

const apkReleaseJSON = `{"tag_name":"v0.3.0","assets":[{"name":"pitwall_linux_arm64.tar.gz","browser_download_url":"https://example.com/desktop"},{"name":"pitwall-phone.apk","browser_download_url":"https://github.com/quanticstudios/pitwall/releases/download/v0.3.0/pitwall-phone.apk","size":5}]}`

func TestPhoneReleaseOffer(t *testing.T) {
	var r phoneRelease
	if err := json.Unmarshal([]byte(apkReleaseJSON), &r); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		current, dismissed string
		want               bool
	}{
		{"v0.2.9", "", true},
		{"0.2.9", "", true},
		{"v0.2.9-5-gabc123", "", true},
		{"0.2.9-5-gabc123", "", true},
		{"v0.3.0-5-gabc123", "", false},
		{"v0.3.0", "", false},
		{"v0.4.0", "", false},
		{"dev", "", false},
		{"dev-abc123", "", false},
		{"", "", false},
		{"v0.2.9", "v0.3.0", false},
		{"v0.2.9", "v0.4.0", false},
		{"v0.2.9", "v0.2.10", true},
	} {
		if got := r.offered(c.current, phoneSettings{CheckUpdates: true, Dismissed: c.dismissed}); got != c.want {
			t.Errorf("offer %s, dismissed %s = %v, want %v", c.current, c.dismissed, got, c.want)
		}
	}
	if r.offered("v0.2.9", phoneSettings{}) {
		t.Fatal("offered with updates disabled")
	}
	for _, body := range []string{
		`{"tag_name":"v0.3.0"}`,
		`{"tag_name":"v0.3.0","assets":[{"name":"other.apk","browser_download_url":"https://example.com/app.apk"}]}`,
		strings.ReplaceAll(apkReleaseJSON, "https://github.com/quanticstudios/pitwall/releases/download/v0.3.0/pitwall-phone.apk", "file:///app.apk"),
		strings.ReplaceAll(apkReleaseJSON, "https://github.com/quanticstudios/pitwall/releases/download/v0.3.0/pitwall-phone.apk", ""),
		strings.ReplaceAll(apkReleaseJSON, `"size":5`, `"size":0`),
		strings.ReplaceAll(apkReleaseJSON, `"size":5`, `"size":-1`),
		strings.ReplaceAll(apkReleaseJSON, `"size":5`, `"size":9999999999`),
	} {
		r = phoneRelease{}
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatal(err)
		}
		if r.offered("v0.2.9", phoneSettings{CheckUpdates: true}) {
			t.Fatalf("offered without a usable APK: %s", body)
		}
	}
}

func TestFetchPhoneRelease(t *testing.T) {
	status, body, requests := http.StatusOK, apkReleaseJSON, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
			t.Error("unexpected method or auth")
		}
		if requests > 1 && r.Header.Get("If-None-Match") != `"release-1"` {
			t.Error("missing ETag")
		}
		w.Header().Set("ETag", `"release-1"`)
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	r, etag, err := fetchPhoneRelease(context.Background(), srv.Client(), srv.URL, "", phoneRelease{})
	if err != nil || r.Tag != "v0.3.0" || etag != `"release-1"` || r.apkURL() == "" {
		t.Fatalf("200: %+v, %q, %v", r, etag, err)
	}
	status = http.StatusNotModified
	cached, e, err := fetchPhoneRelease(context.Background(), srv.Client(), srv.URL, etag, r)
	if err != nil || cached.Tag != r.Tag || cached.apkURL() != r.apkURL() || e != etag {
		t.Fatalf("304: %+v, %q, %v", cached, e, err)
	}
	for _, c := range []struct {
		status int
		body   string
	}{
		{http.StatusForbidden, "rate limited"},
		{http.StatusOK, "{"},
		{http.StatusOK, apkReleaseJSON + "{}"},
		{http.StatusOK, strings.Repeat(" ", (1<<20)+1)},
	} {
		status, body = c.status, c.body
		got, e, err := fetchPhoneRelease(context.Background(), srv.Client(), srv.URL, etag, r)
		if err == nil || got.Tag != r.Tag || e != etag {
			t.Fatalf("failure %d: %+v, %q, %v", status, got, e, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := fetchPhoneRelease(ctx, srv.Client(), srv.URL, etag, r); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestManualPhoneUpdateCheck(t *testing.T) {
	oldVersion, oldURL := Version, releaseURL
	t.Cleanup(func() { Version, releaseURL = oldVersion, oldURL })
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); fmt.Fprint(w, apkReleaseJSON) }))
			defer srv.Close()
			Version, releaseURL = "v0.2.9", srv.URL
			a := &App{w: new(app.Window), ui: make(chan func(), 10), closed: make(chan struct{})}
			a.startUpdateCheck(time.Now(), true)
			if a.updates.cancel == nil {
				t.Fatal("manual check did not start while daily checks were off")
			}
			select {
			case done := <-a.ui:
				done()
			case <-time.After(time.Second):
				t.Fatal("manual check did not finish")
			}
			if status == http.StatusOK {
				if !a.updates.checked || !a.updates.release.available(Version) {
					t.Fatal("manual check dropped its result")
				}
			} else if !strings.Contains(a.updates.checkError, "403") {
				t.Fatalf("missing failure reason: %q", a.updates.checkError)
			}
		})
	}
}

func TestPhoneSettings(t *testing.T) {
	dir := t.TempDir()
	s, err := loadPhoneSettings(dir)
	if err != nil || !s.CheckUpdates || s.Dismissed != "" {
		t.Fatalf("default: %+v, %v", s, err)
	}
	s = phoneSettings{CheckUpdates: false, Dismissed: "v0.3.0"}
	if err := savePhoneSettings(dir, s); err != nil {
		t.Fatal(err)
	}
	got, err := loadPhoneSettings(dir)
	if err != nil || got != s {
		t.Fatalf("reload: %+v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPhoneSettings(dir); err == nil {
		t.Fatal("invalid settings accepted")
	}
}

func TestPhoneUpdateSchedule(t *testing.T) {
	oldVersion, oldURL := Version, releaseURL
	t.Cleanup(func() { Version, releaseURL = oldVersion, oldURL })
	requests := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		fmt.Fprint(w, apkReleaseJSON)
	}))
	defer srv.Close()
	Version, releaseURL = "v0.2.9", srv.URL
	a := &App{w: new(app.Window), ui: make(chan func(), 10), closed: make(chan struct{})}
	a.updates.settings.CheckUpdates = true
	gtx := layout.Context{Ops: new(op.Ops), Now: time.Now()}
	a.pollUpdates(gtx)
	if a.updates.cancel != nil {
		t.Fatal("background app started a request")
	}
	a.focused = true
	a.updates.settings.CheckUpdates = false
	a.pollUpdates(gtx)
	if a.updates.cancel != nil {
		t.Fatal("disabled app started a request")
	}
	a.updates.settings.CheckUpdates = true
	Version = "dev"
	a.pollUpdates(gtx)
	if a.updates.cancel != nil {
		t.Fatal("dev build started a request")
	}
	Version = "v0.2.9"
	finish := func() {
		t.Helper()
		select {
		case <-requests:
		case <-time.After(time.Second):
			t.Fatal("no request")
		}
		select {
		case f := <-a.ui:
			f()
		case <-time.After(time.Second):
			t.Fatal("no result")
		}
	}
	a.pollUpdates(gtx)
	finish()
	gtx.Now = gtx.Now.Add(24*time.Hour - time.Second)
	a.pollUpdates(gtx)
	if a.updates.cancel != nil {
		t.Fatal("checked before 24 hours")
	}
	gtx.Now = gtx.Now.Add(time.Second)
	a.pollUpdates(gtx)
	finish()
}

func TestPhoneUpdateLineFits(t *testing.T) {
	a := &App{th: theme.Dark()}
	a.updates.settings.CheckUpdates = true
	if err := json.Unmarshal([]byte(apkReleaseJSON), &a.updates.release); err != nil {
		t.Fatal(err)
	}
	old := Version
	Version = "v0.2.9"
	defer func() { Version = old }()
	gtx := layout.Context{Ops: new(op.Ops), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Constraints{Max: image.Pt(280, 100)}}
	if d := a.updateLine(gtx); d.Size.X > 280 || d.Size.Y > 44 || d.Size.Y == 0 {
		t.Fatalf("update row dimensions: %v", d.Size)
	}
}
