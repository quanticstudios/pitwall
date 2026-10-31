package daemon

import (
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/web"
)

// serveWeb publishes web.json for its loopback listener, serves /ws behind
// the Origin check, and stops listening when stopped.
func TestServeWeb(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.json")
	d := &Daemon{o: Options{WebPath: path}}
	stop := d.serveWeb(config.WebSettings{Enabled: true})
	f, err := web.Read(path)
	if err != nil || f.PID != os.Getpid() || !strings.HasPrefix(f.URL, "ws://127.0.0.1:") || len(f.Token) != 64 {
		stop()
		t.Fatalf("web.json %+v %v", f, err)
	}
	url := "http" + strings.TrimPrefix(f.URL, "ws")
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign origin: %d, want 403", resp.StatusCode)
	}
	stop()
	if _, err := http.Get(url); err == nil {
		t.Error("still serving after stop")
	}
	if g, _ := web.Read(path); g.Token != f.Token {
		t.Errorf("stop changed the token")
	}

	// A port still held, as by a daemon handing off, is nil: watchListener
	// tries again at the next check.
	ln, err := web.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if stop := d.serveWeb(config.WebSettings{Enabled: true, Port: ln.Addr().(*net.TCPAddr).Port}); stop != nil {
		stop()
		t.Error("serveWeb on a taken port did not return nil")
	}
}

// With [web] bundle, the listener serves the bundle at / to a browser
// that opened a code from web.NewCode, and to nothing else.
func TestServeWebBundle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "web.json")
	bundle := t.TempDir()
	os.WriteFile(filepath.Join(bundle, "index.html"), []byte("fixture"), 0o644)
	d := &Daemon{o: Options{WebPath: path}}
	stop := d.serveWeb(config.WebSettings{Enabled: true, Bundle: bundle})
	defer stop()
	f, _ := web.Read(path)
	base := "http" + strings.TrimSuffix(strings.TrimPrefix(f.URL, "ws"), "/ws")
	if resp, err := http.Get(base + "/"); err != nil || resp.StatusCode != 401 {
		t.Fatalf("no session: %v %v", resp, err)
	}
	code, err := web.NewCode(web.OpenPath(path), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	resp, err := (&http.Client{Jar: jar}).Get(base + "/_pitwall/open?code=" + code)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "fixture" || resp.Request.URL.String() != base+"/" {
		t.Fatalf("open: %d %q at %s", resp.StatusCode, b, resp.Request.URL)
	}
}
