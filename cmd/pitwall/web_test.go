package main

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/web"
)

func TestRunWeb(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	run := func(args ...string) (int, string) {
		var out, errs strings.Builder
		code := runWeb(args, &out, &errs)
		return code, out.String() + errs.String()
	}
	if code, out := run("token"); code != 2 || !strings.Contains(out, "usage") {
		t.Fatalf("token: %d %q", code, out)
	}
	if code, out := run("token", "--rotate"); code != 0 || !strings.Contains(out, web.Path()) {
		t.Fatalf("rotate: %d %q", code, out)
	}
	first, err := web.Read(web.Path())
	if err != nil || len(first.Token) != 64 {
		t.Fatalf("web.json %+v %v", first, err)
	}
	run("token", "--rotate")
	if f, _ := web.Read(web.Path()); f.Token == first.Token {
		t.Fatal("rotate kept the token")
	}
}

// web open opens a one-time link, never the token, that gets the browser
// the bundle at /.
func TestWebOpen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	var opened []string
	defer func(old func(string) error) { openURL = old }(openURL)
	openURL = func(u string) error { opened = append(opened, u); return nil }
	run := func() (int, string) {
		var out, errs strings.Builder
		code := runWeb([]string{"open"}, &out, &errs)
		return code, out.String() + errs.String()
	}
	if code, out := run(); code != 1 || !strings.Contains(out, "bundle") {
		t.Fatalf("no config: %d %q", code, out)
	}
	bundle := filepath.Join(dir, "bundle")
	os.MkdirAll(bundle, 0o755)
	os.WriteFile(filepath.Join(bundle, "index.html"), []byte("fixture"), 0o644)
	os.MkdirAll(config.Dir(), 0o755)
	os.WriteFile(config.Path(), []byte("[web]\nenabled = true\nbundle = \""+filepath.ToSlash(bundle)+"\"\n"), 0o644)
	if code, out := run(); code != 1 || !strings.Contains(out, "has not started") {
		t.Fatalf("no web.json: %d %q", code, out)
	}

	ln, err := web.Listen(0)
	if err != nil {
		t.Fatal(err)
	}
	f, err := web.Publish(web.Path(), ln.Addr(), os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	ws := &web.Server{
		Token:    func() (string, error) { return f.Token, nil },
		Client:   &web.Client{Dir: bundle, Live: func(o string) bool { return o == f.Token }},
		OpenPath: web.OpenPath(web.Path()),
	}
	srv := &http.Server{Handler: ws.Handler()}
	go srv.Serve(ln)
	defer srv.Close()

	code, out := run()
	if code != 0 || len(opened) != 1 || !strings.Contains(out, "Opened http://"+ln.Addr().String()+"/") {
		t.Fatalf("open: %d %q %v", code, out, opened)
	}
	if strings.Contains(opened[0], f.Token) || strings.Contains(out, "code=") {
		t.Fatalf("the token is in the link, or the code in the output: %q %q", opened[0], out)
	}
	jar, _ := cookiejar.New(nil)
	resp, err := (&http.Client{Jar: jar}).Get(opened[0])
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "fixture" || strings.Contains(resp.Request.URL.String(), "code") {
		t.Fatalf("link: %d %q at %s", resp.StatusCode, b, resp.Request.URL)
	}
	if resp, _ := http.Get(opened[0]); resp.StatusCode != 401 {
		t.Fatalf("link's second use: %d", resp.StatusCode)
	}
}
