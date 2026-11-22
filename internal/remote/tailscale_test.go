package remote

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailscaleAuthorizeAndCoexistence(t *testing.T) {
	s, _ := newServer(t)
	s.Authorize = func(r *http.Request) (Device, int, string) {
		if r.RemoteAddr == "100.64.0.2:1234" {
			return Device{ID: "ts-0123456789abcdef", Name: "alice@example.com (iphone)", Tailscale: true}, 0, ""
		}
		return Device{}, http.StatusForbidden, "tailscale user bob@example.com is not allowed"
	}
	h := s.Handler()

	// Allowed Tailscale peer needs no pairing token.
	r := httptest.NewRequest("GET", "/api/items", nil)
	r.RemoteAddr = "100.64.0.2:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"device":"alice@example.com (iphone)"`) {
		t.Fatalf("allowed tailscale peer: %d %s", w.Code, w.Body.String())
	}

	// Denied Tailscale peer gets 403 with reason.
	r = httptest.NewRequest("GET", "/api/items", nil)
	r.RemoteAddr = "100.64.0.9:1234"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "bob@example.com") {
		t.Fatalf("denied tailscale peer: %d %s", w.Code, w.Body.String())
	}

	// Cross-origin request over Tailscale auth is rejected to prevent CSRF.
	r = httptest.NewRequest("POST", "/api/answer", strings.NewReader(`{"pane":"p1","allow":true}`))
	r.RemoteAddr = "100.64.0.2:1234"
	r.Host = "pitwall-box.tail1.ts.net"
	r.Header.Set("Origin", "https://evil.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin tailscale POST: %d %s", w.Code, w.Body.String())
	}

	r = httptest.NewRequest("POST", "/api/answer", strings.NewReader(`{"pane":"p1","allow":true}`))
	r.RemoteAddr = "100.64.0.2:1234"
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site Sec-Fetch-Site POST: %d %s", w.Code, w.Body.String())
	}

	// Same-origin POST succeeds.
	r = httptest.NewRequest("POST", "/api/answer", strings.NewReader(`{"pane":"p1","allow":true}`))
	r.RemoteAddr = "100.64.0.2:1234"
	r.Host = "pitwall-box.tail1.ts.net"
	r.Header.Set("Origin", "https://pitwall-box.tail1.ts.net")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("same-origin tailscale POST: %d %s", w.Code, w.Body.String())
	}

	// Paired-device Bearer token still works beside Tailscale auth, even from a non-allowed peer IP.
	tok := pairDevice(t, s, h, "paired-pixel")
	r = httptest.NewRequest("GET", "/api/items", nil)
	r.RemoteAddr = "100.64.0.9:1234"
	r.Header.Set("Authorization", "Bearer "+tok)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"device":"paired-pixel"`) {
		t.Fatalf("paired token alongside tailscale: %d %s", w.Code, w.Body.String())
	}
}

func TestTailscaleStatusAndStateHelpers(t *testing.T) {
	dir := t.TempDir()
	if TailscaleStateExists(dir) {
		t.Fatal("fresh dir reported state exists")
	}
	if _, ok := ReadTailscaleStatus(dir); ok {
		t.Fatal("missing status returned ok")
	}
	want := TailscaleStatus{
		Enabled:  true,
		State:    TailscaleNeedsLogin,
		Hostname: "pitwall-box",
		AuthURL:  "https://login.tailscale.com/a/abc123",
	}
	if err := WriteTailscaleStatus(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok := ReadTailscaleStatus(dir)
	if !ok || got.State != want.State || got.AuthURL != want.AuthURL || got.Hostname != want.Hostname {
		t.Fatalf("status = %+v ok=%v", got, ok)
	}

	// State dir & purge marker.
	if err := os.MkdirAll(TailscaleDir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(TailscaleDir(dir), "tailscaled.state"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "devices"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "devices", "ts-0123456789abcdef.push.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !TailscaleStateExists(dir) {
		t.Fatal("expected TailscaleStateExists true")
	}
	if err := RequestTailscalePurge(dir); err != nil {
		t.Fatal(err)
	}
	if !ConsumeTailscalePurge(dir) {
		t.Fatal("expected first ConsumeTailscalePurge true")
	}
	if ConsumeTailscalePurge(dir) {
		t.Fatal("expected second ConsumeTailscalePurge false")
	}
	if err := RemoveTailscaleState(dir); err != nil {
		t.Fatal(err)
	}
	if TailscaleStateExists(dir) {
		t.Fatal("state still exists after RemoveTailscaleState")
	}
	if _, err := os.Stat(filepath.Join(dir, "devices", "ts-0123456789abcdef.push.json")); !os.IsNotExist(err) {
		t.Fatalf("ts push subscription survived RemoveTailscaleState: %v", err)
	}
}
