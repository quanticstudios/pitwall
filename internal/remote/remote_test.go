package remote

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
)

// call makes one request to h and returns the status and body.
func call(t *testing.T, h http.Handler, method, path, token, body string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	b, _ := io.ReadAll(w.Result().Body)
	return w.Code, string(b)
}

func newServer(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	s := &Server{
		Dir:    t.TempDir(),
		Items:  func() []Item { return []Item{{Pane: "p1", Detail: "Run the tests"}} },
		Answer: func(string, int64, bool) error { return nil },
		Reply:  func(string, int64, string) error { return nil },
	}
	return s, s.Handler()
}

func pairDevice(t *testing.T, s *Server, h http.Handler, name string) (token string) {
	t.Helper()
	code, err := Pair(s.Dir, name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	st, body := call(t, h, "POST", "/api/pair", "", `{"code":"`+code+`"}`)
	if st != 200 {
		t.Fatalf("pair: %d %s", st, body)
	}
	var r struct{ Token, Device string }
	json.Unmarshal([]byte(body), &r)
	if r.Token == "" || r.Device == "" {
		t.Fatalf("pair reply %s", body)
	}
	return r.Token
}

func TestPairThenToken(t *testing.T) {
	s, h := newServer(t)
	if st, _ := call(t, h, "GET", "/api/items", "", ""); st != 401 {
		t.Fatalf("no token: %d", st)
	}
	code, err := Pair(s.Dir, "pixel", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token := redeemCode(t, h, code)
	st, body := call(t, h, "GET", "/api/items", token, "")
	if st != 200 || !strings.Contains(body, "Run the tests") || !strings.Contains(body, `"device":"pixel"`) {
		t.Fatalf("items: %d %s", st, body)
	}
	// The code works once.
	if st, _ := call(t, h, "POST", "/api/pair", "", `{"code":"`+code+`"}`); st != 401 {
		t.Fatalf("second use of a code: %d", st)
	}
	// Neither the token nor the code is on disk, only their hashes.
	filepath.WalkDir(s.Dir, func(p string, e os.DirEntry, err error) error {
		if e != nil && !e.IsDir() {
			data, _ := os.ReadFile(p)
			_, secret, _ := strings.Cut(token, ".")
			if strings.Contains(string(data), secret) || strings.Contains(string(data), code) {
				t.Errorf("%s holds a secret", p)
			}
			if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode %v", p, fi.Mode().Perm())
			}
		}
		return nil
	})
	// An expired code pairs nothing.
	code, _ = Pair(s.Dir, "old", time.Now().Add(-PairTTL-time.Second))
	if st, _ := call(t, h, "POST", "/api/pair", "", `{"code":"`+code+`"}`); st != 401 {
		t.Fatalf("expired code: %d", st)
	}
	// A taken name gets a number.
	pairDevice(t, s, h, "pixel")
	devs, _ := Devices(s.Dir)
	if len(devs) != 2 || devs[0].Name != "pixel" || devs[1].Name != "pixel 2" {
		t.Fatalf("devices %+v", devs)
	}
}

func redeemCode(t *testing.T, h http.Handler, code string) string {
	t.Helper()
	st, body := call(t, h, "POST", "/api/pair", "", `{"code":"`+code+`"}`)
	if st != 200 {
		t.Fatalf("pair: %d %s", st, body)
	}
	var r struct{ Token string }
	json.Unmarshal([]byte(body), &r)
	return r.Token
}

func TestBadTokenLimited(t *testing.T) {
	s, h := newServer(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	token := pairDevice(t, s, h, "")
	id, _, _ := strings.Cut(token, ".")
	for i, bad := range []string{"nope", id + ".wrong", "../../x.y", token + "x", strings.ToUpper(token)} {
		if st, _ := call(t, h, "GET", "/api/items", bad, ""); st != 401 {
			t.Fatalf("bad token %d: %d", i, st)
		}
	}
	for range maxFails - 5 {
		call(t, h, "POST", "/api/pair", "", `{"code":"AAAA"}`)
	}
	// Now every attempt is refused, the good token too, until a minute
	// has passed since the oldest failure.
	if st, _ := call(t, h, "GET", "/api/items", token, ""); st != 429 {
		t.Fatalf("good token while limited: %d", st)
	}
	if st, _ := call(t, h, "POST", "/api/pair", "", `{"code":"AAAA"}`); st != 429 {
		t.Fatalf("pairing while limited: %d", st)
	}
	now = now.Add(failWindow)
	if st, _ := call(t, h, "GET", "/api/items", token, ""); st != 200 {
		t.Fatalf("good token after the window: %d", st)
	}
}

func TestRevoke(t *testing.T) {
	s, h := newServer(t)
	a := pairDevice(t, s, h, "a")
	b := pairDevice(t, s, h, "b")
	if _, err := Revoke(s.Dir, "a"); err != nil {
		t.Fatal(err)
	}
	if st, _ := call(t, h, "GET", "/api/items", a, ""); st != 401 {
		t.Fatalf("revoked token: %d", st)
	}
	if st, _ := call(t, h, "POST", "/api/answer", a, `{"pane":"p1","allow":true}`); st != 401 {
		t.Fatalf("revoked token answers: %d", st)
	}
	if st, _ := call(t, h, "GET", "/api/items", b, ""); st != 200 {
		t.Fatalf("other device: %d", st)
	}
	devs, _ := Devices(s.Dir)
	if _, err := Revoke(s.Dir, devs[0].ID); err != nil {
		t.Fatal(err)
	}
	if st, _ := call(t, h, "GET", "/api/items", b, ""); st != 401 {
		t.Fatalf("token revoked by id: %d", st)
	}
	if _, err := Revoke(s.Dir, "a"); err == nil {
		t.Fatal("revoked a device twice")
	}
}

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"fix the tests":                   "fix the tests",
		"a\x1b[201~\x03rm -rf /\x1b[200~": "a[201~rm -rf /[200~",
		"line one\r\nline two\rthree":     "line one\nline two\nthree",
		"tab\there\x7f\u009b31m\x00":      "tab here31m",
		"  \x1b  ":                        "",
		"bad \xff utf8":                   "bad  utf8",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPage(t *testing.T) {
	_, h := newServer(t)
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `<script src="app.js">`) ||
		!strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatalf("page: %d %v", w.Code, w.Header())
	}
	if st, body := call(t, h, "GET", "/app.js", "", ""); st != 200 || !strings.Contains(body, "textContent") || strings.Contains(body, "innerHTML") {
		t.Fatalf("app.js: %d", st)
	}
}

func TestItems(t *testing.T) {
	now := time.Now()
	st := model.State{
		Sessions:   []model.Session{{ID: "s", Name: "work"}},
		Workspaces: []model.Workspace{{ID: "w", SessionID: "s", Label: "fix auth"}},
		Activities: []model.Activity{
			{PaneID: "a", WorkspaceID: "w", Provider: model.ProviderClaude, State: model.StateWorking, UpdatedAt: now},
			{PaneID: "b", WorkspaceID: "w", Provider: model.ProviderCodex, State: model.StateCompleted, Detail: "done", UpdatedAt: now},
			{PaneID: "c", WorkspaceID: "w", Provider: model.ProviderClaude, State: model.StatePendingApproval, Detail: "Push",
				AdviceRule: "sudo", Advice: "deny", AdviceP: 0.91, UpdatedAt: now},
			{PaneID: "d", WorkspaceID: "w", Provider: model.ProviderPi, State: model.StatePendingApproval, UpdatedAt: now},
		},
	}
	got := Items(st)
	if len(got) != 3 || got[0].Pane != "c" || got[2].Pane != "b" {
		t.Fatalf("items %+v", got)
	}
	c := got[0]
	if !c.Answer || c.Reply || c.Risk != "sudo" || c.Advice != "deny 91%" || c.Tab != "fix auth" || c.Session != "work" || c.At != now.UnixNano() {
		t.Errorf("approval %+v", c)
	}
	if got[1].Answer { // no known prompt keys for pi
		t.Errorf("pi approval %+v", got[1])
	}
	if b := got[2]; b.Answer || !b.Reply || b.Label != "Done" {
		t.Errorf("done %+v", b)
	}
}

func TestCertAndURL(t *testing.T) {
	dir := t.TempDir()
	_, fp, err := Cert(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, again, err := Cert(dir); err != nil || again != fp || len(fp) != 95 {
		t.Fatalf("second load %q, %v; first %q", again, err, fp)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "tls.pem")); fi.Mode().Perm() != 0o600 {
		t.Errorf("tls.pem mode %v", fi.Mode().Perm())
	}
	for _, tc := range []struct {
		s    config.RemoteSettings
		want string
	}{
		{config.RemoteSettings{Listen: "127.0.0.1:7777"}, "http://127.0.0.1:7777/"},
		{config.RemoteSettings{Listen: "127.0.0.1:7777", URL: "https://box.ts.net/"}, "https://box.ts.net/"},
		{config.RemoteSettings{Listen: "[::1]:9000", TLS: true}, "https://[::1]:9000/"},
	} {
		if got := URL(tc.s); got != tc.want {
			t.Errorf("URL(%+v) = %q, want %q", tc.s, got, tc.want)
		}
	}
	if got := URL(config.RemoteSettings{Listen: "0.0.0.0:7777", TLS: true}); strings.Contains(got, "0.0.0.0") || !strings.HasPrefix(got, "https://") {
		t.Errorf("unspecified host: %q", got)
	}
	if got := PairURL(config.RemoteSettings{Listen: "127.0.0.1:7777"}, "ABC"); got != "http://127.0.0.1:7777/#pair=ABC" {
		t.Errorf("PairURL = %q", got)
	}
}
