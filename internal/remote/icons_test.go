package remote

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestIcons serves a repo's icon by its hash to a paired device alone,
// with an ETag a second fetch gets a 304 for. A repo whose icon files are
// not images, or none, gets NoIcon, and a folder outside a repo "". No
// path reaches a file: only hashes of files repoicon lists are served.
func TestIcons(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	png := append([]byte("\x89PNG\r\n\x1a\n"), "pixels"...)
	for name, data := range map[string][]byte{
		"repo/.git/HEAD":           []byte("ref: refs/heads/main\n"),
		"repo/public/favicon.png":  png,
		"repo/src/x.go":            []byte("package x"),
		"fake/.git/HEAD":           []byte("ref: refs/heads/main\n"),
		"fake/favicon.png":         []byte("not an image"),
		"fake/favicon.svg":         []byte("<html><script>alert(1)</script></html>"),
		"bare/.git/HEAD":           []byte("ref: refs/heads/main\n"),
		"outside/favicon.png":      png,
		"repo/.pitwall/secret.txt": []byte("token"),
		"text/.git/HEAD":           []byte("ref: refs/heads/main\n"),
		"text/favicon.svg":         []byte(`{"role":"user","text":"draw <svg width=1>"}`),
		"svg/.git/HEAD":            []byte("ref: refs/heads/main\n"),
		"svg/favicon.svg":          []byte(svgIcon),
		"link/.git/HEAD":           []byte("ref: refs/heads/main\n"),
		"linkdir/.git/HEAD":        []byte("ref: refs/heads/main\n"),
	} {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := newServer(t)
	now := time.Now()
	s.Icons = &Icons{now: func() time.Time { return now }}
	h := s.Handler()
	token := pairDevice(t, s, h, "pixel")

	hash := s.Icons.Of(filepath.Join(repo, "src"))
	if len(hash) != 24 {
		t.Fatalf("a repo with a PNG favicon: icon %q, want a hash", hash)
	}
	if got := s.Icons.Of(filepath.Join(root, "svg")); len(got) != 24 {
		t.Errorf("a repo with an SVG favicon: icon %q, want a hash", got)
	}
	for dir, want := range map[string]string{filepath.Join(root, "text"): NoIcon, filepath.Join(root, "fake"): NoIcon, filepath.Join(root, "bare"): NoIcon, filepath.Join(root, "outside"): ""} {
		if got := s.Icons.Of(dir); got != want {
			t.Errorf("%s: icon %q, want %q", filepath.Base(dir), got, want)
		}
	}

	if st, _ := call(t, h, "GET", "/api/icon/"+hash, "", ""); st != http.StatusUnauthorized {
		t.Fatalf("no token: %d, want 401", st)
	}
	r := httptest.NewRequest("GET", "/api/icon/"+hash, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != string(png) || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("ETag") != `"`+hash+`"` {
		t.Fatalf("icon: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	r.Header.Set("If-None-Match", `"`+hash+`"`)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("If-None-Match: %d with %d bytes, want 304 and none", w.Code, w.Body.Len())
	}

	for _, p := range []string{
		"/api/icon/" + NoIcon,
		"/api/icon/0123456789abcdef01234567",
		"/api/icon/..%2F..%2Frepo%2F.pitwall%2Fsecret.txt",
		"/api/icon/%2Fetc%2Fpasswd",
		"/api/icon/../../etc/passwd",
	} {
		if st, body := call(t, h, "GET", p, token, ""); st == 200 {
			t.Errorf("%s: 200 %q, want no file", p, body)
		}
	}

	// An icon that is a symlink out of its repo, to a private file, is not
	// read, nor one under a folder that is.
	if err := os.Symlink(filepath.Join(root, "outside/favicon.png"), filepath.Join(root, "link/favicon.png")); err != nil {
		t.Log("no symlinks here:", err)
	} else {
		if got := s.Icons.Of(filepath.Join(root, "link")); got != NoIcon {
			t.Errorf("a symlink out of the repo: icon %q, want %q", got, NoIcon)
		}
		os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "linkdir/public"))
		if got := s.Icons.Of(filepath.Join(root, "linkdir")); got != NoIcon {
			t.Errorf("a symlinked folder out of the repo: icon %q, want %q", got, NoIcon)
		}
	}

	// A new icon in the repo shows once the folder is read again.
	os.WriteFile(filepath.Join(repo, "public/favicon.png"), append(png, '2'), 0o644)
	if s.Icons.Of(filepath.Join(repo, "src")) != hash {
		t.Fatal("the icon was read again before iconRecheck")
	}
	now = now.Add(iconRecheck)
	if s.Icons.Of(filepath.Join(repo, "src")) == hash {
		t.Fatal("a changed icon kept its hash after iconRecheck")
	}
}

const svgIcon = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">
<!-- an icon -->
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><title>a&nbsp;b</title><circle cx="8" cy="8" r="7"/></svg>
`
