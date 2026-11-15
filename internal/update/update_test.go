package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		{"v9.9.9", "v0.1.1-dirty", false},
		{"v9.9.9", "v0.1.1-3-g0123abc", false},
		{"v0.1.1-dirty", "v0.1.0", false},
		{"v9.9.9", "0123abc", false},
		{"v9.9.9", "", false},
	} {
		if got := Newer(c.tag, c.cur); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.tag, c.cur, got, c.want)
		}
	}
}

// release serves a latest-release redirect, checksums.txt and flavor's
// archive for goos and this GOARCH holding bin, with sums as checksums.txt.
func release(t *testing.T, flavor, goos string, bin []byte, sums func(name, sum string) string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var buf bytes.Buffer
	name, member, err := asset(flavor, goos, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	if goos == "windows" {
		zw := zip.NewWriter(&buf)
		for _, f := range []struct {
			name string
			data []byte
		}{{"LICENSE", []byte("MIT")}, {member, bin}} {
			w, _ := zw.Create(f.name)
			w.Write(f.data)
		}
		zw.Close()
	} else {
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		for _, f := range []struct {
			name string
			data []byte
		}{{"LICENSE", []byte("MIT")}, {member, bin}} {
			tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.data)), Typeflag: tar.TypeReg})
			tw.Write(f.data)
		}
		tw.Close()
		zw.Close()
	}
	arc := buf.Bytes()
	sum := sha256.Sum256(arc)
	var hits atomic.Int32
	const dl = "/quanticstudios/pitwall/releases/download/v0.1.0-alpha.10/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/latest":
			http.Redirect(w, r, "/quanticstudios/pitwall/releases/tag/v0.1.0-alpha.10", http.StatusFound)
		case dl + "checksums.txt":
			fmt.Fprint(w, sums(name, hex.EncodeToString(sum[:])))
		case dl + name:
			w.Write(arc)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestInstall installs a release's tar.gz the Unix way, and its zip the
// Windows way: the old binary set aside, then removed on the next start.
func TestInstall(t *testing.T) {
	ok := func(name, sum string) string {
		return "abc  pitwall-phone.apk\nabc  pitwall_other.zip\n" + sum + "  " + name + "\n"
	}
	for _, goos := range []string{"linux", "windows"} {
		for _, c := range []struct {
			name string
			sums func(name, sum string) string
			want string // the binary after Install
		}{
			{"verified", ok, "new"},
			{"mismatch", func(name, _ string) string { return fmt.Sprintf("%064d  %s\n", 0, name) }, "old"},
			{"missing", func(string, string) string { return "abc  pitwall_other.zip\n" }, "old"},
		} {
			t.Run(goos+"/"+c.name, func(t *testing.T) {
				srv, _ := release(t, "pitwall", goos, []byte("new"), c.sums)
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
				// Windows lets only some accounts make symlinks.
				link, files := filepath.Join(dir, "link"), 2
				if err := os.Symlink(exe, link); err != nil && runtime.GOOS == "windows" {
					link, files = exe, 1
				} else if err != nil {
					t.Fatal(err)
				}
				err = install(ctx, srv.Client(), rel, link, "pitwall", goos, runtime.GOARCH)
				if (err == nil) != (c.want == "new") {
					t.Fatalf("Install: %v", err)
				}
				got, _ := os.ReadFile(exe)
				if string(got) != c.want {
					t.Fatalf("binary = %q, want %q", got, c.want)
				}
				if fi, _ := os.Lstat(link); link != exe && fi.Mode()&os.ModeSymlink == 0 {
					t.Fatal("the symlink was replaced instead of its target")
				}
				if fi, _ := os.Stat(exe); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755 {
					t.Fatalf("mode = %v", fi.Mode())
				}
				if goos == "windows" && c.want == "new" {
					if old, _ := os.ReadFile(exe + ".old"); string(old) != "old" {
						t.Fatalf("set aside %q", old)
					}
					RemoveOld(link)
				}
				if entries, _ := os.ReadDir(dir); len(entries) != files {
					t.Fatalf("left behind: %v", entries)
				}
			})
		}
	}
}

// TestInstallAssetNames: each flavor asks for its own archive and takes its
// own binary out of it; pitwall-core outside Linux fails before a request.
func TestInstallAssetNames(t *testing.T) {
	for _, c := range []struct {
		bin, goos, goarch, name, member string
	}{
		{"pitwall", "linux", "amd64", "pitwall_linux_amd64.tar.gz", "pitwall"},
		{"pitwall", "linux", "arm64", "pitwall_linux_arm64.tar.gz", "pitwall"},
		{"pitwall", "darwin", "arm64", "pitwall_darwin_arm64.tar.gz", "pitwall"},
		{"pitwall", "windows", "amd64", "pitwall_windows_amd64.zip", "pitwall.exe"},
		{"pitwall-core", "linux", "amd64", "pitwall-core_linux_amd64.tar.gz", "pitwall-core"},
		{"pitwall-core", "linux", "arm64", "pitwall-core_linux_arm64.tar.gz", "pitwall-core"},
		{"pitwall-core", "darwin", "arm64", "", ""},
		{"pitwall-core", "windows", "amd64", "", ""},
	} {
		name, member, err := asset(c.bin, c.goos, c.goarch)
		if name != c.name || member != c.member || (err == nil) != (c.name != "") {
			t.Errorf("asset(%s, %s, %s) = %q, %q, %v", c.bin, c.goos, c.goarch, name, member, err)
		}
	}
	for _, goos := range []string{"darwin", "windows"} {
		srv, hits := release(t, "pitwall", goos, []byte("new"), func(name, sum string) string { return sum + "  " + name + "\n" })
		rel := Release{Tag: "v0.1.0-alpha.10", Base: srv.URL + "/quanticstudios/pitwall/releases/download/v0.1.0-alpha.10/"}
		if err := install(context.Background(), srv.Client(), rel, filepath.Join(t.TempDir(), "pitwall-core"), "pitwall-core", goos, runtime.GOARCH); err == nil || hits.Load() != 0 {
			t.Errorf("pitwall-core on %s: %v after %d requests", goos, err, hits.Load())
		}
	}
	for _, flavor := range []string{"pitwall", "pitwall-core"} {
		t.Run(flavor, func(t *testing.T) {
			srv, _ := release(t, flavor, "linux", []byte(flavor+" new"), func(name, sum string) string { return sum + "  " + name + "\n" })
			rel := Release{Tag: "v0.1.0-alpha.10", Base: srv.URL + "/quanticstudios/pitwall/releases/download/v0.1.0-alpha.10/"}
			exe := filepath.Join(t.TempDir(), flavor)
			if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := install(context.Background(), srv.Client(), rel, exe, flavor, "linux", runtime.GOARCH); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(exe); string(got) != flavor+" new" {
				t.Fatalf("binary = %q", got)
			}
			// The other flavor's archive is not in this release: nothing changes.
			other := map[string]string{"pitwall": "pitwall-core", "pitwall-core": "pitwall"}[flavor]
			if err := install(context.Background(), srv.Client(), rel, exe, other, "linux", runtime.GOARCH); err == nil {
				t.Fatalf("installed %s from a release with only %s", other, flavor)
			}
			if got, _ := os.ReadFile(exe); string(got) != flavor+" new" {
				t.Fatalf("after a failed install binary = %q", got)
			}
		})
	}
}

// TestInstallConcurrent: two installs to one path at once, as the window's
// Update button and pitwall update, each rename a whole binary over it and
// leave no temporary file.
func TestInstallConcurrent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows swaps the binary aside; TestSwapPastBusyOld covers it")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "pitwall")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	big := func(c byte) []byte { return bytes.Repeat([]byte{c}, 8<<20) }
	var rels [2]Release
	var clients [2]*http.Client
	for i, c := range []byte{'a', 'b'} {
		srv, _ := release(t, "pitwall", runtime.GOOS, big(c), func(name, sum string) string { return sum + "  " + name + "\n" })
		rels[i] = Release{Tag: "v0.1.0-alpha.10", Base: srv.URL + "/quanticstudios/pitwall/releases/download/v0.1.0-alpha.10/"}
		clients[i] = srv.Client()
	}
	errs := make(chan error, 2)
	for i := range 2 {
		go func() {
			errs <- install(context.Background(), clients[i], rels[i], exe, "pitwall", runtime.GOOS, runtime.GOARCH)
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, big('a')) && !bytes.Equal(got, big('b')) {
		t.Fatalf("the binary is a mix of both installs (%d bytes)", len(got))
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
}

// TestSwapPastBusyOld: an older binary that cannot be removed, as one a
// daemon still runs, gets the next aside name, and RemoveOld clears the
// rest.
func TestSwapPastBusyOld(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "pitwall.exe")
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(data), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write(exe, "v2")
	// A directory stands for the running binary: Remove fails on it.
	if err := os.MkdirAll(filepath.Join(exe+".old", "busy"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(exe+".old1", "v0")
	write(filepath.Join(dir, "next"), "v3")
	if err := swap(exe, filepath.Join(dir, "next")); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{exe: "v3", exe + ".old1": "v2"} {
		if got, _ := os.ReadFile(name); string(got) != want {
			t.Errorf("%s = %q, want %q", filepath.Base(name), got, want)
		}
	}
	RemoveOld(exe)
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("after RemoveOld: %v", entries)
	}
	// next is gone, so the swap puts exe back.
	if err := swap(exe, filepath.Join(dir, "next")); err == nil {
		t.Fatal("swapped in a missing binary")
	}
	if got, _ := os.ReadFile(exe); string(got) != "v3" {
		t.Fatalf("after a failed swap exe = %q", got)
	}
}

func TestDevBuildNeverChecks(t *testing.T) {
	srv, hits := release(t, "pitwall", runtime.GOOS, []byte("new"), func(string, string) string { return "" })
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
		`C:\Users\u\scoop\apps\pitwall\current\pitwall.exe`:        true,
		`C:\Users\u\AppData\Local\pitwall\bin\pitwall.exe`:         false,
	} {
		if got := Managed(exe); got != want {
			t.Errorf("Managed(%q) = %v, want %v", exe, got, want)
		}
	}
}

// TestTagged: a named release downloads from its own folder, older or
// newer than the latest; a tag never released fails before any write, and
// a string that is not a release tag fails before any request.
func TestTagged(t *testing.T) {
	rel, err := Tagged(LatestURL, "v0.2.13")
	if want := "https://github.com/quanticstudios/pitwall/releases/download/v0.2.13/"; err != nil || rel.Tag != "v0.2.13" || rel.Base != want {
		t.Fatalf("Tagged(LatestURL) = %+v, %v; want base %s", rel, err, want)
	}
	for _, bad := range []string{"", "latest", "v0.2", "v0.2.13/../../x", "../v0.2.13", "v0.2.13-1-gabc1234", "v0.2.13-dirty"} {
		if _, err := Tagged(LatestURL, bad); err == nil {
			t.Errorf("Tagged(%q) passed", bad)
		}
	}
	if _, err := Tagged("https://example.com/releases", "v0.2.13"); err == nil {
		t.Error("Tagged took a URL that does not end in /latest")
	}
	srv, _ := release(t, "pitwall", "linux", []byte("new"), func(name, sum string) string { return sum + "  " + name + "\n" })
	latest := srv.URL + "/quanticstudios/pitwall/releases/latest"
	for tag, want := range map[string]string{"v0.1.0-alpha.10": "new", "v0.1.0-alpha.11": "old"} {
		t.Run(tag, func(t *testing.T) {
			rel, err := Tagged(latest, tag)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			exe := filepath.Join(dir, "pitwall")
			if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
				t.Fatal(err)
			}
			err = install(context.Background(), srv.Client(), rel, exe, "pitwall", "linux", runtime.GOARCH)
			if (err == nil) != (want == "new") {
				t.Fatalf("install %s: %v", tag, err)
			}
			if got, _ := os.ReadFile(exe); string(got) != want {
				t.Fatalf("binary = %q, want %q", got, want)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 1 {
				t.Fatalf("left behind: %v", entries)
			}
		})
	}
}
