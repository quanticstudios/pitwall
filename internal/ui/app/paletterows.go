package app

import (
	"fmt"
	"slices"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
)

// gotoAny names the palette's one row for goto_tab_1 to goto_tab_9.
const gotoAny = "goto_tab"

// maxRecent is how many actions the palette remembers as recent.
const maxRecent = 5

// id names e in gui.json's recent actions: its config name, after "tab."
// or "pane." for a mode's key.
func (e paletteEntry) id() string {
	switch {
	case e.action.Tab:
		return "tab." + e.action.Name
	case e.action.Pane:
		return "pane." + e.action.Name
	}
	return e.action.Name
}

// paletteRows is what the palette lists for query, best first. A tab- or
// pane-mode action titled as a window action is that action, its keys
// added to the window action's. goto_tab_1 to 9 are one row until a word
// of query is a digit. With no query, recent, newest first, leads.
func paletteRows(b *config.Bindings, query string, recent []string) []paletteEntry {
	es := foldModes(paletteEntries(b))
	if !slices.ContainsFunc(strings.Fields(query), func(w string) bool { return len(w) == 1 && w[0] >= '1' && w[0] <= '9' }) {
		es = collapseGoto(es)
	}
	if strings.TrimSpace(query) != "" {
		return rank(es, query)
	}
	var out []paletteEntry
	for _, id := range recent {
		if i := slices.IndexFunc(es, func(e paletteEntry) bool { return e.id() == id }); i >= 0 {
			out = append(out, es[i])
		}
	}
	for _, e := range es {
		if !slices.Contains(recent, e.id()) {
			out = append(out, e)
		}
	}
	return out
}

// foldModes drops each mode action whose title a window action has,
// adding its keys to that action's: pane mode's "Split the pane below" is
// split_down.
func foldModes(es []paletteEntry) []paletteEntry {
	var out []paletteEntry
	at := map[string]int{} // a window action's title: its index in out
	for _, e := range es {
		if !e.action.Tab && !e.action.Pane {
			at[e.action.Title()] = len(out)
		}
		out = append(out, e)
	}
	return slices.DeleteFunc(out, func(e paletteEntry) bool {
		i, ok := at[e.action.Title()]
		if !ok || !e.action.Tab && !e.action.Pane {
			return false
		}
		out[i].keys = append(slices.Clip(out[i].keys), e.keys...)
		return true
	})
}

// collapseGoto puts one "Go to tab 1–9" row where goto_tab_1 was, in
// place of the nine. Its keys are goto_tab_1's that goto_tab_9 has with 9
// for the 1, as "Alt+1…9".
func collapseGoto(es []paletteEntry) []paletteEntry {
	isGoto := func(n int) func(paletteEntry) bool {
		return func(e paletteEntry) bool {
			return !e.action.Tab && !e.action.Pane && e.action.Name == fmt.Sprint("goto_tab_", n)
		}
	}
	first, last := slices.IndexFunc(es, isGoto(1)), slices.IndexFunc(es, isGoto(9))
	if first < 0 || last < 0 {
		return es
	}
	row := paletteEntry{action: config.Action{Name: gotoAny, Doc: "Go to tab 1–9", Group: es[first].action.Group}}
	for _, k := range es[first].keys {
		if p, ok := strings.CutSuffix(k, "1"); ok && slices.Contains(es[last].keys, p+"9") {
			row.keys = append(row.keys, p+"1…9")
		}
	}
	out := slices.Clone(es)
	out[first] = row
	return slices.DeleteFunc(out, func(e paletteEntry) bool {
		return !e.action.Tab && !e.action.Pane && strings.HasPrefix(e.action.Name, gotoAny+"_")
	})
}

// pushRecent puts id first in recent, once, keeping maxRecent.
func pushRecent(recent []string, id string) []string {
	out := append([]string{id}, slices.DeleteFunc(slices.Clone(recent), func(s string) bool { return s == id })...)
	return out[:min(len(out), maxRecent)]
}

// paletteHeight is the palette card's height for n rows of rowH, the
// chrome around them and at most most: room for one row when there are
// none, for the line that says so.
func paletteHeight(n, rowH, chrome, most int) int {
	return min(max(n, 1)*rowH+chrome, most)
}

// hitSpan is a piece of a title, hit when the query matched it.
type hitSpan struct {
	s   string
	hit bool
}

// hitSpans cuts title into the runs each word of query matches, as fuzzy
// aligns it, and the runs between. A word that matches the title only
// through its group or keys marks nothing.
func hitSpans(title, query string) []hitSpan {
	hit := make([]bool, len(title))
	if len(strings.ToLower(title)) == len(title) { // fuzzyPos's offsets are the lowered string's
		for _, w := range strings.Fields(query) {
			for _, i := range fuzzyPos(w, title) {
				hit[i] = true
			}
		}
	}
	var out []hitSpan
	for i := 0; i < len(title); {
		j := i
		for j < len(title) && hit[j] == hit[i] {
			j++
		}
		out = append(out, hitSpan{title[i:j], hit[i]})
		i = j
	}
	return out
}

// fuzzyPos is where fuzzy's best alignment of q puts each of q's bytes in
// s, nil when q does not match.
func fuzzyPos(q, s string) []int {
	if q == "" || s == "" {
		return nil
	}
	q, s = strings.ToLower(q), strings.ToLower(s)
	const none = -1 << 30
	// score[j][i] is the best score of q[:j+1] with q[j] at s[i]; from[j][i]
	// is where q[j-1] is then.
	score, from := make([][]int, len(q)), make([][]int, len(q))
	for j := range len(q) {
		score[j], from[j] = make([]int, len(s)), make([]int, len(s))
		for i := range len(s) {
			score[j][i] = none
			if s[i] != q[j] {
				continue
			}
			pts := 1
			if i == 0 || !wordChar(s[i-1]) {
				pts += 8
			}
			if j == 0 {
				score[j][i] = pts
				continue
			}
			best, at := none, -1
			for k := range i {
				p := score[j-1][k]
				if p == none {
					continue
				}
				if k == i-1 {
					p += 6
				} else {
					p -= i - k + 1
				}
				if p > best {
					best, at = p, k
				}
			}
			if at >= 0 {
				score[j][i], from[j][i] = best+pts, at
			}
		}
	}
	end, best := -1, none
	for i, n := range score[len(q)-1] {
		if n > best {
			end, best = i, n
		}
	}
	if end < 0 {
		return nil
	}
	pos := make([]int, len(q))
	for j := len(q) - 1; j >= 0; j-- {
		pos[j], end = end, from[j][end]
	}
	return pos
}
