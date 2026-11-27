package mobile

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
)

// The phone's chat folds a turn's work into one line, "Worked 2m 14s ·
// 6 tools · 1 failed", and puts its edits on one card, "Edited 2 files
// +18 −4", after the reply. A turn runs from a prompt to the next; its
// work is its calls and every reply but its last. The desktop's Worked
// line counts the same calls.

// rowKind is what a row of the chat's list draws.
type rowKind uint8

const (
	rowEntry  rowKind = iota // an entry as it is: a prompt, the reply, an error
	rowWork                  // a turn's Worked line
	rowFolded                // an entry of an open Worked line's work
	rowDiff                  // a turn's diff card
)

// logRow is a row of the chat's list: an entry by its index, or a turn's
// line or card by the turn's index.
type logRow struct {
	kind rowKind
	i    int // the entry, for rowEntry and rowFolded; the turn otherwise
	at   int // the first entry the row stands for, for Since you left
}

// turnWork is a turn's work as its line and card say it.
type turnWork struct {
	key     int64 // the first work entry's seq: what its fold is kept by
	tools   int
	failed  int
	running bool // a call runs, or the tab works on this, the last turn
	start   int64
	end     int64 // Unix ms, the turn's first and latest entry; 0 unknown
	files   []fileEdit
}

// fileEdit is a file's lines added and removed over a turn's edits.
type fileEdit struct {
	name, path string
	add, del   int
}

// isEdit reports whether call e changes a file.
func isEdit(e flow.Entry) bool {
	switch e.Tool {
	case "Edit", "MultiEdit", "Write", "NotebookEdit", "edit", "write", "write_file", "replace", "apply_patch":
		return true
	}
	return e.Add > 0 || e.Del > 0
}

// buildRows is es as the list's rows and the turns' work, into the
// buffers given; open says which Worked lines are open, by key; busy is
// whether the tab works, so the last turn's line says Working.
func buildRows(rows []logRow, turns []turnWork, es []flow.Entry, open func(int64) bool, busy bool) ([]logRow, []turnWork) {
	rows, turns = rows[:0], turns[:0]
	for lo := 0; lo < len(es); {
		hi := lo + 1
		for hi < len(es) && es[hi].Kind != flow.EntryUser {
			hi++
		}
		rows, turns = turn(rows, turns, es, lo, hi, open, busy && hi == len(es))
		lo = hi
	}
	return rows, turns
}

// turn adds the rows of es[lo:hi], one turn.
func turn(rows []logRow, turns []turnWork, es []flow.Entry, lo, hi int, open func(int64) bool, last bool) ([]logRow, []turnWork) {
	final := -1 // the reply that stays out of the fold
	for j := hi - 1; j >= lo; j-- {
		if es[j].Kind == flow.EntryAssistant {
			final = j
			break
		}
	}
	work := func(j int) bool {
		k := es[j].Kind
		return k == flow.EntryTool || k == flow.EntryAssistant && j != final
	}
	first := -1
	for j := lo; j < hi; j++ {
		if work(j) {
			first = j
			break
		}
	}
	if first < 0 {
		for j := lo; j < hi; j++ {
			rows = append(rows, logRow{rowEntry, j, j})
		}
		return rows, turns
	}
	t := turnWork{key: es[first].Seq, running: last, start: es[lo].At}
	for j := lo; j < hi; j++ {
		e := es[j]
		if e.At > 0 {
			t.end = max(t.end, e.At)
			if t.start == 0 {
				t.start = e.At
			}
		}
		if e.Kind != flow.EntryTool {
			continue
		}
		t.tools++
		switch {
		case !e.Done:
			t.running = true
		case e.Failed:
			t.failed++
		case isEdit(e):
			p := e.Path
			if p == "" {
				p = e.Arg
			}
			k := slices.IndexFunc(t.files, func(f fileEdit) bool { return f.path == p })
			if k < 0 {
				k = len(t.files)
				t.files = append(t.files, fileEdit{name: e.Arg, path: p})
			}
			t.files[k].add += e.Add
			t.files[k].del += e.Del
		}
	}
	ti := len(turns)
	turns = append(turns, t)
	isOpen := open(t.key)
	for j := lo; j < hi; j++ {
		switch {
		case j == first:
			rows = append(rows, logRow{rowWork, ti, j})
			if isOpen {
				rows = append(rows, logRow{rowFolded, j, j})
			}
		case work(j):
			if isOpen {
				rows = append(rows, logRow{rowFolded, j, j})
			}
		default:
			rows = append(rows, logRow{rowEntry, j, j})
		}
	}
	if len(t.files) > 0 && !t.running {
		rows = append(rows, logRow{rowDiff, ti, hi - 1})
	}
	return rows, turns
}

// title is the Worked line's words: "Worked 2m 14s", or "Working 40s"
// to now while the turn runs; "Worked" alone when the times are unknown.
func (t turnWork) title(now time.Time) string {
	verb, end := "Worked", t.end
	if t.running {
		verb, end = "Working", now.UnixMilli()
	}
	if t.start == 0 || end-t.start < 1000 {
		return verb
	}
	return verb + " " + took(time.Duration(end-t.start)*time.Millisecond)
}

// took is d as the Worked line says it: 14s, 2m 14s, 1h 05m, as the
// desktop's.
func took(d time.Duration) string {
	s := max(0, int(d/time.Second))
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm", s/3600, s/60%60)
}

// totals are the lines t's edits add and remove.
func (t turnWork) totals() (add, del int) {
	for _, f := range t.files {
		add, del = add+f.add, del+f.del
	}
	return add, del
}

// plural is "1 tool", "6 tools".
func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}
