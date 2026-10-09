package settings

import (
	"crypto/ecdh"
	"encoding/base64"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestPushRow: a device's Push notifications row says off until the
// phone subscribes, then offers Send test, whose result shows under it.
func TestPushRow(t *testing.T) {
	var p Page
	p.th = theme.Dark()
	p.ph.dir = filepath.Join(t.TempDir(), "remote")
	code, err := remote.Pair(p.ph.dir, "pixel", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/pair", strings.NewReader(`{"code":"`+code+`"}`))
	w := httptest.NewRecorder()
	(&remote.Server{Dir: p.ph.dir}).Handler().ServeHTTP(w, r)
	p.readDevices()
	if w.Code != 200 || len(p.ph.devices) != 1 {
		t.Fatalf("pair: %d, %d devices", w.Code, len(p.ph.devices))
	}
	d := p.ph.devices[0]
	btn := func(id, label string, kind btnKind, click func()) gl.Widget { return nil }
	if got := p.pushRow(d, btn); got.label != "Push notifications" || !strings.HasPrefix(got.desc, "Off for pixel") {
		t.Fatalf("unsubscribed row %+v", got)
	}
	ua, _ := ecdh.P256().GenerateKey(nil)
	sub := remote.Subscription{Endpoint: "https://127.0.0.1:1/push", Keys: remote.PushKeys{
		P256dh: base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))}}
	if err := remote.Subscribe(p.ph.dir, d.ID, sub); err != nil {
		t.Fatal(err)
	}
	if got := p.pushRow(d, btn); !strings.HasPrefix(got.desc, "On for pixel") || got.below != nil {
		t.Fatalf("subscribed row %+v", got)
	}
	// Nothing listens on port 1, so the test push fails, and says so.
	p.sendTest(d.ID)
	for start := time.Now(); time.Since(start) < 5*time.Second; time.Sleep(10 * time.Millisecond) {
		p.ph.test.mu.Lock()
		busy := p.ph.test.busy
		p.ph.test.mu.Unlock()
		if !busy {
			break
		}
	}
	if p.ph.test.ok || p.ph.test.result == "" || p.pushRow(d, btn).below == nil {
		t.Fatalf("test push result %+v", &p.ph.test)
	}
}
