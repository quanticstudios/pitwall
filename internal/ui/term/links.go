package term

import (
	"image"
	"regexp"
	"strings"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// link is a clickable run of cells [x0,x1) on one row.
type link struct {
	x0, x1 int
	url    string // what opens: the OSC 8 target, or the text with https:// before a bare www.
	osc    bool   // set by the program with OSC 8, not found in the text
}

// urlStart finds where a text URL may begin. Matches only start the scan;
// urlEnd decides where each URL stops.
var urlStart = regexp.MustCompile(`(?i)\b(?:https?://|file://|www\.)`)

// urlChar is what a text URL may contain: printable ASCII but quotes, angle
// brackets and backticks. Anything else, box drawing included, ends it.
func urlChar(c byte) bool {
	return c > ' ' && c < 0x7f && !strings.ContainsRune("\"<>`", rune(c))
}

// trimURL drops trailing punctuation that ends the sentence around a URL
// rather than the URL: a closing bracket stays only while it has a partner,
// as in Wikipedia's Foo_(bar), and a quote only when it pairs with another.
func trimURL(u string) string {
	for len(u) > 0 {
		switch c := u[len(u)-1]; c {
		case '.', ',', ':', ';', '!', '?':
		case ')', ']', '}':
			open := map[byte]byte{')': '(', ']': '[', '}': '{'}[c]
			if strings.Count(u, string(open)) >= strings.Count(u, string(c)) {
				return u
			}
		case '\'':
			if strings.Count(u, "'")%2 == 0 {
				return u
			}
		default:
			return u
		}
		u = u[:len(u)-1]
	}
	return u
}

// openable reports whether a link may be handed to the OS opener: web,
// file and mail links only, never javascript: or a custom scheme that
// starts an app.
func openable(u string) bool {
	scheme, _, ok := strings.Cut(u, ":")
	if !ok {
		return false
	}
	switch strings.ToLower(scheme) {
	case "http", "https", "file", "mailto":
		return true
	}
	return false
}

// rowLinks appends the links on one row to dst: OSC 8 runs first, then URLs
// found in the text between them. buf is scratch space; both are returned
// for reuse.
func rowLinks(dst []link, buf []byte, cells []vt.Cell) ([]link, []byte) {
	for x := 0; x < len(cells); {
		u := cells[x].Link
		if u == "" {
			x++
			continue
		}
		k := x + 1
		for k < len(cells) && (cells[k].Link == u || cells[k].Width == 0 && cells[k].Content == "") {
			k++
		}
		if openable(u) {
			dst = append(dst, link{x0: x, x1: k, url: u, osc: true})
		}
		x = k
	}

	// The row as text with one byte per column: wide and non-ASCII cells
	// and OSC 8 cells become a byte no URL contains, so a byte offset is a
	// column.
	buf = buf[:0]
	for _, c := range cells {
		b := byte(0)
		if len(c.Content) == 1 && c.Link == "" {
			b = c.Content[0]
		}
		buf = append(buf, b)
	}
	s := string(buf)
	if !strings.Contains(s, "://") && !strings.Contains(s, "www.") && !strings.Contains(s, "WWW.") {
		return dst, buf
	}
	for _, m := range urlStart.FindAllStringIndex(s, -1) {
		if n := len(dst); n > 0 && !dst[n-1].osc && m[0] < dst[n-1].x1 {
			continue // inside the previous URL
		}
		end := m[1]
		for end < len(s) && urlChar(s[end]) {
			end++
		}
		u := trimURL(s[m[0]:end])
		if len(u) <= m[1]-m[0] {
			continue // the prefix alone
		}
		target := u
		if strings.EqualFold(u[:4], "www.") {
			target = "https://" + u
		}
		dst = append(dst, link{x0: m[0], x1: m[0] + len(u), url: target})
	}
	return dst, buf
}

// linkAt is the link under cell p of g.
func (v *View) linkAt(g *vt.Grid, p image.Point) (link, bool) {
	if p.Y < 0 || p.Y >= g.Rows || p.X < 0 || p.X >= g.Cols {
		return link{}, false
	}
	v.links, v.linkBuf = rowLinks(v.links[:0], v.linkBuf, g.Cells[p.Y*g.Cols:(p.Y+1)*g.Cols])
	for _, l := range v.links {
		if p.X >= l.x0 && p.X < l.x1 {
			return l, true
		}
	}
	return link{}, false
}

// hovered reports whether l on row y is drawn as the link under the pointer:
// all cells of the same OSC 8 target, or the one text URL under it.
func (v *View) hovered(l link, y int) bool {
	h := v.hover
	if !v.hoverOn || l.osc != h.osc {
		return false
	}
	if l.osc {
		return l.url == h.url
	}
	return y == v.hoverY && l.x0 == h.x0
}
