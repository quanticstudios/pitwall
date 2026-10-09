// Package update finds a newer pitwall release on GitHub and installs it
// over the running binary, as scripts/get.sh would.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

// LatestURL is GitHub's latest release, the one get.sh installs.
const LatestURL = "https://api.github.com/repos/quanticstudios/pitwall/releases/latest"

// Managed is whether a package manager owns the binary at exe, a resolved
// path: Homebrew's Cellar, /usr/bin from pacman or a distro package, or
// Scoop's apps folder. Its package manager updates it, so the button stays
// hidden.
func Managed(exe string) bool {
	return strings.Contains(exe, "/Cellar/") || strings.HasPrefix(exe, "/usr/bin/") ||
		strings.Contains(strings.ToLower(exe), `\scoop\apps\`)
}

// Release is a GitHub release: its tag and its assets' download URLs by
// file name.
type Release struct {
	Tag    string
	Assets map[string]string
}

// releaseRe matches a release tag. A git describe string past a tag
// (v0.1.0-alpha.18-3-gabc1234, ...-dirty) and dev builds do not match.
var releaseRe = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*))?$`)

// Newer reports whether tag is a later release than current, by semver
// precedence: v0.1.0-alpha.10 is after v0.1.0-alpha.9. A current that is
// not a release tag never has a newer one.
func Newer(tag, current string) bool {
	a, b := releaseRe.FindStringSubmatch(tag), releaseRe.FindStringSubmatch(current)
	return a != nil && b != nil && compare(a[1:], b[1:]) > 0
}

// compare orders [major, minor, patch, prerelease].
func compare(a, b []string) int {
	for i := range 3 {
		if c := compareNum(a[i], b[i]); c != 0 {
			return c
		}
	}
	switch {
	case a[3] == b[3]:
		return 0
	case a[3] == "": // a release is after its prereleases
		return 1
	case b[3] == "":
		return -1
	}
	x, y := strings.Split(a[3], "."), strings.Split(b[3], ".")
	for i := range min(len(x), len(y)) {
		xn, yn := isNum(x[i]), isNum(y[i])
		c := 0
		switch {
		case xn && yn:
			c = compareNum(x[i], y[i])
		case xn: // numeric identifiers sort before alphanumeric ones
			c = -1
		case yn:
			c = 1
		default:
			c = strings.Compare(x[i], y[i])
		}
		if c != 0 {
			return c
		}
	}
	return len(x) - len(y)
}

func isNum(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }

// compareNum orders decimal strings of any length.
func compareNum(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

// Check fetches the latest release from url and reports whether it is
// newer than current. A current that is not a release tag makes no
// request.
func Check(ctx context.Context, c *http.Client, url, current string) (Release, bool, error) {
	if releaseRe.FindStringSubmatch(current) == nil {
		return Release{}, false, nil
	}
	data, err := get(ctx, c, url, 1<<20)
	if err != nil {
		return Release{}, false, err
	}
	var r struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return Release{}, false, fmt.Errorf("latest release: %w", err)
	}
	rel := Release{Tag: r.Tag, Assets: map[string]string{}}
	for _, a := range r.Assets {
		rel.Assets[a.Name] = a.URL
	}
	return rel, Newer(rel.Tag, current), nil
}

// Install downloads rel's archive for this platform, checks it against the
// release's checksums.txt, and replaces exe (through symlinks) with the
// pitwall binary inside. On any error exe is left as it was.
func Install(ctx context.Context, c *http.Client, rel Release, exe string) error {
	return install(ctx, c, rel, exe, runtime.GOOS, runtime.GOARCH)
}

// install is Install for goos and goarch.
func install(ctx context.Context, c *http.Client, rel Release, exe, goos, goarch string) error {
	name, extract, bin := fmt.Sprintf("pitwall_%s_%s.tar.gz", goos, goarch), extractTar, "pitwall"
	if goos == "windows" {
		name, extract, bin = fmt.Sprintf("pitwall_%s_%s.zip", goos, goarch), extractZip, "pitwall.exe"
	}
	sums, err := get(ctx, c, rel.Assets["checksums.txt"], 1<<20)
	if err != nil {
		return fmt.Errorf("checksums.txt: %w", err)
	}
	arc, err := get(ctx, c, rel.Assets[name], 256<<20)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := verify(sums, name, arc); err != nil {
		return err
	}
	data, err := extract(arc, bin)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return replace(exe, data, goos == "windows")
}

// verify checks data against name's line in a sha256sum listing.
func verify(sums []byte, name string, data []byte) error {
	for line := range strings.Lines(string(sums)) {
		f := strings.Fields(line)
		if len(f) != 2 || strings.TrimPrefix(f[1], "*") != name {
			continue
		}
		got := sha256.Sum256(data)
		if !strings.EqualFold(f[0], hex.EncodeToString(got[:])) {
			return fmt.Errorf("%s does not match checksums.txt", name)
		}
		return nil
	}
	return fmt.Errorf("checksums.txt has no %s", name)
}

// extractTar is the file named bin in a release tar.gz.
func extractTar(arc []byte, bin string) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(arc))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no pitwall binary in the archive")
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Clean(h.Name) == bin {
			return readMax(tr, 512<<20)
		}
	}
}

// extractZip is the file named bin in a release zip.
func extractZip(arc []byte, bin string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(arc), int64(len(arc)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Mode().IsRegular() && path.Clean(f.Name) == bin {
			r, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer r.Close()
			return readMax(r, 512<<20)
		}
	}
	return nil, errors.New("no pitwall binary in the archive")
}

// replace writes bin next to exe and renames it over exe, so a crash
// midway leaves the old binary. A running process keeps its old copy. With
// aside, for Windows, it swaps the two instead.
func replace(exe string, bin []byte, aside bool) error {
	exe, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return err
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(exe), ".pitwall-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // gone already after the rename
	_, err = f.Write(bin)
	err = errors.Join(err, f.Chmod(fi.Mode().Perm()|0o111), f.Sync(), f.Close())
	if err != nil {
		return err
	}
	if aside {
		return swap(exe, f.Name())
	}
	return os.Rename(f.Name(), exe)
}

// oldMax bounds the binaries swap keeps aside at once.
const oldMax = 10

// swap moves exe aside and next into its place: Windows will not replace
// or delete a running executable but lets it be renamed. The old binary
// goes to exe.old, or exe.old1 and on while an older one still runs, say
// in the daemon; RemoveOld deletes them on a later start. If next cannot
// move in, exe moves back.
func swap(exe, next string) error {
	old, err := freeOld(exe)
	if err != nil {
		return err
	}
	if err := os.Rename(exe, old); err != nil {
		return err
	}
	if err := os.Rename(next, exe); err != nil {
		return errors.Join(err, os.Rename(old, exe))
	}
	return nil
}

// freeOld is the first aside name for exe that is free or can be freed.
func freeOld(exe string) (string, error) {
	var err error
	for i := range oldMax {
		old := oldName(exe, i)
		if err = os.Remove(old); err == nil || errors.Is(err, fs.ErrNotExist) {
			return old, nil
		}
	}
	return "", fmt.Errorf("%d older binaries still run: %w", oldMax, err)
}

func oldName(exe string, i int) string {
	if i == 0 {
		return exe + ".old"
	}
	return exe + ".old" + strconv.Itoa(i)
}

// RemoveOld deletes the binaries swap set aside next to exe. One that a
// process still runs stays for a later start.
func RemoveOld(exe string) {
	if exe, err := filepath.EvalSymlinks(exe); err == nil {
		for i := range oldMax {
			os.Remove(oldName(exe, i))
		}
	}
}

func get(ctx context.Context, c *http.Client, url string, limit int64) ([]byte, error) {
	if url == "" {
		return nil, errors.New("not in the release")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return readMax(resp.Body, limit)
}

func readMax(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("larger than %d bytes", limit)
	}
	return data, err
}
