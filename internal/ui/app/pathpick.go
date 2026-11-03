package app

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/nowindow"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// maxPathFiles bounds the files the path picker lists, and the entries its
// walk outside a repository visits.
const maxPathFiles = 20000

// maxPathRows is the most matches the picker shows; typing narrows them.
const maxPathRows = 200

// pathPick is the path picker's state: the pane it types into, the files
// under the pane's folder, listed off the UI goroutine, and the ones marked.
// The command palette's card draws it (u.pal.files).
type pathPick struct {
	pane   string
	dir    string
	files  []string // nil while the listing runs
	err    string   // why there are no files
	done   chan pathList
	marked []string

	query string   // the query rows were ranked for
	rows  []string // its best matches, at most maxPathRows
	n     int      // how many matched
}

type pathList struct {
	files []string
	err   error
}

// openPathPick opens the path picker on the focused pane and starts
// listing the files under its folder.
func (u *ui) openPathPick(st *model.State, now time.Time) {
	id := u.nav.focused()
	pn := findPane(st, id)
	if pn == nil {
		return
	}
	u.pal.search, u.pal.tabs, u.pal.files = false, false, true
	u.pal.openAt(now)
	u.paths = pathPick{pane: id, dir: pn.Cwd}
	switch {
	case curHost() != "":
		// ponytail: the files are on the host; a proto request would list them there.
		u.paths.err = "Insert path lists files on this machine only."
		return
	case pn.Cwd == "":
		u.paths.err = "The pane's folder is unknown."
		return
	}
	done := make(chan pathList, 1)
	u.paths.done = done
	invalidate := u.invalidate
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		files, err := listPaths(ctx, pn.Cwd)
		done <- pathList{files, err}
		if invalidate != nil {
			invalidate()
		}
	}()
}

// pathRows is the picker's rows for the palette's query now, ranked once
// per query.
func (u *ui) pathRows() []string {
	pp := &u.paths
	if pp.done != nil {
		select {
		case l := <-pp.done:
			pp.done, pp.files = nil, l.files
			if pp.files == nil {
				pp.files = []string{}
			}
			if l.err != nil {
				pp.err = "Can't list " + model.ShortPath(pp.dir) + ": " + l.err.Error()
			}
			pp.query = "\x00" // rank again
		default:
		}
	}
	if pp.query != u.pal.query {
		pp.query = u.pal.query
		pp.rows, pp.n = rankPaths(pp.files, pp.query, maxPathRows)
	}
	return pp.rows
}

// listPaths lists the files under dir, relative to it: in a repository,
// git's tracked and untracked but not ignored files that are in the
// folder; elsewhere a walk that skips folders whose names start with a
// dot. Either stops at maxPathFiles. A failure or a cancelled ctx is an
// error, never a walk of a repository.
func listPaths(ctx context.Context, dir string) ([]string, error) {
	if out, err := gitOutput(ctx, dir, "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(string(out)) != "true" {
		return walkPaths(ctx, dir, maxPathFiles)
	}
	out, err := gitOutput(ctx, dir, "-c", "core.quotePath=false", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, cmp.Or(ctx.Err(), err)
	}
	all := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	slices.Sort(all)          // untracked files come after the tracked ones
	all = slices.Compact(all) // a conflict lists a file once per stage
	var files []string
	for _, f := range all {
		if len(files) == maxPathFiles {
			break
		}
		// --cached keeps a file deleted from the folder, or left out of a
		// sparse checkout, until the deletion is staged.
		if _, err := os.Lstat(filepath.Join(dir, f)); f != "" && err == nil {
			files = append(files, f)
		}
	}
	return files, nil
}

// gitOutput runs git with args in dir and returns its output, or its error
// message as the error.
func gitOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	nowindow.Set(cmd)
	out, err := cmd.Output()
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		err = errors.New(strings.TrimSpace(string(ee.Stderr)))
	}
	return out, err
}

// walkPaths lists the files under dir, relative to it, skipping folders
// whose names start with a dot, until it has seen limit entries or ctx
// ends.
func walkPaths(ctx context.Context, dir string, limit int) ([]string, error) {
	var files []string
	seen := 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if p == dir {
				return err
			}
			return nil // an unreadable folder: skip it
		}
		if p == dir {
			return nil
		}
		if seen++; seen > limit {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if rel, err := filepath.Rel(dir, p); err == nil {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// rankPaths is the files every word of query matches, as rank's words
// do, best first and at most limit, and how many matched. A word scores
// against the whole path and again against its base name, so "app" puts
// app.go above approve/x.go; ties go to the shorter path.
func rankPaths(files []string, query string, limit int) ([]string, int) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return files[:min(len(files), limit)], len(files)
	}
	type scored struct {
		f string
		n int
	}
	var out []scored
	for _, f := range files {
		base := filepath.Base(f)
		total := 0
		for _, w := range words {
			n := fuzzy(w, f)
			if n < 0 {
				total = -1
				break
			}
			total += n + max(fuzzy(w, base), 0)
		}
		if total >= 0 {
			out = append(out, scored{f, total})
		}
	}
	slices.SortStableFunc(out, func(a, b scored) int { return cmp.Or(b.n-a.n, len(a.f)-len(b.f)) })
	rows := make([]string, min(len(out), limit))
	for i := range rows {
		rows[i] = out[i].f
	}
	return rows, len(out)
}

// pathPickKey runs one key in the open path picker. Its own shortcut and
// Escape close it, Tab or Ctrl+Space marks the highlighted file and moves
// on, Enter types the marked files' paths, or the highlighted one's, at the
// pane's cursor, and the rest edit and move as in the command palette.
func (u *ui) pathPickKey(e key.Event) {
	p, pp := &u.pal, &u.paths
	if e.State != key.Press {
		return
	}
	rows := u.pathRows()
	switch {
	case e.Name == key.NameEscape || u.nav.bind().Action(e) == "insert_path":
		p.close()
	case e.Name == key.NameReturn || e.Name == key.NameEnter:
		paths := pp.marked
		if len(paths) == 0 && p.sel < len(rows) {
			paths = rows[p.sel : p.sel+1]
		}
		if len(paths) > 0 {
			u.insertPaths(pp.pane, paths)
			p.close()
		}
	case e.Name == key.NameTab && e.Modifiers == 0, e.Name == key.NameSpace && e.Modifiers == key.ModCtrl:
		if p.sel < len(rows) {
			pp.toggle(rows[p.sel])
			p.sel = min(p.sel+1, len(rows)-1)
		}
	default:
		p.key(make([]paletteEntry, len(rows)), e)
	}
	if !p.open {
		u.nav.swallow = e.Name
	}
}

// toggle marks f, or unmarks it.
func (pp *pathPick) toggle(f string) {
	if i := slices.Index(pp.marked, f); i >= 0 {
		pp.marked = slices.Delete(pp.marked, i, i+1)
	} else {
		pp.marked = append(pp.marked, f)
	}
}

// insertPaths types paths at pane's cursor, separated by spaces, each
// quoted when the shell needs it, as a bracketed paste when the program
// asked for one.
func (u *ui) insertPaths(pane string, paths []string) {
	_, m, _ := u.b.Frame(pane)
	if data := input.TypePaths(paths, m); len(data) > 0 {
		u.send(proto.Input{Pane: pane, Data: data})
	}
}
