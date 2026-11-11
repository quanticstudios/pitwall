package remote

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// MaxFileBytes is the largest file the phone reads, markdown or image.
const MaxFileBytes = 10 << 20

// Why a file is not served.
var (
	errOutside = errors.New("that file is outside the tab's folder")
	errKind    = errors.New("only markdown files and their images open on the phone")
	errNotFile = errors.New("not a file")
	errLarge   = fmt.Errorf("larger than %d MiB", MaxFileBytes>>20)
	errImage   = errors.New("not a PNG, JPEG, GIF, WebP or SVG image")
)

var svgStart = regexp.MustCompile(`(?s)^\s*(<\?xml[^>]*>\s*)?(<!--.*?-->\s*)*(<!DOCTYPE[^>]*>\s*)?<svg\b`)

// ReadFile reads name for the phone: a markdown file, or an image one
// shows, in the folder root. A relative name is cwd's, and ~/ is the home
// folder. The file opens through os.OpenRoot, so neither a symlink nor
// .. leaves root; it must be a regular file of at most MaxFileBytes, and
// an image must be one by its content. It returns the bytes and their
// Content-Type.
func ReadFile(root, cwd, name string) ([]byte, string, error) {
	if root == "" || name == "" {
		return nil, "", errOutside
	}
	if rest, ok := strings.CutPrefix(name, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, "", errOutside
		}
		name = filepath.Join(home, rest)
	}
	name = filepath.FromSlash(name)
	if !filepath.IsAbs(name) {
		name = filepath.Join(cmp.Or(cwd, root), name)
	}
	rel, err := filepath.Rel(root, name)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, "", errOutside
	}
	var ctype string
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".md", ".markdown":
		ctype = "text/markdown; charset=utf-8"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
	default:
		return nil, "", errKind
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, "", err
	}
	defer r.Close()
	// why: a FIFO is refused before it is opened, as opening one waits
	// for a writer; the open does not wait either, and what it opened is
	// checked again, in case the path changed in between.
	if fi, err := r.Stat(rel); err != nil {
		return nil, "", rootErr(err)
	} else if !fi.Mode().IsRegular() {
		return nil, "", errNotFile
	}
	f, err := r.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", rootErr(err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil {
		return nil, "", err
	} else if !fi.Mode().IsRegular() {
		return nil, "", errNotFile
	} else if fi.Size() > MaxFileBytes {
		return nil, "", errLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > MaxFileBytes {
		return nil, "", errLarge
	}
	if ctype == "" {
		ctype = http.DetectContentType(b)
		switch {
		case ctype == "image/png" || ctype == "image/jpeg" || ctype == "image/gif" || ctype == "image/webp":
		case strings.EqualFold(filepath.Ext(rel), ".svg") && svgStart.Match(b):
			ctype = "image/svg+xml"
		default:
			return nil, "", errImage
		}
	}
	return b, ctype, nil
}

// rootErr is err from os.Root without the path it repeats, and an escape
// as errOutside.
func rootErr(err error) error {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	if errors.Is(err, fs.ErrNotExist) {
		return fs.ErrNotExist
	}
	if strings.Contains(err.Error(), "escapes from parent") {
		return errOutside
	}
	return err
}

// file serves ?path= of the tab ?tab= names, read by ReadFile against
// the tab's working directory and folder.
func (s *Server) file(w http.ResponseWriter, r *http.Request, d Device) {
	q := r.URL.Query()
	tab, name := q.Get("tab"), q.Get("path")
	cwd, root, ok := s.Folder(tab)
	if !ok {
		http.Error(w, "that tab is not open, or has no folder", http.StatusNotFound)
		return
	}
	b, ctype, err := ReadFile(root, cwd, name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, "no such file", http.StatusNotFound)
		return
	case errors.Is(err, errLarge):
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	log.Printf("remote: %q read %d bytes from tab %s", d.Name, len(b), tab) // never the path
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("Content-Disposition", "attachment")
	w.Write(b)
}
