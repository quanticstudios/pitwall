package app

import (
	"strconv"
	"strings"

	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/review"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
)

// reviewFind is the review view's find: a query found in every file's
// lines, unchanged lines folded between hunks and files over hugeLines
// too, file by file, top to bottom.
type reviewFind struct {
	bar  panel.FindBar
	q    []rune // the query this frame, folded
	sig  string // the query and the files the hits are of
	hits []reviewHit
	cur  int // the current hit; -1 for none
	jump bool
}

// reviewHit is a match at rune at of file file's line line, as the view
// draws the line, tabs spaced out.
type reviewHit struct {
	file, line, at int
}

// reviewText is line l as the diff draws it.
func reviewText(l review.Line) string { return strings.ReplaceAll(l.Text, "\t", "    ") }

// reviewHits are q's matches, folded, in files' lines, binary files
// left out.
func reviewHits(files []review.File, q []rune) []reviewHit {
	if len(q) == 0 {
		return nil
	}
	var out []reviewHit
	for k := range files {
		if files[k].Binary {
			continue
		}
		for i, l := range files[k].Lines {
			for _, a := range session.FindRunes(session.Fold(reviewText(l)), q) {
				out = append(out, reviewHit{file: k, line: i, at: a})
			}
		}
	}
	return out
}

// filesSig is what files' hits change with.
func filesSig(files []review.File) string {
	var sb strings.Builder
	for i := range files {
		sb.WriteString(files[i].Key())
		sb.WriteString(files[i].Hash)
		sb.WriteByte(0)
	}
	return sb.String()
}

// update takes the find bar's keys, finds again when the query or the
// files changed, and moves the current match; it reports whether the bar
// shut, which gives the keys back to the view.
func (rf *reviewFind) update(gtx gl.Context, files []review.File) bool {
	if !rf.bar.On() {
		rf.q = nil
		return false
	}
	step, shut := rf.bar.Update(gtx)
	if shut {
		rf.close()
		return true
	}
	rf.q = rf.bar.Query()
	if sig := string(rf.q) + "\x00" + filesSig(files); sig != rf.sig {
		rf.sig, rf.hits, rf.cur = sig, reviewHits(files, rf.q), -1
		if len(rf.hits) > 0 {
			rf.cur, rf.jump = 0, true
		}
	}
	if n := len(rf.hits); step != 0 && n > 0 {
		rf.cur, rf.jump = ((rf.cur+step)%n+n)%n, true
	}
	return false
}

// close shuts the bar and drops its matches.
func (rf *reviewFind) close() {
	rf.bar.Close()
	rf.q, rf.hits, rf.sig, rf.jump = nil, nil, "", false
}

// count is the bar's count: "3 of 12", "No results", "" without a query.
func (rf *reviewFind) count() string {
	switch {
	case len(rf.q) == 0:
		return ""
	case len(rf.hits) == 0:
		return "No results"
	}
	return strconv.Itoa(rf.cur+1) + " of " + strconv.Itoa(len(rf.hits))
}

// current is the current hit's rune in file file's line line, -1 when
// the current hit is elsewhere.
func (rf *reviewFind) current(file, line int) int {
	if rf.cur < 0 || rf.cur >= len(rf.hits) {
		return -1
	}
	if h := rf.hits[rf.cur]; h.file == file && h.line == line {
		return h.at
	}
	return -1
}

// findJump shows the current match: it selects its file, shows a file
// over hugeLines, unfolds the unchanged lines around it, and scrolls its
// row into view.
func (r *reviewView) findJump(files []review.File) {
	rf := &r.find
	if !rf.jump || rf.cur < 0 || rf.cur >= len(rf.hits) {
		return
	}
	rf.jump = false
	h := rf.hits[rf.cur]
	if h.file >= len(files) {
		return
	}
	r.selectFile(files, h.file)
	f := &files[h.file]
	if r.huge(f) {
		if r.shownHuge == nil {
			r.shownHuge = map[string]bool{}
		}
		r.shownHuge[f.Path], r.rowsDirty = true, true
	}
	rows := r.rowsOf(f)
	row := lineRow(rows, h.line)
	if row < 0 {
		for _, w := range rows {
			if w.Kind == 'f' && w.Line <= h.line && h.line < w.Line+w.N {
				r.openFold(f.Key(), w.Line)
				break
			}
		}
		row = lineRow(r.rowsOf(f), h.line)
	}
	r.scrollTo = row
}
