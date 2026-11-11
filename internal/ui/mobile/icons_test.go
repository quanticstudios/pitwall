package mobile

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gioui.org/app"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
)

// TestRepoIcon gives the sidebar a repo's icon once its machine sends it,
// and the letter tile, a root with no files, for a repo without one or a
// machine that serves none.
func TestRepoIcon(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), "pixels"...)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "not paired", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/api/icon/abc" {
			http.NotFound(w, r) // a daemon too old to serve icons says this to every hash
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	}))
	defer ts.Close()
	a := &App{w: new(app.Window), ui: make(chan func(), 16)}
	a.fleet = phone.NewFleet([]phone.Machine{{ID: "m1", Name: "box", Creds: phone.Creds{Server: ts.URL + "/", Token: "tok"}}})
	settle := func(dir string) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			a.drain()
			if _, ok := a.repoIcon("box", dir); ok {
				return
			}
		}
		t.Fatalf("%s: no answer", dir)
	}

	icon := phone.IconDir("m1", "abc", "api")
	if _, ok := a.repoIcon("box", icon); ok {
		t.Fatal("an icon not fetched yet reads as done")
	}
	settle(icon)
	if r, _ := a.repoIcon("box", icon); r.Root != icon || len(r.Files) != 1 || string(r.Files[0].Data) != string(png) {
		t.Fatalf("fetched icon: %+v", r)
	}
	for _, dir := range []string{phone.IconDir("m1", remote.NoIcon, "api"), phone.IconDir("m1", "gone", "api")} {
		settle(dir)
		if r, _ := a.repoIcon("box", dir); r.Root != dir || len(r.Files) != 0 {
			t.Errorf("%s: %+v, want the root alone, for the letter tile", dir, r)
		}
	}
	if r, ok := a.repoIcon("box", ""); !ok || r.Root != "" {
		t.Errorf("no folder: %+v %v, want no repo", r, ok)
	}
}
