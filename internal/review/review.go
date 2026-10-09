// Package review is the review view's model, free of Gio and git: it parses
// a unified diff into files and lines, groups the lines into hunks with the
// unchanged lines between them folded, and turns review comments into the
// prompt the agent gets.
package review

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// File is one file's change.
type File struct {
	Path    string // relative to the repository root; a rename's new path
	OldPath string // a rename's old path, else ""
	// Status is 'A' added, 'M' modified, 'D' deleted, 'R' renamed or '?'
	// untracked, as gitstat.FileStat has it.
	Status   byte
	Binary   bool
	Add, Del int
	Lines    []Line
	// Hash names the change: it differs once the file changes again.
	Hash string
}

// Line is one line of a file's diff.
type Line struct {
	Kind byte // ' ' unchanged, '+' added, '-' deleted
	// Old and New are 1-based line numbers before and after the change, 0
	// on the side the line is not on.
	Old, New int
	Text     string
}

// Changed reports whether the line was added or deleted.
func (l Line) Changed() bool { return l.Kind != ' ' }

// Parse reads the output of git diff with a/ and b/ prefixes, no color,
// renames found, and any amount of context.
func Parse(patch []byte) []File {
	var files []File
	var f *File
	var start int // where f's text starts in patch
	var old, nu int
	inHunk := false
	finish := func(end int) {
		if f == nil {
			return
		}
		sum := sha256.Sum256(patch[start:end])
		f.Hash = hex.EncodeToString(sum[:8])
		if f.Status == 0 {
			f.Status = 'M'
		}
		files = append(files, *f)
	}
	for at := 0; at < len(patch); {
		end := bytes.IndexByte(patch[at:], '\n')
		next := len(patch)
		if end >= 0 {
			end += at
			next = end + 1
		} else {
			end = len(patch)
		}
		line := string(patch[at:end])
		lineAt := at
		at = next
		if strings.HasPrefix(line, "diff --git ") {
			finish(lineAt)
			f, start, inHunk = &File{Path: headerPath(line)}, lineAt, false
			continue
		}
		if f == nil {
			continue
		}
		if inHunk {
			if line == "" { // a blank context line whose space an editor took
				line = " "
			}
			switch line[0] {
			case ' ':
				old++
				nu++
				f.Lines = append(f.Lines, Line{Kind: ' ', Old: old, New: nu, Text: line[1:]})
				continue
			case '+':
				nu++
				f.Add++
				f.Lines = append(f.Lines, Line{Kind: '+', New: nu, Text: line[1:]})
				continue
			case '-':
				old++
				f.Del++
				f.Lines = append(f.Lines, Line{Kind: '-', Old: old, Text: line[1:]})
				continue
			case '\\': // "\ No newline at end of file"
				continue
			}
			inHunk = false
		}
		switch {
		case strings.HasPrefix(line, "@@ "):
			// @@ -12,3 +12,4 @@: the numbers count from the hunk's first
			// line; -0,0 is a side with no lines.
			var o, n int
			fmt.Sscanf(strings.TrimPrefix(hunkRange(line, '-'), "-"), "%d", &o)
			fmt.Sscanf(strings.TrimPrefix(hunkRange(line, '+'), "+"), "%d", &n)
			old, nu, inHunk = max(o-1, 0), max(n-1, 0), true
		case strings.HasPrefix(line, "new file mode"):
			f.Status = 'A'
		case strings.HasPrefix(line, "deleted file mode"):
			f.Status = 'D'
		case strings.HasPrefix(line, "rename from "):
			f.Status, f.OldPath = 'R', unquote(strings.TrimPrefix(line, "rename from "))
		case strings.HasPrefix(line, "rename to "):
			f.Status, f.Path = 'R', unquote(strings.TrimPrefix(line, "rename to "))
		case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch":
			f.Binary = true
		case strings.HasPrefix(line, "+++ ") && f.Status != 'D':
			if p := strings.TrimPrefix(unquote(strings.TrimPrefix(line, "+++ ")), "b/"); p != "/dev/null" {
				f.Path = p
			}
		case strings.HasPrefix(line, "--- ") && f.Status == 'D':
			f.Path = strings.TrimPrefix(unquote(strings.TrimPrefix(line, "--- ")), "a/")
		}
	}
	finish(len(patch))
	return files
}

// hunkRange is the -a,b or +c,d field of a hunk header.
func hunkRange(header string, sign byte) string {
	for _, f := range strings.Fields(header) {
		if len(f) > 1 && f[0] == sign {
			return f
		}
	}
	return ""
}

// headerPath is the path in "diff --git a/p b/p", which names the same
// file twice when it is not a rename; the ---, +++ and rename lines that
// follow override it.
func headerPath(line string) string {
	rest := strings.TrimPrefix(line, "diff --git ")
	if strings.HasPrefix(rest, `"`) {
		if i := strings.Index(rest, `" `); i > 0 {
			return strings.TrimPrefix(unquote(rest[:i+1]), "a/")
		}
	}
	// "a/x b/x" is 2n+5 bytes for an n-byte path.
	if n := (len(rest) - 5) / 2; n > 0 && strings.HasPrefix(rest, "a/") && rest[2+n:2+n+3] == " b/" && rest[2:2+n] == rest[5+n:] {
		return rest[2 : 2+n]
	}
	return strings.TrimPrefix(strings.SplitN(rest, " ", 2)[0], "a/")
}

// unquote undoes git's C-style quoting of a path with unusual bytes.
func unquote(s string) string {
	if len(s) < 2 || s[0] != '"' {
		return s
	}
	if u, err := strconv.Unquote(s); err == nil {
		return u
	}
	return s
}

// Untracked is the diff of a file git does not track yet: every line
// added. data is its content; huge says it was too big to read, so it
// shows as binary does, without lines.
func Untracked(path string, data []byte, huge bool) File {
	f := File{Path: path, Status: '?'}
	sum := sha256.Sum256(append([]byte(path+"\x00"), data...))
	f.Hash = hex.EncodeToString(sum[:8])
	if huge || bytes.IndexByte(data, 0) >= 0 {
		f.Binary = true
		return f
	}
	text := strings.TrimSuffix(string(data), "\n")
	if text == "" && len(data) == 0 {
		return f
	}
	for i, s := range strings.Split(text, "\n") {
		f.Lines = append(f.Lines, Line{Kind: '+', New: i + 1, Text: strings.TrimSuffix(s, "\r")})
	}
	f.Add = len(f.Lines)
	return f
}

// Hunk is a run of lines shown together: changed lines and up to the
// context's number of unchanged lines either side of them. Start and End
// index File.Lines, End exclusive.
type Hunk struct{ Start, End int }

// Hunks groups lines into hunks with ctx unchanged lines around each
// change, as git diff -U does: changes at most 2*ctx unchanged lines apart
// share a hunk. A file with no changed line has none.
func Hunks(lines []Line, ctx int) []Hunk {
	var out []Hunk
	for i, l := range lines {
		if !l.Changed() {
			continue
		}
		s, e := max(i-ctx, 0), min(i+ctx+1, len(lines))
		if n := len(out); n > 0 && s <= out[n-1].End {
			out[n-1].End = max(out[n-1].End, e)
			continue
		}
		out = append(out, Hunk{s, e})
	}
	return out
}

// Header is the hunk's "@@ -12,7 +12,9 @@" line. A side the hunk has no
// line on starts at the line before it, as git writes it.
func Header(lines []Line, h Hunk) string {
	var o, oc, n, nc int
	for _, l := range lines[h.Start:h.End] {
		if l.Old > 0 {
			if oc == 0 {
				o = l.Old
			}
			oc++
		}
		if l.New > 0 {
			if nc == 0 {
				n = l.New
			}
			nc++
		}
	}
	if oc == 0 {
		o = before(lines[:h.Start], func(l Line) int { return l.Old })
	}
	if nc == 0 {
		n = before(lines[:h.Start], func(l Line) int { return l.New })
	}
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@", o, oc, n, nc)
}

// before is the last non-zero number num gives a line of lines, 0 for none.
func before(lines []Line, num func(Line) int) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if n := num(lines[i]); n > 0 {
			return n
		}
	}
	return 0
}

// Row is one row of the diff as the view draws it.
type Row struct {
	// Kind is 'h' a hunk header, 'f' folded unchanged lines or 'l' a line.
	Kind byte
	// Line indexes File.Lines: the line of an 'l' row, the first folded
	// one of an 'f' row, the hunk's first of an 'h' row.
	Line int
	N    int    // an 'f' row's folded line count
	Text string // an 'h' row's header
}

// Rows lays lines out as hunks: a header over each, and an 'f' row for each
// run of unchanged lines between, before and after them, unless open says
// the run, named by its first line, was expanded. A file with no changed
// line, such as an empty new one, shows every line.
func Rows(lines []Line, ctx int, open func(first int) bool) []Row {
	hunks := Hunks(lines, ctx)
	if len(hunks) == 0 {
		rows := make([]Row, len(lines))
		for i := range lines {
			rows[i] = Row{Kind: 'l', Line: i}
		}
		return rows
	}
	var rows []Row
	gap := func(s, e int) {
		switch {
		case s >= e:
		case open != nil && open(s):
			for i := s; i < e; i++ {
				rows = append(rows, Row{Kind: 'l', Line: i})
			}
		default:
			rows = append(rows, Row{Kind: 'f', Line: s, N: e - s})
		}
	}
	at := 0
	for _, h := range hunks {
		gap(at, h.Start)
		rows = append(rows, Row{Kind: 'h', Line: h.Start, Text: Header(lines, h)})
		for i := h.Start; i < h.End; i++ {
			rows = append(rows, Row{Kind: 'l', Line: i})
		}
		at = h.End
	}
	gap(at, len(lines))
	return rows
}

// Comment is a review comment on a range of one file's lines.
type Comment struct {
	Path  string
	Lines []Line // the lines it is on, in file order, as they were
	Text  string
}

// Ref is where the comment points, "path:12" or "path:12-14" in the new
// file's numbers. Deleted lines have none, so a comment on deleted lines
// alone uses the old file's: "path, deleted lines 12-14".
func (c Comment) Ref() string {
	lo, hi := span(c.Lines, func(l Line) int { return l.New })
	if lo > 0 {
		return c.Path + ":" + lineRange(lo, hi)
	}
	lo, hi = span(c.Lines, func(l Line) int { return l.Old })
	if lo == hi {
		return fmt.Sprintf("%s, deleted line %d", c.Path, lo)
	}
	return fmt.Sprintf("%s, deleted lines %s", c.Path, lineRange(lo, hi))
}

func span(lines []Line, num func(Line) int) (lo, hi int) {
	for _, l := range lines {
		if n := num(l); n > 0 {
			if lo == 0 || n < lo {
				lo = n
			}
			hi = max(hi, n)
		}
	}
	return lo, hi
}

func lineRange(lo, hi int) string {
	if lo == hi {
		return strconv.Itoa(lo)
	}
	return fmt.Sprintf("%d-%d", lo, hi)
}

// maxQuote is how many of a comment's lines its prompt quotes.
const maxQuote = 12

// Prompt is the message comments become for the agent: a line saying what
// it is, then each comment's reference, its lines quoted with their diff
// marks, and its text.
func Prompt(cs []Comment) string {
	var b strings.Builder
	b.WriteString("Review comments on your changes. Each names a file and line, quotes the diff, then says what to change.\n")
	for _, c := range cs {
		b.WriteString("\n")
		b.WriteString(c.Ref())
		b.WriteString("\n")
		for i, l := range c.Lines {
			if i == maxQuote {
				fmt.Fprintf(&b, "> ... %d more lines\n", len(c.Lines)-maxQuote)
				break
			}
			b.WriteString("> ")
			b.WriteByte(l.Kind)
			b.WriteString(l.Text)
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimSpace(c.Text))
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}
