package panel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/mdview"
)

// fileEdit is one file's part of an edit call: its lines, and whether
// the call made the file.
type fileEdit struct {
	path    string
	lines   []diffLine
	created bool
}

// fileChange is a file a turn edited: its lines added and removed over
// all the turn's edits to it, and each edit's lines in order.
type fileChange struct {
	path     string // relative to the chat's folder when under it
	add, del int
	created  bool // the turn's first edit to it made it
	diffs    [][]diffLine
}

// isEdit is whether a tool of this name edits files.
func isEdit(tool string) bool {
	switch tool {
	case "Edit", "MultiEdit", "Write", "NotebookEdit", "edit", "write", "write_file", "replace", "apply_patch":
		return true
	}
	return false
}

// callEdits are the files an edit call changed: Codex's changes one per
// file, an apply_patch body split at its file markers, else the one file
// the call names with lines, its diff as the tool row draws it.
func callEdits(c *session.ToolCall, lines []diffLine) []fileEdit {
	if c == nil || !isEdit(c.ToolName) {
		return nil
	}
	in := inputMap(c.Input)
	if raw := in["changes"]; raw != nil {
		var chs []struct {
			Path string `json:"path"`
			Diff string `json:"diff"`
			Kind struct {
				Type string `json:"type"`
			} `json:"kind"`
		}
		if json.Unmarshal(raw, &chs) == nil && len(chs) > 0 {
			var out []fileEdit
			for _, ch := range chs {
				e := fileEdit{path: ch.Path, created: ch.Kind.Type == "add"}
				if e.created && !strings.HasPrefix(ch.Diff, "@@") && !strings.HasPrefix(ch.Diff, "+") {
					e.lines = numbered(lineDiff("", ch.Diff)) // a new file's text, not a diff
				} else {
					e.lines = unified(ch.Diff)
				}
				out = append(out, e)
			}
			return out
		}
	}
	if body := in.str("input", "patch"); strings.Contains(body, "*** Begin Patch") {
		return patchEdits(body)
	}
	p := in.str("file_path", "path", "notebook_path")
	if p == "" && len(c.Files) > 0 {
		p = c.Files[0].Path
	}
	if p == "" {
		return nil
	}
	created := len(c.Files) > 0 && c.Files[0].Status == "add"
	return []fileEdit{{path: p, lines: lines, created: created}}
}

// patchEdits splits an apply_patch body into its files.
func patchEdits(body string) []fileEdit {
	var out []fileEdit
	var cur *fileEdit
	var part []string
	flush := func() {
		if cur != nil {
			cur.lines = unified(strings.Join(part, "\n"))
			out = append(out, *cur)
		}
		cur, part = nil, nil
	}
	for l := range strings.SplitSeq(body, "\n") {
		for _, m := range [...]string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
			if p, ok := strings.CutPrefix(l, m); ok {
				flush()
				cur = &fileEdit{path: strings.TrimSpace(p), created: m == "*** Add File: "}
				l = ""
			}
		}
		if cur != nil && l != "" {
			part = append(part, l)
		}
	}
	flush()
	return out
}

// numbered gives a new file's lines their numbers from 1.
func numbered(ls []diffLine) []diffLine {
	for i := range ls {
		ls[i].new = i + 1
	}
	return ls
}

// planFile is whether p is a plan Claude Code wrote in plan mode, under
// ~/.claude/plans: its own notes, not a change to the user's files.
func planFile(p string) bool {
	d := filepath.Dir(filepath.Clean(p))
	return filepath.Base(d) == "plans" && filepath.Base(filepath.Dir(d)) == ".claude"
}

// turnChanges are the files content[lo:hi] edited, in the order the turn
// first touched them, each counted once over its edits. A failed or
// running call changed nothing yet. diff is an edit item's lines.
func turnChanges(content []session.Item, lo, hi int, dir string, diff func(*session.Item) []diffLine) []fileChange {
	var out []fileChange
	at := map[string]int{}
	for i := lo; i < min(hi, len(content)); i++ {
		it := &content[i]
		c := it.Tool
		if it.Kind != session.KindToolCall || c == nil || !isEdit(c.ToolName) || c.Status == "failed" || c.Status == "running" {
			continue
		}
		for _, e := range callEdits(c, diff(it)) {
			if planFile(e.path) {
				continue
			}
			p := relPath(dir, filepath.Clean(e.path))
			k, ok := at[p]
			if !ok {
				k = len(out)
				at[p] = k
				out = append(out, fileChange{path: p, created: e.created})
			}
			f := &out[k]
			for _, l := range e.lines {
				switch l.kind {
				case '+':
					f.add++
				case '-':
					f.del++
				}
			}
			f.diffs = append(f.diffs, e.lines)
		}
	}
	return out
}

// changeTotals are a turn's lines added and removed over its files.
func changeTotals(fs []fileChange) (add, del int) {
	for _, f := range fs {
		add, del = add+f.add, del+f.del
	}
	return add, del
}

// changeLines are f's edits as one diff: each edit's hunks, an edit
// without hunk headers under one of its own.
func changeLines(f fileChange) []diffLine {
	var out []diffLine
	for i, ls := range f.diffs {
		if len(f.diffs) > 1 && (len(ls) == 0 || ls[0].kind != '@') {
			out = append(out, diffLine{kind: '@', text: fmt.Sprintf("Edit %d of %d", i+1, len(f.diffs))})
		}
		out = append(out, ls...)
	}
	return out
}

// The side panel's file limits: source past maxSource shows its start,
// a picture past maxPicture does not show.
const (
	maxSource  = 400 << 10
	maxPicture = 5 << 20
)

// previewKind is what the File view draws.
type previewKind uint8

const (
	previewNone    previewKind = iota // refused: why says why
	previewText                       // text: mono with line numbers
	previewMD                         // markdown: through mdview
	previewPicture                    // an image
)

// preview is a file read for the File view.
type preview struct {
	path string // absolute, as resolved
	kind previewKind
	text string
	img  image.Image
	size int64
	cut  bool   // text is the file's first maxSource bytes
	why  string // previewNone: the row it shows instead
}

// errOutside is a path that resolves outside the chat's folder.
var errOutside = errors.New("outside the chat's folder")

// insideRoot resolves p, relative to root when not absolute, through its
// links, and refuses it when it lands outside root.
func insideRoot(root, p string) (string, error) {
	if root == "" {
		return "", errOutside
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	rr, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	if r, err := filepath.Rel(rr, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", errOutside
	}
	return real, nil
}

// pictureExt are the extensions the File view shows as a picture.
var pictureExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".bmp": true, ".svg": true}

// readPreview reads p, under root, for the File view: a picture up to
// maxPicture, markdown or text up to maxSource and cut there, and a
// row saying why for a folder, a binary file, a file too big, one that
// is gone or one outside root.
func readPreview(root, p string) preview {
	pv := preview{path: p}
	real, err := insideRoot(root, p)
	switch {
	case errors.Is(err, errOutside):
		pv.why = "Outside this chat's folder, not shown"
		return pv
	case errors.Is(err, os.ErrNotExist):
		pv.why = "Not on disk: deleted or moved since"
		return pv
	case err != nil:
		pv.why = "Not readable: " + plainErr(err)
		return pv
	}
	pv.path = real
	fi, err := os.Stat(real)
	if err != nil {
		pv.why = "Not readable: " + plainErr(err)
		return pv
	}
	pv.size = fi.Size()
	if !fi.Mode().IsRegular() {
		pv.why = "A folder, not a file"
		return pv
	}
	ext := strings.ToLower(filepath.Ext(real))
	if pictureExt[ext] {
		if fi.Size() > maxPicture {
			pv.why = "Picture over 5 MiB (" + byteSize(fi.Size()) + "), not shown"
			return pv
		}
		b, err := os.ReadFile(real)
		if err == nil {
			pv.img, err = mdview.Decode(b, 1600)
		}
		if err != nil {
			pv.why = "Picture not shown: " + plainErr(err)
			return pv
		}
		pv.kind = previewPicture
		return pv
	}
	f, err := os.Open(real)
	if err != nil {
		pv.why = "Not readable: " + plainErr(err)
		return pv
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSource+1))
	if err != nil {
		pv.why = "Not readable: " + plainErr(err)
		return pv
	}
	if len(b) > maxSource {
		b, pv.cut = b[:maxSource], true
		if i := bytes.LastIndexByte(b, '\n'); i > 0 {
			b = b[:i]
		}
	}
	if binary(b, pv.cut) {
		pv.why = "Binary file (" + byteSize(fi.Size()) + "), not shown"
		return pv
	}
	pv.text, pv.kind = string(b), previewText
	if mdview.IsMarkdown(real) {
		pv.kind = previewMD
	}
	return pv
}

// binary is whether b is not text: a NUL in its first 8 KB, or bytes that
// are not UTF-8. cut allows a rune split at its end.
func binary(b []byte, cut bool) bool {
	if bytes.IndexByte(b[:min(len(b), 8<<10)], 0) >= 0 {
		return true
	}
	if cut {
		for i := 0; i < utf8.UTFMax && len(b) > 0 && !utf8.Valid(b); i++ {
			b = b[:len(b)-1]
		}
	}
	return !utf8.Valid(b)
}

// byteSize is n as a file size: 812 B, 14 KB, 2.4 MB.
func byteSize(n int64) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d B", n)
	case n < 1<<20:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}

// plainErr is err without the path an os error repeats.
func plainErr(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err.Error()
	}
	return err.Error()
}
