package daemon

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
}
