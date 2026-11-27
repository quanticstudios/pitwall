package remote

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/web"
)

func TestCSPAllowsBlobAndAttachmentStyled(t *testing.T) {
	_, h := newServer(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data: blob:;") {
		t.Fatalf("CSP missing blob: in img-src: %q", csp)
	}

	st, css := call(t, h, "GET", "/app.css", "", "")
	if st != 200 || !strings.Contains(css, "#attachment {") || !strings.Contains(css, "#attachment[hidden]") {
		t.Fatalf("app.css (%d) missing #attachment rules", st)
	}
}

func TestSameOriginAllEndpoints(t *testing.T) {
	s, _ := newServer(t)
	bundle := t.TempDir()
	os.WriteFile(filepath.Join(bundle, "index.html"), []byte("ok"), 0o644)
	s.Web = &web.Client{Dir: bundle, Live: func(id string) bool { return Paired(s.Dir, id) }}
	s.WS = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ws") })
	s.Approve = func(string, string, string, string) error { return nil }
	s.AnswerInput = func(string, string, map[string][]string) error { return nil }
	s.NewOptions = func(string) NewOptions { return NewOptions{} }
	s.Start = func(NewTab) (string, string, error) { return "", "", nil }
	s.CloseTab = func(string) error { return nil }
	s.Restart = func(string) error { return nil }
	h := s.Handler()
	token := pairDevice(t, s, h, "phone")

	// Start a web session for /_pitwall/bootstrap.
	webReq := httptest.NewRequest("POST", "/api/web", nil)
	webReq.Header.Set("Authorization", "Bearer "+token)
	webRec := httptest.NewRecorder()
	h.ServeHTTP(webRec, webReq)
	cks := webRec.Result().Cookies()
	if webRec.Code != 200 || len(cks) != 1 {
		t.Fatalf("POST /api/web: %d %v", webRec.Code, cks)
	}
	cookie := cks[0].Name + "=" + cks[0].Value

	routes := []struct {
		method, path, body string
		useCookie          bool
	}{
		{"POST", "/api/pair", `{"code":"AAAA"}`, false},
		{"GET", "/api/items", "", false},
		{"POST", "/api/web", "", false},
		{"GET", "/_pitwall/bootstrap", "", true},
		{"GET", "/api/push", "", false},
		{"POST", "/api/push", `{}`, false},
		{"POST", "/api/answer", `{"pane":"p1","at":"0","allow":true}`, false},
		{"POST", "/api/reply", `{"pane":"p1","at":"0","text":"hi"}`, false},
		{"POST", "/api/approve", `{"chat":"c","request":"r","decision":"allow"}`, false},
		{"POST", "/api/input", `{"chat":"c","request":"r","answers":{}}`, false},
		{"GET", "/api/new", "", false},
		{"POST", "/api/new", `{"dir":"/","agent":"terminal"}`, false},
		{"POST", "/api/close", `{"tab":"w"}`, false},
		{"POST", "/api/restart", `{"pane":"p"}`, false},
	}
	for _, rt := range routes {
		r := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
		r.Header.Set("Origin", "https://evil.example")
		if rt.useCookie {
			r.Header.Set("Cookie", cookie)
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s with foreign Origin: %d, want 403", rt.method, rt.path, w.Code)
		}

		// Same origin passes sameOrigin (may still return 400/401 on bad body/code, never 403).
		r2 := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
		r2.Header.Set("Origin", "http://example.com")
		if rt.useCookie {
			r2.Header.Set("Cookie", cookie)
		} else {
			r2.Header.Set("Authorization", "Bearer "+token)
		}
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r2)
		if w2.Code == http.StatusForbidden {
			t.Errorf("%s %s with same Origin rejected with 403", rt.method, rt.path)
		}
	}
}

func TestRateLimitPartitionedByIP(t *testing.T) {
	s, h := newServer(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	token := pairDevice(t, s, h, "good-phone")

	reqFrom := func(addr, method, path, tok, body string) int {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.RemoteAddr = addr
		if tok != "" {
			r.Header.Set("Authorization", "Bearer "+tok)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	// Exhaust maxFails from attacker IP A (varying ports on the same IP).
	for i := range maxFails {
		code := reqFrom("203.0.113.10:100"+string(rune('0'+i%10)), "POST", "/api/pair", "", `{"code":"XXXX"}`)
		if code != http.StatusUnauthorized {
			t.Fatalf("fail %d: %d, want 401", i, code)
		}
	}
	if code := reqFrom("203.0.113.10:9999", "GET", "/api/items", token, ""); code != http.StatusTooManyRequests {
		t.Fatalf("locked IP /api/items: %d, want 429", code)
	}
	if code := reqFrom("203.0.113.10:9999", "POST", "/api/pair", "", `{"code":"XXXX"}`); code != http.StatusTooManyRequests {
		t.Fatalf("locked IP /api/pair: %d, want 429", code)
	}

	// Legitimate device on IP B is unaffected by IP A's lockout.
	if code := reqFrom("198.51.100.7:4321", "GET", "/api/items", token, ""); code != http.StatusOK {
		t.Fatalf("other IP /api/items: %d, want 200", code)
	}
	validCode, err := Pair(s.Dir, "second-phone", now)
	if err != nil {
		t.Fatal(err)
	}
	if code := reqFrom("198.51.100.7:4322", "POST", "/api/pair", "", `{"code":"`+validCode+`"}`); code != http.StatusOK {
		t.Fatalf("other IP /api/pair: %d, want 200", code)
	}

	// IP A recovers after failWindow.
	now = now.Add(failWindow)
	if code := reqFrom("203.0.113.10:9999", "GET", "/api/items", token, ""); code != http.StatusOK {
		t.Fatalf("IP A after window: %d, want 200", code)
	}
}
