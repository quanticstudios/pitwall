package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		tag, cur string
		want     bool
	}{
		{"v0.1.0-alpha.10", "v0.1.0-alpha.9", true},
		{"v0.1.0-alpha.9", "v0.1.0-alpha.10", false},
		{"v0.1.0-alpha.18", "v0.1.0-alpha.18", false},
		{"v0.1.0", "v0.1.0-alpha.18", true},
		{"v0.1.0-beta.1", "v0.1.0-alpha.18", true},
		{"v0.1.0-alpha.1", "v0.0.9", true},
		{"v0.10.0", "v0.9.0", true},
		{"v1.0.0-alpha", "v1.0.0-alpha.1", false},
		{"not-a-tag", "v0.1.0-alpha.1", false},
		// Dev builds and git describe strings never have a newer release.
		{"v9.9.9", "dev", false},
		{"v9.9.9", "dev-0123456789ab", false},
		{"v9.9.9", "dev-0123456789ab-dirty", false},
		{"v9.9.9", "v0.1.0-alpha.18-3-g0123abc", false},
		{"v9.9.9", "v0.1.0-alpha.18-dirty", false},
		{"v9.9.9", "0123abc", false},
		{"v9.9.9", "", false},
	} {
		if got := Newer(c.tag, c.cur); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.tag, c.cur, got, c.want)
		}
	}
}

// release serves a latest-release JSON, checksums.txt and a tar.gz for
// this platform holding bin, with sums as checksums.txt when set.
func release(t *testing.T, bin []byte, sums func(name, sum string) string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, f := range []struct {
		name string
		data []byte
	}{{"LICENSE", []byte("MIT")}, {"pitwall", bin}} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
		tw.Write(f.data)
	}
	tw.Close()
	zw.Close()
	arc := buf.Bytes()
	name := fmt.Sprintf("pitwall_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(arc)
	var hits atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/latest":
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v0.1.0-alpha.10", "assets": []map[string]string{
				{"name": "checksums.txt", "browser_download_url": srv.URL + "/checksums.txt"},
				{"name": name, "browser_download_url": srv.URL + "/" + name},
			}})
		case "/checksums.txt":
			fmt.Fprint(w, sums(name, hex.EncodeToString(sum[:])))
		case "/" + name:
			w.Write(arc)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestInstall(t *testing.T) {
	if !Supported {
		t.Skip("no updates on " + runtime.GOOS)
	}
	ok := func(name, sum string) string { return "abc  pitwall_other.zip\n" + sum + "  " + name + "\n" }
	for _, c := range []struct {
		name string
		sums func(name, sum string) string
		want string // the binary after Install
	}{
		{"verified", ok, "new"},
		{"mismatch", func(name, _ string) string { return fmt.Sprintf("%064d  %s\n", 0, name) }, "old"},
		{"missing", func(string, string) string { return "abc  pitwall_other.zip\n" }, "old"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := release(t, []byte("new"), c.sums)
			ctx := context.Background()
			rel, newer, err := Check(ctx, srv.Client(), srv.URL+"/latest", "v0.1.0-alpha.9")
			if err != nil || !newer || rel.Tag != "v0.1.0-alpha.10" {
				t.Fatalf("Check = %+v, %v, %v", rel, newer, err)
			}
			dir := t.TempDir()
			exe := filepath.Join(dir, "pitwall")
			if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, "link")
			if err := os.Symlink(exe, link); err != nil {
				t.Fatal(err)
			}
			err = Install(ctx, srv.Client(), rel, link)
			if (err == nil) != (c.want == "new") {
				t.Fatalf("Install: %v", err)
			}
			got, _ := os.ReadFile(exe)
			if string(got) != c.want {
				t.Fatalf("binary = %q, want %q", got, c.want)
			}
			if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
				t.Fatal("the symlink was replaced instead of its target")
			}
			if fi, _ := os.Stat(exe); fi.Mode().Perm() != 0o755 {
				t.Fatalf("mode = %v", fi.Mode())
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 2 {
				t.Fatalf("left behind: %v", entries)
			}
		})
	}
}

func TestDevBuildNeverChecks(t *testing.T) {
	srv, hits := release(t, []byte("new"), func(string, string) string { return "" })
	for _, v := range []string{"dev", "dev-0123456789ab-dirty", "v0.1.0-alpha.9-2-g0123abc", "v0.1.0-alpha.9-dirty"} {
		if _, newer, err := Check(context.Background(), srv.Client(), srv.URL+"/latest", v); newer || err != nil {
			t.Errorf("Check(%q) = %v, %v", v, newer, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("dev builds made %d requests", n)
	}
}

func TestManaged(t *testing.T) {
	for exe, want := range map[string]bool{
		"/opt/homebrew/Cellar/pitwall/0.1.0/bin/pitwall":              true,
		"/home/linuxbrew/.linuxbrew/Cellar/pitwall/0.1.0/bin/pitwall": true,
		"/usr/bin/pitwall":           true,
		"/home/u/.local/bin/pitwall": false,
		"/Users/u/Applications/pitwall.app/Contents/MacOS/pitwall": false,
		"/usr/local/bin/pitwall":                                   false,
	} {
		if got := Managed(exe); got != want {
			t.Errorf("Managed(%q) = %v, want %v", exe, got, want)
		}
	}
}
