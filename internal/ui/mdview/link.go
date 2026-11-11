package mdview

import (
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// IsMarkdown reports whether path names a markdown file.
func IsMarkdown(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return true
	}
	return false
}

var lineSuffix = regexp.MustCompile(`(:\d+)+$`)

// LinkPath is the local markdown file link names, read from dir, and its
// #fragment. link is a path as a pane prints it (relative, absolute, ~/,
// with a :line suffix or not), a markdown link's destination, or a
// file:// URL. ok is false for anything else: a web URL, another
// scheme, a file that is not markdown.
func LinkPath(link, dir string) (path, frag string, ok bool) {
	if u, err := url.Parse(link); err == nil && len(u.Scheme) > 1 {
		if !strings.EqualFold(u.Scheme, "file") {
			return "", "", false
		}
		p := u.Path
		if runtime.GOOS == "windows" && len(p) > 2 && p[0] == '/' && p[2] == ':' {
			p = p[1:] // file:///C:/x.md
		}
		p = filepath.Clean(filepath.FromSlash(p))
		return p, u.Fragment, IsMarkdown(p)
	}
	p, frag, _ := strings.Cut(link, "#")
	p, _, _ = strings.Cut(p, "?")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	p = lineSuffix.ReplaceAllString(p, "")
	if !IsMarkdown(p) {
		return "", "", false
	}
	return Resolve(p, dir), frag, true
}

// openable reports whether a link may go to the OS opener: web, file and
// mail links only, as a pane's links.
func openable(link string) bool {
	scheme, _, ok := strings.Cut(link, ":")
	if !ok {
		return false
	}
	switch strings.ToLower(scheme) {
	case "http", "https", "file", "mailto":
		return true
	}
	return false
}

// FileURL is the file URL of absolute path p.
func FileURL(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}
