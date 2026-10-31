package web

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// get sends path as it is, unclean paths too, with cookie ("" for none).
func get(t *testing.T, hs *httptest.Server, path, cookie string) *http.Response {
	t.Helper()
	addr := strings.TrimPrefix(hs.URL, "http://")
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close() })
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET " + path + " HTTP/1.1\r\nHost: " + addr + "\r\nConnection: close\r\n"
	if cookie != "" {
		req += "Cookie: " + cookie + "\r\n"
	}
	io.WriteString(nc, req+"\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(nc), nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func body(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

// newBundle serves dir with web.json's token tok, a string the test may
// rotate while the server reads it, and returns the open code file's path.
func newBundle(t *testing.T, dir string, tok *atomic.Value) (*httptest.Server, string) {
	t.Helper()
	open := filepath.Join(t.TempDir(), "web-open.json")
	s := &Server{
		Token:    func() (string, error) { return tok.Load().(string), nil },
		Version:  "test",
		Client:   &Client{Dir: dir, Live: func(owner string) bool { return owner == tok.Load().(string) }},
		OpenPath: open,
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { s.Close(); hs.Close() })
	return hs, open
}

// openSession opens a session with a fresh code and returns its cookie.
func openSession(t *testing.T, hs *httptest.Server, open string) string {
	t.Helper()
	code, err := NewCode(open, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp := get(t, hs, "/_pitwall/open?code="+code, "")
	body(resp)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("open: %d %v", resp.StatusCode, resp.Header)
	}
	cks := resp.Cookies()
	if len(cks) != 1 || !cks[0].HttpOnly || cks[0].SameSite != http.SameSiteStrictMode || cks[0].Path != "/" {
		t.Fatalf("cookie %+v", cks)
	}
	if cks[0].Value == code || strings.Contains(cks[0].Value, goodToken) {
		t.Fatal("the cookie is the code or the token")
	}
	return cks[0].Name + "=" + cks[0].Value
}

func TestOpenCode(t *testing.T) {
	var tok atomic.Value
	tok.Store(goodToken)
	hs, open := newBundle(t, "testdata/bundle", &tok)
	if resp := get(t, hs, "/", ""); resp.StatusCode != 401 {
		t.Fatalf("no session: %d", resp.StatusCode)
	}
	code, _ := NewCode(open, time.Now())
	if resp := get(t, hs, "/_pitwall/open?code=nope", ""); resp.StatusCode != 401 || len(resp.Cookies()) != 0 {
		t.Fatalf("bad code: %d %v", resp.StatusCode, resp.Cookies())
	}
	if resp := get(t, hs, "/_pitwall/open?code="+code, ""); resp.StatusCode != 303 {
		t.Fatalf("code: %d", resp.StatusCode)
	}
	if resp := get(t, hs, "/_pitwall/open?code="+code, ""); resp.StatusCode != 401 {
		t.Fatalf("a code's second use: %d", resp.StatusCode)
	}
	code, _ = NewCode(open, time.Now().Add(-OpenTTL-time.Second))
	if resp := get(t, hs, "/_pitwall/open?code="+code, ""); resp.StatusCode != 401 {
		t.Fatalf("expired code: %d", resp.StatusCode)
	}
	// Only a hash is on disk, and an expired code is gone too.
	if _, err := os.Stat(open); !os.IsNotExist(err) {
		t.Fatalf("code file after use: %v", err)
	}
	code, _ = NewCode(open, time.Now())
	if data, _ := os.ReadFile(open); strings.Contains(string(data), code) {
		t.Fatal("the code is on disk")
	}
	// A foreign page cannot start one: the Origin check comes first.
	req, _ := http.NewRequest("GET", hs.URL+"/_pitwall/open?code="+code, nil)
	req.Header.Set("Origin", "http://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("foreign origin: %d", resp.StatusCode)
	}
}

func TestBundleFiles(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"index.html": "index", "assets/app.js": "app();", "sub/index.html": "sub"} {
		os.MkdirAll(filepath.Join(dir, filepath.Dir(name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644)
	}
	secret, _ := filepath.Abs("testdata/secret.txt")
	links := os.Symlink(secret, filepath.Join(dir, "link.txt")) == nil
	var tok atomic.Value
	tok.Store(goodToken)
	hs, open := newBundle(t, dir, &tok)
	ck := openSession(t, hs, open)

	for _, tc := range []struct{ path, ctype, want string }{
		{"/", "text/html; charset=utf-8", "index"},
		{"/index.html", "text/html; charset=utf-8", "index"},
		{"/assets/app.js", "text/javascript; charset=utf-8", "app();"},
		{"/sub/", "text/html; charset=utf-8", "sub"},
	} {
		resp := get(t, hs, tc.path, ck)
		h := resp.Header
		if got := body(resp); resp.StatusCode != 200 || got != tc.want || h.Get("Content-Type") != tc.ctype {
			t.Errorf("%s: %d %q %q", tc.path, resp.StatusCode, h.Get("Content-Type"), got)
		}
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self';") || strings.Contains(csp, "unsafe") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s: CSP %q", tc.path, csp)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: headers %v", tc.path, h)
		}
	}
	for _, p := range []string{
		"/../secret.txt", "/assets/../../secret.txt", "/%2e%2e/secret.txt", "/..%2fsecret.txt",
		"/assets/..%5c..%5csecret.txt", "/..\\secret.txt", "/" + secret, "/C:%5cWindows%5cwin.ini",
		"/sub", "/assets/", "/missing.js", "/.hidden", "/assets/.git/config", "/link.txt",
	} {
		// The mux sends an unclean path on to its clean form, inside the bundle.
		resp := get(t, hs, p, ck)
		if got := body(resp); resp.StatusCode == 200 || strings.Contains(got, "outside") {
			t.Errorf("%s: %d %q", p, resp.StatusCode, got)
		}
		// serveFile on its own, with no mux to clean the path first.
		r := httptest.NewRequest("GET", "/", nil)
		r.URL.Path = p
		w := httptest.NewRecorder()
		serveFile(w, r, dir)
		if w.Code != 404 {
			t.Errorf("serveFile %s: %d %q", p, w.Code, w.Body)
		}
	}
	if !links {
		t.Log("no symlinks here; /link.txt checked a missing file")
	}
}

func TestFixtureBundle(t *testing.T) {
	var tok atomic.Value
	tok.Store(goodToken)
	hs, open := newBundle(t, "testdata/bundle", &tok)
	ck := openSession(t, hs, open)
	for _, p := range []string{"/", "/assets/app.js", "/assets/app.css"} {
		if resp := get(t, hs, p, ck); resp.StatusCode != 200 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	for _, p := range []string{"/.hidden", "/empty/", "/empty", "/../secret.txt"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.URL.Path = p
		w := httptest.NewRecorder()
		serveFile(w, r, "testdata/bundle")
		if w.Code != 404 {
			t.Errorf("%s: %d, want 404", p, w.Code)
		}
	}
}

func TestBootstrap(t *testing.T) {
	var tok atomic.Value
	tok.Store(goodToken)
	hs, open := newBundle(t, "testdata/bundle", &tok)
	if resp := get(t, hs, "/_pitwall/bootstrap", ""); resp.StatusCode != 401 {
		t.Fatalf("no session: %d", resp.StatusCode)
	}
	ck := openSession(t, hs, open)
	resp := get(t, hs, "/_pitwall/bootstrap", ck)
	var b struct {
		URL      string
		Token    string
		Protocol int
	}
	if err := json.Unmarshal([]byte(body(resp)), &b); err != nil || resp.StatusCode != 200 {
		t.Fatalf("bootstrap: %d %v", resp.StatusCode, err)
	}
	if b.URL != "/ws" || b.Protocol != Protocol || !tokenRe.MatchString(b.Token) || b.Token == goodToken {
		t.Fatalf("bootstrap %+v", b)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("bootstrap Cache-Control %q", resp.Header.Get("Cache-Control"))
	}

	// The session's token says hello; a made-up one does not.
	c, _ := dial(t, hs, "", "")
	c.send(t, hi(b.Token, Protocol))
	if m := c.json(t); m["type"] != "welcome" {
		t.Fatalf("session token: %v", m)
	}
	c, _ = dial(t, hs, "", "")
	c.send(t, hi(strings.Repeat("0", 64), Protocol))
	if code, _ := c.closed(t); code != 4001 {
		t.Fatalf("made-up token: %d", code)
	}

	// A rotated web.json token ends the session: bundle, bootstrap and hello.
	tok.Store(strings.Repeat("1", 64))
	for _, p := range []string{"/", "/_pitwall/bootstrap"} {
		if resp := get(t, hs, p, ck); resp.StatusCode != 401 {
			t.Errorf("%s after rotate: %d", p, resp.StatusCode)
		}
	}
	c, _ = dial(t, hs, "", "")
	c.send(t, hi(b.Token, Protocol))
	if code, _ := c.closed(t); code != 4001 {
		t.Fatalf("session token after rotate: %d", code)
	}
}

func TestCSP(t *testing.T) {
	if got := CSP("127.0.0.1:8123"); !strings.Contains(got, "connect-src 'self' ws://127.0.0.1:8123 wss://127.0.0.1:8123;") {
		t.Errorf("CSP %q", got)
	}
	if got := CSP("evil; script-src *"); strings.Contains(got, "evil") {
		t.Errorf("CSP took a bad host: %q", got)
	}
}
