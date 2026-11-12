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

	"github.com/quanticstudios/pitwall/internal/repoicon"
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
	errLinked  = errors.New("that file has another hard link, which can be a file outside the tab's folder")
	errChanged = errors.New("that file changed while it was opened")
	errMoved   = errors.New("the tab's folder was moved or replaced since the phone first read from it")
)

var svgStart = regexp.MustCompile(`(?s)^\s*(<\?xml[^>]*>\s*)?(<!--.*?-->\s*)*(<!DOCTYPE[^>]*>\s*)?<svg\b`)

// kind is "md" for a markdown file's name, "image" for an image's, else "".
func kind(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown":
		return "md"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		return "image"
	}
	return ""
}

// ReadFile reads name for the phone: a markdown file, or an image one
// shows, in the folder r, opened at root. A relative name is cwd's, and
// ~/ is the home folder. The file opens through r, so neither a symlink
// nor .. leaves it. A symlink is followed one link at a time, and the
// file it ends at must be the same kind by its name, so alias.md ->
// secret.txt is refused. That file must be a regular file with one link
// and at most MaxFileBytes, and an image must be one by its content. It
// returns the bytes and their Content-Type.
func ReadFile(r *os.Root, root, cwd, name string) ([]byte, string, error) {
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
	want := kind(rel)
	if want == "" {
		return nil, "", errKind
	}
	// why: the file is checked before it is opened, as opening a FIFO
	// waits for a writer, and again once open, in case the path changed
	// in between.
	var fi fs.FileInfo
	for hops := 0; ; hops++ {
		if fi, err = r.Lstat(rel); err != nil {
			return nil, "", rootErr(err)
		}
		if fi.Mode()&fs.ModeSymlink == 0 {
			break
		}
		t, err := r.Readlink(rel)
		if err != nil {
			return nil, "", rootErr(err)
		}
		if filepath.IsAbs(t) || hops == 40 {
			return nil, "", errOutside
		}
		// why: not filepath.Join, which drops dir/.. by name; r resolves
		// a .. after a symlinked folder as the kernel does.
		rel = filepath.Dir(rel) + string(filepath.Separator) + t
	}
	if kind(rel) != want {
		return nil, "", errKind
	}
	if !fi.Mode().IsRegular() {
		return nil, "", errNotFile
	}
	f, err := r.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", rootErr(err)
	}
	defer f.Close()
	got, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !os.SameFile(fi, got) {
		return nil, "", errChanged
	}
	if n, err := links(f); err != nil {
		return nil, "", err
	} else if n > 1 {
		return nil, "", errLinked
	}
	if got.Size() > MaxFileBytes {
		return nil, "", errLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > MaxFileBytes {
		return nil, "", errLarge
	}
	if want == "md" {
		return b, "text/markdown; charset=utf-8", nil
	}
	ctype := http.DetectContentType(b)
	switch {
	case ctype == "image/png" || ctype == "image/jpeg" || ctype == "image/gif" || ctype == "image/webp":
	case strings.EqualFold(filepath.Ext(rel), ".svg") && svgStart.Match(b):
		if err := repoicon.SVGSafe(b); err != nil {
			return nil, "", err
		}
		ctype = "image/svg+xml"
	default:
		return nil, "", errImage
	}
	return b, ctype, nil
}

// pin is a tab's folder as the phone first read from it.
type pin struct {
	path string
	id   fs.FileInfo // for os.SameFile
}

// folder opens tab's folder at path for one request. The folder the
// first read found is kept by identity, not by an open handle, which
// on Windows would stop the folder being deleted: when it, or a folder
// above it, is replaced later, path now leading elsewhere, reads are
// refused instead of following it, until the tab closes or moves to
// another folder.
func (s *Server) folder(tab, path string) (*os.Root, error) {
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	id, err := r.Stat(".")
	if err != nil {
		r.Close()
		return nil, err
	}
	s.pmu.Lock()
	defer s.pmu.Unlock()
	for t, p := range s.pins {
		if _, root, ok := s.Folder(t); !ok || root != p.path {
			delete(s.pins, t)
		}
	}
	p, ok := s.pins[tab]
	if !ok {
		if s.pins == nil {
			s.pins = map[string]pin{}
		}
		s.pins[tab] = pin{path, id}
	} else if !os.SameFile(p.id, id) {
		r.Close()
		return nil, errMoved
	}
	return r, nil
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
	fr, err := s.folder(tab, root)
	var b []byte
	var ctype string
	if err == nil {
		b, ctype, err = ReadFile(fr, root, cwd, name)
		fr.Close()
	}
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
