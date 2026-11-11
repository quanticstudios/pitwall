package remote

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/repoicon"
)

// Icons finds the repo icon of a tab's folder for the app and keeps it by
// a hash of its bytes, which GET /api/icon/{hash} serves. Only files
// repoicon.Files lists, of PNG, ICO or SVG bytes, go in.
type Icons struct {
	mu     sync.Mutex
	dirs   map[string]iconDir // by folder
	byHash map[string]icon
	now    func() time.Time
}

type iconDir struct {
	hash string
	at   time.Time
}

type icon struct {
	data  []byte
	ctype string
}

// iconRecheck is how long a folder's icon is trusted before its repo is
// read again, so a changed icon reaches the app.
const iconRecheck = time.Minute

// NoIcon is Tab.Icon for a repo with no icon file, which the app draws as
// the sidebar's letter tile.
const NoIcon = "-"

// Of is the hash of the icon of dir's repo, NoIcon for a repo without
// one, or "" outside a repo.
func (ic *Icons) Of(dir string) string {
	if dir == "" {
		return ""
	}
	now := time.Now
	if ic.now != nil {
		now = ic.now
	}
	ic.mu.Lock()
	d, ok := ic.dirs[dir]
	ic.mu.Unlock()
	if ok && now().Sub(d.at) < iconRecheck {
		return d.hash
	}
	hash, found := "", icon{}
	if root := repoicon.Root(dir); root != "" {
		hash = NoIcon
		for _, f := range repoicon.Files(root) {
			b, err := repoicon.Read(root, f)
			if err != nil {
				continue
			}
			if t := iconType(f, b); t != "" {
				sum := sha256.Sum256(b)
				hash, found = hex.EncodeToString(sum[:12]), icon{b, t}
				break
			}
		}
	}
	ic.mu.Lock()
	defer ic.mu.Unlock()
	if ic.dirs == nil {
		ic.dirs, ic.byHash = map[string]iconDir{}, map[string]icon{}
	}
	ic.dirs[dir] = iconDir{hash, now()}
	if found.data != nil {
		// ponytail: an icon replaced in its repo stays served until the daemon restarts; a few KB each.
		ic.byHash[hash] = found
	}
	return hash
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// iconType is the media type of b, the bytes of icon file name, or "" for
// anything but a PNG, an ICO or an SVG.
func iconType(name string, b []byte) string {
	switch {
	case bytes.HasPrefix(b, pngMagic):
		return "image/png"
	case bytes.HasPrefix(b, []byte{0, 0, 1, 0}):
		return "image/x-icon"
	case strings.EqualFold(filepath.Ext(name), ".svg") && isSVG(b):
		return "image/svg+xml"
	}
	return ""
}

// isSVG reports whether b is a well-formed XML document whose one root
// element is svg, with nothing but space, comments, a declaration and a
// doctype outside it.
func isSVG(b []byte) bool {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Entity = xml.HTMLEntity
	depth, roots := 0, 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			return roots == 1 && depth == 0
		}
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				if roots > 0 || t.Name.Local != "svg" {
					return false
				}
				roots++
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(t)) > 0 {
				return false
			}
		}
	}
}

// serve answers GET /api/icon/{hash} with the icon of that hash, and 304
// to an If-None-Match that names it.
func (ic *Icons) serve(w http.ResponseWriter, r *http.Request, _ Device) {
	hash := r.PathValue("hash")
	ic.mu.Lock()
	i, ok := ic.byHash[hash]
	ic.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("ETag", `"`+hash+`"`)
	// why: the hash names the bytes, so they never change under it.
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	if r.Header.Get("If-None-Match") == `"`+hash+`"` {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Type", i.ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	// why: an SVG from a repo opened on this origin must run no script.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	w.Write(i.data)
}
