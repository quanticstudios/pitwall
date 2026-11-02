package term

import (
	"image"
	"regexp"
	"slices"
	"strings"

	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Hint kinds, what a HintMatch is.
const (
	HintURL    = "url"
	HintPath   = "path"
	HintIP     = "ip"
	HintNumber = "number" // #123, an issue or pull request
	HintHash   = "hash"   // a git commit
	HintCustom = "custom" // a [terminal] hint_patterns entry
)

// HintSpan is the cells [X0,X1) of row Y a match covers.
type HintSpan struct{ Y, X0, X1 int }

// HintMatch is a run of text hints mode found on a grid. A match the
// terminal wrapped has one span per row.
type HintMatch struct {
	Text  string
	Kind  string
	Spans []HintSpan
}

// Hint is a match with its label, as hints mode draws it. Typed is how
// many of the label's letters the user typed so far.
type Hint struct {
	HintMatch
	Label string
	Typed int
}

// HintAlphabet is the label letters, easiest first: the home row, then the
// rows above and below. i, l and o are left out because they read as 1, I
// and 0 in a small chip.
const HintAlphabet = "asdfghjkqwertyupzxcvbnm"

// HintLabels returns n labels of HintAlphabet, shortest first, none the
// prefix of another, so typing one never stops at another. Each round
// replaces the first label with its one-letter longer children, as
// Vimium's link hints do.
func HintLabels(n int) []string {
	if n <= 0 {
		return nil
	}
	labels, next := []string{""}, 0
	for len(labels)-next < n || len(labels) == 1 {
		p := labels[next]
		next++
		for _, c := range HintAlphabet {
			labels = append(labels, p+string(c))
		}
	}
	return labels[next : next+n]
}

var (
	pathRe = regexp.MustCompile(`(?:~|\.\.?)?/[\w.@%+~-]+(?:/[\w.@%+~-]*)*(?::\d+){0,2}|[\w.@%+-]+(?:/[\w.@%+~-]+)+/?(?::\d+){0,2}|[\w.-]+\.[A-Za-z]\w*:\d+(?::\d+)?`)
	ipRe   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}(?::\d{1,5})?\b`)
	numRe  = regexp.MustCompile(`#\d+\b`)
	hashRe = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
)

// hintsWrapRows caps how many wrapped rows one line joins, so a screen of
// one long line does not make a match too long to label.
const hintsWrapRows = 8

// ScanHints finds the URLs, paths, IPv4 addresses, #numbers and git hashes
// on g, and the matches of custom, in reading order. A row the terminal
// wrapped joins the next one, so a URL broken across rows is one match. Two
// matches never overlap: custom patterns win, then the match that starts
// first, then the longer one.
//
// Adapted from tuios (MIT): internal/app/hints_scan.go
func ScanHints(g *vt.Grid, custom []*regexp.Regexp) []HintMatch {
	var out []HintMatch
	var b strings.Builder
	var refs []image.Point // the cell each byte run starts at
	var offs []int         // where each cell's text starts in the line
	for y := 0; y < g.Rows; {
		b.Reset()
		refs, offs = refs[:0], offs[:0]
		next := y
		for rows := 0; next < g.Rows; {
			for x := range g.Cols {
				c := g.At(x, next)
				if c.Content == "" && x > 0 && g.At(x-1, next).Width == 2 {
					continue // the right half of a wide character
				}
				offs = append(offs, b.Len())
				refs = append(refs, image.Pt(x, next))
				if c.Content == "" {
					b.WriteByte(' ')
				} else {
					b.WriteString(c.Content)
				}
			}
			rows++
			wrapped := next < len(g.Wrapped) && g.Wrapped[next]
			next++
			if !wrapped || rows >= hintsWrapRows {
				break
			}
		}
		line := b.String()
		for _, f := range scanLine(line, custom) {
			first, _ := slices.BinarySearch(offs, f.start)
			var spans []HintSpan
			for i := first; i < len(offs) && offs[i] < f.end; i++ {
				c := refs[i]
				if n := len(spans); n > 0 && spans[n-1].Y == c.Y {
					spans[n-1].X1 = c.X + max(1, int(g.At(c.X, c.Y).Width))
				} else {
					spans = append(spans, HintSpan{Y: c.Y, X0: c.X, X1: c.X + max(1, int(g.At(c.X, c.Y).Width))})
				}
			}
			if len(spans) > 0 {
				out = append(out, HintMatch{Text: line[f.start:f.end], Kind: f.kind, Spans: spans})
			}
		}
		y = next
	}
	return out
}

type found struct {
	start, end int
	kind       string
}

// scanLine finds the matches in one line of text, without overlaps, in
// order.
func scanLine(s string, custom []*regexp.Regexp) []found {
	var mine, built []found
	for _, re := range custom {
		for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
			a, z := m[0], m[1]
			if len(m) >= 4 && m[2] >= 0 {
				a, z = m[2], m[3] // the first group, when the pattern has one
			}
			if z > a {
				mine = append(mine, found{a, z, HintCustom})
			}
		}
	}
	for _, m := range urlStart.FindAllStringIndex(s, -1) {
		end := m[1]
		for end < len(s) && urlChar(s[end]) {
			end++
		}
		if u := trimURL(s[m[0]:end]); len(u) > m[1]-m[0] {
			built = append(built, found{m[0], m[0] + len(u), HintURL})
		}
	}
	for _, m := range ipRe.FindAllStringIndex(s, -1) {
		if ipv4(s, m[0], m[1]) {
			built = append(built, found{m[0], m[1], HintIP})
		}
	}
	for _, m := range pathRe.FindAllStringIndex(s, -1) {
		if a, z, ok := pathAt(s, m[0], m[1]); ok {
			built = append(built, found{a, z, HintPath})
		}
	}
	for _, m := range numRe.FindAllStringIndex(s, -1) {
		if m[0] == 0 || !wordByte(s[m[0]-1]) && s[m[0]-1] != '&' {
			built = append(built, found{m[0], m[1], HintNumber})
		}
	}
	for _, m := range hashRe.FindAllStringIndex(s, -1) {
		if h := s[m[0]:m[1]]; strings.ContainsAny(h, "0123456789") && strings.ContainsAny(h, "abcdef") {
			built = append(built, found{m[0], m[1], HintHash})
		}
	}
	out := pick(nil, mine)
	return pick(out, built)
}

// pick adds the candidates that overlap nothing in have, leftmost first,
// then longest, and returns them all in order.
func pick(have, cand []found) []found {
	slices.SortStableFunc(cand, func(a, b found) int {
		if a.start != b.start {
			return a.start - b.start
		}
		return b.end - a.end
	})
	for _, c := range cand {
		if !slices.ContainsFunc(have, func(h found) bool { return c.start < h.end && h.start < c.end }) {
			have = append(have, c)
		}
	}
	slices.SortFunc(have, func(a, b found) int { return a.start - b.start })
	return have
}

func wordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// ipv4 reports whether s[a:z] is a whole IPv4 address, each part 0 to 255,
// and not part of a longer dotted number such as a version.
func ipv4(s string, a, z int) bool {
	if a > 0 && s[a-1] == '.' || z+1 < len(s) && s[z] == '.' && s[z+1] >= '0' && s[z+1] <= '9' {
		return false
	}
	host, _, _ := strings.Cut(s[a:z], ":")
	for p := range strings.SplitSeq(host, ".") {
		n := 0
		for _, c := range p {
			n = n*10 + int(c-'0')
		}
		if n > 255 {
			return false
		}
	}
	return true
}

// pathAt trims a path match s[a:z] the way a URL is trimmed, and rejects
// one that is no path: an absolute path right after a letter, digit, _ or
// < (as in </p>), and one with no letter, such as 1/2.
func pathAt(s string, a, z int) (int, int, bool) {
	p := trimURL(s[a:z])
	if s[a] == '/' && a > 0 && (wordByte(s[a-1]) || s[a-1] == '<') {
		return 0, 0, false
	}
	if !strings.ContainsFunc(p, func(r rune) bool { return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' }) {
		return 0, 0, false
	}
	return a, a + len(p), p != "/"
}

// SetHints turns hints mode's look on for the next Layout: every cell but
// the matches dims, and each hint's label covers the start of its match.
// on false turns it off. Call it before Layout.
func (v *View) SetHints(on bool, hints []Hint) {
	v.hintsOn, v.hints = on, hints
}

// hintLit appends the spans of row y the hints leave bright to dst.
func (v *View) hintLit(dst []HintSpan, y int) []HintSpan {
	for _, h := range v.hints {
		for _, s := range h.Spans {
			if s.Y == y {
				dst = append(dst, s)
			}
		}
	}
	return dst
}

// dimHints dims the styles of a row but its lit spans.
func dimHints(st []style, lit []HintSpan) {
	for x := range st {
		if !slices.ContainsFunc(lit, func(s HintSpan) bool { return x >= s.X0 && x < s.X1 }) {
			st[x].fg = theme.Mix(st[x].bg, st[x].fg, 0.3)
			st[x].link = false
		}
	}
}

// hintChip is a label's chip image, by label and typed letters.
type hintChip struct {
	label string
	typed int
}

// drawHintLabels draws each hint's label as a chip over the first cells
// of its match, in the theme's primary color, the typed letters faded. A
// chip that would run off the row's end moves left.
func (v *View) drawHintLabels(ops *op.Ops, n, h int) {
	if v.chips == nil {
		v.chips = map[hintChip]paint.ImageOp{}
	}
	cw, ch := v.cell.X, v.cell.Y
	bg, fg := v.th.Primary, v.th.OnPrimary
	done := theme.Mix(bg, fg, 0.45)
	for _, hint := range v.hints {
		if hint.Label == "" || len(hint.Spans) == 0 {
			continue
		}
		s := hint.Spans[0]
		w := len(hint.Label)
		if s.Y >= h || w > n {
			continue
		}
		x := min(s.X0, n-w)
		k := hintChip{hint.Label, hint.Typed}
		img, ok := v.chips[k]
		if !ok {
			rgba := image.NewRGBA(image.Rect(0, 0, w*cw, ch))
			fillRect(rgba, rgba.Rect, bg)
			for i, r := range hint.Label {
				c := fg
				if i < hint.Typed {
					c = done
				}
				blit(rgba, i*cw, 0, v.glyph(1, string(r), 1), c)
			}
			img = paint.NewImageOp(rgba)
			img.Filter = paint.FilterNearest
			v.chips[k] = img
		}
		r := image.Rect(x*cw, s.Y*ch, (x+w)*cw, (s.Y+1)*ch).Add(image.Pt(v.pad, v.pad))
		t := op.Offset(r.Min).Push(ops)
		rc := clip.UniformRRect(image.Rectangle{Max: r.Size()}, max(1, ch/6)).Push(ops)
		img.Add(ops)
		paint.PaintOp{}.Add(ops)
		rc.Pop()
		t.Pop()
	}
}
