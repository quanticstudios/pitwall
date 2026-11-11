package remote

import (
	"bytes"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The phone reads a tab's markdown files and their images, and nothing
// else: no other kind of file, nothing outside the tab's folder by .., a
// symlink or an absolute path, nothing over the cap, and nothing
// without a device token.
func TestFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo")
	outside := filepath.Join(filepath.Dir(root), "secret.md")
	os.MkdirAll(filepath.Join(root, "docs"), 0o700)
	os.WriteFile(outside, []byte("# secret"), 0o600)
	os.WriteFile(filepath.Join(root, "docs", "a.md"), []byte("# A\n\n![i](i.png)"), 0o600)
	os.WriteFile(filepath.Join(root, "README.markdown"), []byte("# R"), 0o600)
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("plain"), 0o600)
	os.WriteFile(filepath.Join(root, "fake.png"), []byte("# not an image"), 0o600)
	os.WriteFile(filepath.Join(root, "big.md"), bytes.Repeat([]byte("x"), MaxFileBytes+1), 0o600)
	os.WriteFile(filepath.Join(root, "s.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="4" height="4"/>`), 0o600)
	var pb bytes.Buffer
	png.Encode(&pb, image.NewRGBA(image.Rect(0, 0, 2, 2)))
	os.WriteFile(filepath.Join(root, "docs", "i.png"), pb.Bytes(), 0o600)
	links := runtime.GOOS != "windows"
	if links {
		os.Symlink(outside, filepath.Join(root, "out.md"))
		os.Symlink(filepath.Join("docs", "a.md"), filepath.Join(root, "in.md")) // os.Root follows relative links only
	}

	s, _ := newServer(t)
	s.Tabs = func() []Tab { return nil }
	s.Folder = func(tab string) (string, string, bool) {
		return filepath.Join(root, "docs"), root, tab == "w1"
	}
	h := s.Handler()
	token := pairDevice(t, s, h, "phone")
	get := func(tab, p, tok string) (int, string) {
		t.Helper()
		return call(t, h, "GET", "/api/file?"+url.Values{"tab": {tab}, "path": {p}}.Encode(), tok, "")
	}

	for _, c := range []struct {
		name, tab, path string
		code            int
		body            string // a prefix
	}{
		{"relative to the cwd", "w1", "a.md", 200, "# A"},
		{"up to the repo", "w1", "../README.markdown", 200, "# R"},
		{"absolute inside", "w1", filepath.Join(root, "docs", "a.md"), 200, "# A"},
		{"image by content", "w1", "i.png", 200, "\x89PNG"},
		{"svg", "w1", "../s.svg", 200, "<svg"},
		{"up out of the repo", "w1", "../../secret.md", 403, errOutside.Error()},
		{"absolute outside", "w1", outside, 403, errOutside.Error()},
		{"not markdown", "w1", "../notes.txt", 403, errKind.Error()},
		{"not an image", "w1", "../fake.png", 403, errImage.Error()},
		{"over the cap", "w1", "../big.md", 413, errLarge.Error()},
		{"missing", "w1", "gone.md", 404, "no such file"},
		{"no such tab", "w2", "a.md", 404, "that tab"},
		{"no path", "w1", "", 403, errOutside.Error()},
	} {
		if code, body := get(c.tab, c.path, token); code != c.code || !strings.HasPrefix(body, c.body) {
			t.Errorf("%s: %d %q", c.name, code, body)
		}
	}
	if links {
		if code, body := get("w1", "../out.md", token); code != 403 || !strings.HasPrefix(body, errOutside.Error()) {
			t.Errorf("symlink out: %d %q", code, body)
		}
		if code, body := get("w1", "../in.md", token); code != 200 || body != "# A\n\n![i](i.png)" {
			t.Errorf("symlink in: %d %q", code, body)
		}
	}
	if code, _ := get("w1", "a.md", ""); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, _ := get("w1", "a.md", "bad"); code != 401 {
		t.Errorf("bad token: %d", code)
	}
}

// A link is no way to another file: a symlink named like markdown that
// ends at another kind of file, a hard link of a file outside the
// folder, markdown or image, and a folder replaced after the first read
// are all refused, and an SVG oksvg could not draw safely is never sent.
func TestFileLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "home", "repo")
	other := filepath.Join(dir, "other", "repo")
	os.MkdirAll(root, 0o700)
	os.MkdirAll(other, 0o700)
	os.WriteFile(filepath.Join(root, "a.md"), []byte("# A"), 0o600)
	os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# agents"), 0o600)
	os.WriteFile(filepath.Join(root, "secret.txt"), []byte("token"), 0o600)
	os.Symlink("secret.txt", filepath.Join(root, "alias.md"))
	os.Symlink("AGENTS.md", filepath.Join(root, "CLAUDE.md"))
	os.WriteFile(filepath.Join(dir, "id_key"), []byte("key"), 0o600)
	os.WriteFile(filepath.Join(dir, "id.png"), []byte("\x89PNG\r\n\x1a\nkey"), 0o600)
	if err := os.Link(filepath.Join(dir, "id_key"), filepath.Join(root, "hard.md")); err != nil {
		t.Fatal(err)
	}
	os.Link(filepath.Join(dir, "id.png"), filepath.Join(root, "hard.png"))
	os.WriteFile(filepath.Join(root, "use.svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="4" height="4"><use href="#x"/></svg>`), 0o600)
	os.WriteFile(filepath.Join(other, "a.md"), []byte("# other"), 0o600)

	s, _ := newServer(t)
	s.Tabs = func() []Tab { return nil }
	s.Folder = func(tab string) (string, string, bool) { return root, root, tab == "w1" }
	h := s.Handler()
	token := pairDevice(t, s, h, "phone")
	get := func(p string) (int, string) {
		t.Helper()
		return call(t, h, "GET", "/api/file?"+url.Values{"tab": {"w1"}, "path": {p}}.Encode(), token, "")
	}
	for _, c := range []struct {
		path string
		code int
		body string // a prefix
	}{
		{"a.md", 200, "# A"},
		{"alias.md", 403, errKind.Error()},
		{"CLAUDE.md", 200, "# agents"},
		{"hard.md", 403, errLinked.Error()},
		{"hard.png", 403, errLinked.Error()},
		{"use.svg", 403, "SVG has a <use>"},
	} {
		if code, body := get(c.path); code != c.code || !strings.HasPrefix(body, c.body) {
			t.Errorf("%s: %d %q", c.path, code, body)
		}
	}

	// The repo's parent moves away and a symlink to another folder with
	// a repo/a.md takes its place.
	home := filepath.Dir(root)
	if err := os.Rename(home, home+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(other), home); err != nil {
		t.Fatal(err)
	}
	if code, body := get("a.md"); code != 403 || !strings.HasPrefix(body, errMoved.Error()) {
		t.Errorf("replaced folder: %d %q", code, body)
	}
}
