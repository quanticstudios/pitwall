// Package repoicon finds a repo's own icon, its favicon or logo, on the
// machine whose files these are. A window reads its own daemon's repos,
// and a daemon reads them for a window on another machine.
package repoicon

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Paths are where a repo keeps its icon, best first, an SVG before a
// bitmap: .pitwall/icon.* is one the user put there to pick it, apps/*
// covers a monorepo's web apps.
var Paths = []string{
	".pitwall/icon.svg", ".pitwall/icon.png", ".pitwall/icon.ico",
	"favicon.svg", "favicon.ico", "favicon.png",
	"public/favicon.svg", "public/favicon.ico", "public/favicon.png", "public/icon.svg", "public/icon.png", "public/apple-touch-icon.png", "public/logo.svg", "public/logo.png",
	"static/favicon.svg", "static/favicon.ico", "static/favicon.png",
	"app/icon.svg", "app/favicon.ico", "app/icon.png", "src/app/icon.svg", "src/app/favicon.ico", "src/app/icon.png",
	"assets/favicon.svg", "assets/favicon.ico", "assets/favicon.png", "assets/icon.svg", "assets/icon.png", "assets/logo.svg", "assets/logo.png",
	"build/icon.png", "logo.svg", "logo.png", "icon.svg", "icon.png", ".github/logo.svg", ".github/logo.png",
	"apps/*/public/favicon.svg", "apps/*/public/favicon.ico", "apps/*/public/favicon.png", "apps/*/app/icon.svg", "apps/*/app/favicon.ico",
}

// Max caps an icon file: a favicon is kilobytes.
const Max = 1 << 20

// Files are root's icon files, best first: the regular files Paths match
// that hold at most Max bytes.
func Files(root string) []string {
	var files []string
	for _, p := range Paths {
		matches, _ := filepath.Glob(filepath.Join(root, p))
		for _, m := range matches {
			if fi, err := os.Stat(m); err == nil && fi.Mode().IsRegular() && fi.Size() <= Max {
				files = append(files, m)
			}
		}
	}
	return files
}

// Read is the bytes of file, one of root's Files, opened through root so
// that neither file nor a folder above it can be a symlink out of the repo,
// and read only while it stays a regular file of at most Max bytes. It
// checks again what Files checked, since the file may have been swapped.
func Read(root, file string) ([]byte, error) {
	rel, err := filepath.Rel(root, file)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.Mode().IsRegular() {
		return nil, errors.New("repoicon: not a regular file: " + file)
	}
	b, err := io.ReadAll(io.LimitReader(f, Max+1))
	if err == nil && len(b) > Max {
		err = errors.New("repoicon: over Max bytes: " + file)
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

// Root is the folder at or above dir that holds .git, a worktree's own
// included, or "" outside a repo.
func Root(dir string) string {
	if dir == "" {
		return ""
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// Caps on an SVG before oksvg reads it, or the phone gets it.
const (
	MaxSVGDepth = 64    // nested elements
	MaxSVGElems = 20000 // elements
)

// SVGSafe refuses an SVG oksvg could recurse forever in or spend too long
// on: one with a <use>, deeper than MaxSVGDepth, or with more than
// MaxSVGElems elements. oksvg draws a <use> by drawing its target again,
// with no check for a cycle, and a Go stack overflow cannot be recovered.
func SVGSafe(b []byte) error {
	depth, n := 0, 0
	d := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch {
			case t.Name.Local == "use":
				return errors.New("SVG has a <use>")
			case depth >= MaxSVGDepth:
				return errors.New("SVG nested too deep")
			}
			depth++
			if n++; n > MaxSVGElems {
				return errors.New("SVG has too many elements")
			}
		case xml.EndElement:
			depth--
		}
	}
}
