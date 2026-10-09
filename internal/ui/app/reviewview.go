package app

import (
	"bytes"
	"context"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/review"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

const (
	// reviewCtx is how many unchanged lines show around a change.
	reviewCtx = 3
	// hugeLines is how many changed lines a file may have before the view
	// shows a line about it instead, until asked.
	hugeLines = 3000
)

// readDiff and discardFiles are what the review view runs git through;
// tests replace them.
var (
	readDiff     = gitstat.Diff
	discardFiles = gitstat.Discard
)

type noteState int

const (
	notePending noteState = iota
	noteQueued            // in a delivery waiting for the agent
	noteSent
)

// note is a review comment and where it is on its way to the agent.
type note struct {
	review.Comment
	state noteState
	del   widget.Clickable
}

// delivery is comments typed into pane once its agent waits for input.
type delivery struct {
	pane  string
	text  string
	notes []*note
}

// reviewView is the review view: one tab's changes from the merge base
// with the default branch, committed or not, shown in place of its panes
// like the settings page. A goroutine reads the diff, the UI goroutine
// never waits on git.
type reviewView struct {
	on  bool
	ws  string // the tab shown; another tab closes the view
	dir string // the tab's live directory

	mu     sync.Mutex
	gen    int
	stop   context.CancelFunc
	kick   chan struct{} // reads the diff again now
	loaded bool
	err    string
	base   string
	root   string
	files  []review.File

	sel         int    // the selected file
	selPath     string // its path, which keeps the selection across reads
	cur, anchor int    // the selected lines of the selected file, both ends; -1 for none
	opened      map[string]map[int]bool
	shownHuge   map[string]bool
	rows        []review.Row
	rowsKey     string // the path and hash rows are of
	rowsDirty   bool
	scrollTo    int // a row to scroll into view next frame, -1 for none

	fileList, diffList gl.List
	fileClick          map[string]*widget.Clickable
	checkClick         map[string]*widget.Clickable
	gutterClick        map[int]*widget.Clickable
	lineClick          map[int]*widget.Clickable
	foldClick          map[int]*widget.Clickable
	tag                int // takes the view's keys
	focus              bool

	edit             widget.Editor
	editing          bool
	editFrom, editTo int

	send, pr, pager, mark, discard, showHuge widget.Clickable
	status                                   string // the line under the header: sent, queued, a failure

	notes      map[string][]*note // by repository root
	queue      []delivery
	enterPane  string // the pane whose paste waits for its Enter
	enterAt    time.Time
	discarding review.File // the file the discard dialog is about
}

// show opens the view on tab ws, reading dir.
func (r *reviewView) show(ws, dir string, invalidate func()) {
	r.hide()
	r.on, r.ws, r.dir, r.focus = true, ws, dir, true
	r.sel, r.selPath, r.cur, r.anchor, r.scrollTo, r.rowsDirty, r.editing, r.status = 0, "", -1, -1, -1, true, false, ""
	r.fileList.Axis, r.diffList.Axis = gl.Vertical, gl.Vertical
	r.fileList.Position, r.diffList.Position = gl.Position{}, gl.Position{}
	r.mu.Lock()
	r.gen++
	r.loaded, r.err, r.files = false, "", nil
	ctx, cancel := context.WithCancel(context.Background())
	r.stop, r.kick = cancel, make(chan struct{}, 1)
	gen, kick := r.gen, r.kick
	r.mu.Unlock()
	go r.load(ctx, gen, dir, kick, invalidate)
}

// hide closes the view and stops its reader. Comments and queued
// deliveries stay.
func (r *reviewView) hide() {
	r.on, r.editing = false, false
	r.mu.Lock()
	if r.stop != nil {
		r.stop()
		r.stop = nil
	}
	r.gen++
	r.mu.Unlock()
}

// reload reads the diff again now.
func (r *reviewView) reload() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.kick != nil {
		select {
		case r.kick <- struct{}{}:
		default:
		}
	}
}

// load reads dir's diff every filesEvery, or when kicked, until ctx is
// done, and invalidates when it changed.
func (r *reviewView) load(ctx context.Context, gen int, dir string, kick chan struct{}, invalidate func()) {
	t := time.NewTicker(filesEvery)
	defer t.Stop()
	var last []byte
	var parsed []review.File // last's files: an unchanged diff is not parsed again
	for {
		c, cancel := context.WithTimeout(ctx, filesWait)
		p, err := readDiff(c, dir)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.update(gen, p, nil, err.Error(), invalidate)
		} else {
			if parsed == nil || !bytes.Equal(last, p.Diff) {
				parsed, last = review.Parse(p.Diff), p.Diff
			}
			files := slices.Clip(parsed)
			for _, path := range p.Untracked {
				b, ok := gitstat.ReadUntracked(p.Root, path)
				files = append(files, review.Untracked(path, b, !ok))
			}
			r.update(gen, p, files, "", invalidate)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
	}
}

func (r *reviewView) update(gen int, p gitstat.Patch, files []review.File, err string, invalidate func()) {
	r.mu.Lock()
	changed := r.gen == gen && (!r.loaded || r.err != err || r.base != p.Base || !sameFiles(r.files, files))
	if changed {
		r.loaded, r.err, r.base, r.root, r.files = true, err, p.Base, p.Root, files
	}
	r.mu.Unlock()
	if changed {
		invalidate()
	}
}

func sameFiles(a, b []review.File) bool {
	return slices.EqualFunc(a, b, func(x, y review.File) bool { return x.Path == y.Path && x.Hash == y.Hash })
}

// snapshot is what the view draws this frame.
func (r *reviewView) snapshot() (files []review.File, base, root, err string, loaded bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.files, r.base, r.root, r.err, r.loaded
}

// selectFile selects files[i], or keeps the selection on the same path
// after a read moved it.
func (r *reviewView) selectFile(files []review.File, i int) {
	if len(files) == 0 {
		r.sel, r.selPath = 0, ""
		return
	}
	i = min(max(i, 0), len(files)-1)
	if files[i].Path != r.selPath {
		r.cur, r.anchor, r.editing = -1, -1, false
		r.diffList.Position = gl.Position{}
	}
	r.sel, r.selPath, r.rowsDirty = i, files[i].Path, true
}

// follow keeps the selection on its path in a fresh read.
func (r *reviewView) follow(files []review.File) {
	if i := slices.IndexFunc(files, func(f review.File) bool { return f.Path == r.selPath }); i >= 0 {
		if i != r.sel {
			r.sel, r.rowsDirty = i, true
		}
		return
	}
	r.selectFile(files, r.sel)
}

// file is the selected file, or nil.
func (r *reviewView) file(files []review.File) *review.File {
	if r.sel < len(files) {
		return &files[r.sel]
	}
	return nil
}

// huge reports whether f shows a line about its size instead of its lines.
func (r *reviewView) huge(f *review.File) bool {
	return f.Add+f.Del > hugeLines && !r.shownHuge[f.Path]
}

// rowsOf is f's rows as the view draws them now.
func (r *reviewView) rowsOf(f *review.File) []review.Row {
	if key := f.Path + "\x00" + f.Hash; !r.rowsDirty && r.rows != nil && key == r.rowsKey {
		return r.rows
	}
	r.rowsDirty, r.rowsKey = false, f.Path+"\x00"+f.Hash
	if f.Binary || r.huge(f) {
		r.rows = []review.Row{}
		return r.rows
	}
	open := r.opened[f.Path]
	r.rows = review.Rows(f.Lines, reviewCtx, func(first int) bool { return open[first] })
	return r.rows
}

// openFold shows the unchanged lines from first on.
func (r *reviewView) openFold(path string, first int) {
	if r.opened == nil {
		r.opened = map[string]map[int]bool{}
	}
	if r.opened[path] == nil {
		r.opened[path] = map[int]bool{}
	}
	r.opened[path][first], r.rowsDirty = true, true
}

// lineRow is the row showing line i, or -1.
func lineRow(rows []review.Row, i int) int {
	return slices.IndexFunc(rows, func(r review.Row) bool { return r.Kind == 'l' && r.Line == i })
}

// moveLine moves the selected line d rows up or down among the lines
// showing, extending the range when extend is set.
func (r *reviewView) moveLine(rows []review.Row, d int, extend bool) {
	at := lineRow(rows, r.cur)
	for i := at + d; i >= 0 && i < len(rows); i += d {
		if rows[i].Kind == 'l' {
			r.setLine(rows[i].Line, extend)
			r.scrollTo = i
			return
		}
	}
	if at < 0 {
		for i, row := range rows {
			if row.Kind == 'l' {
				r.setLine(row.Line, false)
				r.scrollTo = i
				return
			}
		}
	}
}

func (r *reviewView) setLine(i int, extend bool) {
	if !extend || r.anchor < 0 {
		r.anchor = i
	}
	r.cur = i
}

// span is the selected lines' first and last index.
func (r *reviewView) span() (int, int) {
	return min(r.anchor, r.cur), max(r.anchor, r.cur)
}

// hunk moves to the next (d=1) or previous (d=-1) hunk header after the
// selected line or the top row shown, selecting its first changed line;
// it reports false when there is none that way.
func (r *reviewView) hunk(f *review.File, rows []review.Row, d int) bool {
	at := r.diffList.Position.First
	if row := lineRow(rows, r.cur); row >= 0 {
		at = row
	}
	if d < 0 { // from the header of the hunk at, not inside it
		for at >= 0 && at < len(rows) && rows[at].Kind != 'h' {
			at--
		}
	}
	for i := at + d; i >= 0 && i < len(rows); i += d {
		if rows[i].Kind != 'h' {
			continue
		}
		first := rows[i].Line
		for j := first; j < len(f.Lines); j++ {
			if f.Lines[j].Changed() {
				first = j
				break
			}
		}
		r.setLine(first, false)
		r.scrollTo = i
		return true
	}
	return false
}

// startComment opens the comment field under the selected lines.
func (r *reviewView) startComment() {
	if r.cur < 0 {
		return
	}
	r.editFrom, r.editTo = r.span()
	r.editing, r.focus = true, true
	r.edit.Submit = true
	r.edit.SetText("")
}

// addComment saves the comment field's text on the lines it is under.
func (r *reviewView) addComment(f *review.File, root string) {
	text := strings.TrimSpace(r.edit.Text())
	r.editing, r.focus = false, true
	if text == "" || f == nil || r.editTo >= len(f.Lines) {
		return
	}
	if r.notes == nil {
		r.notes = map[string][]*note{}
	}
	lines := slices.Clone(f.Lines[r.editFrom : r.editTo+1])
	r.notes[root] = append(r.notes[root], &note{Comment: review.Comment{Path: f.Path, Lines: lines, Text: text}})
}

// pending is root's comments not yet sent or queued.
func (r *reviewView) pending(root string) []*note {
	var out []*note
	for _, n := range r.notes[root] {
		if n.state == notePending {
			out = append(out, n)
		}
	}
	return out
}

// notesAt maps each line of f to the comments shown under it: the ones
// whose last line it is. A pending comment whose line is gone from the
// diff shows above it, at -1; a sent one is dropped then, its work done.
func notesAt(notes []*note, f *review.File) map[int][]*note {
	out := map[int][]*note{}
	for _, n := range notes {
		if n.Path != f.Path {
			continue
		}
		i := slices.Index(f.Lines, n.Lines[len(n.Lines)-1])
		if i < 0 && n.state == noteSent {
			continue
		}
		out[i] = append(out[i], n)
	}
	return out
}

// agentPane is the pane of tab ws that comments go to: the first live one
// an agent runs in, as hooks or detection report it.
func agentPane(st *model.State, ws *model.Workspace) *model.Pane {
	if ws == nil {
		return nil
	}
	for _, t := range ws.Tabs {
		for _, id := range panesOf(t.Layout) {
			if p := findPane(st, id); p != nil && !p.Exited && p.Provider != "" && p.Provider != model.ProviderTerminal {
				return p
			}
		}
	}
	return nil
}

// agentReady reports whether pane's agent takes a prompt now: it sits at
// its prompt with no activity, finished its turn, or asks a question.
// Busy, asking permission or showing a plan, it does not.
func agentReady(st *model.State, pane string) bool {
	for _, a := range st.Activities {
		if a.PaneID == pane {
			return remote.Replies(a.State)
		}
	}
	return true
}

// sendNotes queues root's pending comments, as one prompt, for tab ws's
// agent, and delivers it now when the agent is ready.
func (u *ui) sendNotes(st *model.State, root string, now time.Time) {
	r := &u.review
	p := agentPane(st, findWorkspace(st, r.ws))
	notes := r.pending(root)
	if p == nil || len(notes) == 0 {
		return
	}
	cs := make([]review.Comment, len(notes))
	for i, n := range notes {
		cs[i], n.state = n.Comment, noteQueued
	}
	text := review.Prompt(cs)
	if i := slices.IndexFunc(r.queue, func(d delivery) bool { return d.pane == p.ID }); i >= 0 {
		r.queue[i].text += "\n\n" + text
		r.queue[i].notes = append(r.queue[i].notes, notes...)
	} else {
		r.queue = append(r.queue, delivery{pane: p.ID, text: text, notes: notes})
	}
	r.status = "Queued until " + agentName(p) + " waits for input"
	u.deliver(st, now)
}

func agentName(p *model.Pane) string {
	if p.Provider == "" {
		return "the agent"
	}
	return string(p.Provider)
}

// replyDelay separates a delivery's paste from its Enter, as the phone's
// replies do. why: a TUI may read an Enter in the same read as a paste as
// part of it.
const replyDelay = 100 * time.Millisecond

// deliver types the first delivery whose agent is ready into its pane,
// as a bracketed paste, and presses Enter replyDelay later, the way the
// phone's replies go in. A delivery whose pane is gone is dropped and its
// comments are pending again. It returns when it next needs a frame, zero
// for no need.
func (u *ui) deliver(st *model.State, now time.Time) time.Time {
	r := &u.review
	if r.enterPane != "" {
		if now.Before(r.enterAt) {
			return r.enterAt
		}
		_, m, _ := u.b.Frame(r.enterPane)
		u.send(proto.Input{Pane: r.enterPane, Data: input.Key(key.Event{Name: key.NameReturn, State: key.Press}, m)})
		r.enterPane = ""
	}
	for i, d := range r.queue {
		p := findPane(st, d.pane)
		if p == nil || p.Exited {
			for _, n := range d.notes {
				n.state = notePending
			}
			r.queue = slices.Delete(r.queue, i, i+1)
			r.status = "The agent's pane closed; the comments were not sent"
			return now
		}
		if !agentReady(st, d.pane) {
			continue
		}
		_, m, _ := u.b.Frame(d.pane)
		text := remote.Clean(d.text)
		if !m.BracketedPaste {
			text = strings.ReplaceAll(text, "\n", " ") // a bare newline would send the first line alone
		}
		if !u.send(proto.Input{Pane: d.pane, Data: input.Paste(text, m)}) {
			return time.Time{}
		}
		log.Printf("review: sent %d comments to pane %s", len(d.notes), d.pane)
		for _, n := range d.notes {
			n.state = noteSent
		}
		r.queue = slices.Delete(r.queue, i, i+1)
		r.enterPane, r.enterAt = d.pane, now.Add(replyDelay)
		r.status = "Sent to " + agentName(p)
		return r.enterAt
	}
	return time.Time{}
}

// toggleReview opens the review view on the active tab, or closes it when
// it shows that tab.
func (u *ui) toggleReview(st *model.State) {
	if u.review.on && u.review.ws == u.nav.workspace {
		u.review.hide()
		return
	}
	u.openReview(st, u.nav.workspace, "")
}

// openReview opens the review view on tab id, at file when it is not "".
// On a Host the worktree is on the host, so the diff opens in the pager
// there instead.
func (u *ui) openReview(st *model.State, id, file string) {
	ws := findWorkspace(st, id)
	if ws == nil {
		return
	}
	if Host != "" {
		if m := u.nav.review(st, id, "diff_pager"); m != nil {
			u.send(m)
		}
		return
	}
	if diff, _ := sidebar.ReviewBlocked(st, *ws, ghInstalled()); diff != "" {
		log.Printf("view_diff on tab %s: %q", id, diff)
		return
	}
	u.settings.Hide()
	invalidate := u.invalidate
	if invalidate == nil {
		invalidate = func() {}
	}
	u.review.show(id, st.LivePath(*ws), invalidate)
	u.review.selPath = file
}

// reviewed reports whether f is marked reviewed in root at its current
// change.
func (u *ui) reviewed(root string, f *review.File) bool {
	return f.Hash != "" && u.gui.Reviewed[root][f.Path] == f.Hash
}

// markReviewed marks f reviewed in root, or unmarks it, and saves that.
// Only the files still in the diff keep their marks, so the state never
// outgrows a worktree's changes.
func (u *ui) markReviewed(root string, files []review.File, f *review.File, on bool) {
	if u.gui.Reviewed == nil {
		u.gui.Reviewed = map[string]map[string]string{}
	}
	marks := map[string]string{}
	for _, g := range files {
		if h := u.gui.Reviewed[root][g.Path]; h == g.Hash && g.Path != f.Path {
			marks[g.Path] = h
		}
	}
	if on {
		marks[f.Path] = f.Hash
	}
	if len(marks) == 0 {
		delete(u.gui.Reviewed, root)
	} else {
		u.gui.Reviewed[root] = marks
	}
	if u.report != nil { // a real window, not a test
		saveGUIState(u.gui)
	}
}

// discardFile runs the discard dialog's confirm: the file goes back to
// the merge base, or an untracked one is deleted, then the diff is read
// again.
func (u *ui) discardFile() {
	r := &u.review
	f := r.discarding
	paths := []string{f.Path}
	if f.OldPath != "" {
		paths = append(paths, f.OldPath)
	}
	dir, invalidate := r.dir, u.invalidate
	if invalidate == nil {
		invalidate = func() {}
	}
	r.status = "Discarded " + f.Path
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), filesWait)
		defer cancel()
		err := discardFiles(ctx, dir, paths, f.Status == '?')
		if err != nil {
			log.Printf("review: discard %s: %q", f.Path, err)
		}
		r.mu.Lock()
		if err != nil {
			r.err = "Discard failed: " + err.Error()
		}
		r.mu.Unlock()
		r.reload()
		invalidate()
	}()
}

// reviewKeys takes the view's keys before the window's shortcuts: Escape
// closes the comment field or the view, j/k pick a file, n/p a hunk, the
// arrows a line (Shift extends) and c comments on it.
func (u *ui) reviewKeys(gtx gl.Context, st *model.State) {
	r := &u.review
	if !r.on || u.nav.switcherVisible() || u.modal.kind != modalNone {
		return
	}
	files, _, _, _, _ := r.snapshot()
	r.follow(files)
	f := r.file(files)
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &r.edit, Name: key.NameEscape})
		if !ok {
			break
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			r.editing, r.focus = false, true
		}
	}
	// why: Gio gives key focus only to a tag with a FocusFilter.
	filters := []event.Filter{key.FocusFilter{Target: &r.tag}, key.Filter{Name: key.NameEscape}}
	for _, k := range []key.Name{"J", "K", "N", "P", "C"} {
		filters = append(filters, key.Filter{Focus: &r.tag, Name: k})
	}
	filters = append(filters,
		key.Filter{Focus: &r.tag, Name: key.NameUpArrow, Optional: key.ModShift},
		key.Filter{Focus: &r.tag, Name: key.NameDownArrow, Optional: key.ModShift})
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		var rows []review.Row
		if f != nil {
			rows = r.rowsOf(f)
		}
		switch e.Name {
		case key.NameEscape:
			if r.editing {
				r.editing, r.focus = false, true
			} else {
				r.hide()
				return
			}
		case "J", "K":
			d := 1
			if e.Name == "K" {
				d = -1
			}
			r.selectFile(files, r.sel+d)
			f = r.file(files)
		case "N", "P":
			if f == nil {
				break
			}
			d := 1
			if e.Name == "P" {
				d = -1
			}
			if !r.hunk(f, rows, d) && d > 0 && r.sel+1 < len(files) {
				r.selectFile(files, r.sel+1)
				f = r.file(files)
				r.hunk(f, r.rowsOf(f), 1)
			}
		case key.NameUpArrow, key.NameDownArrow:
			d := 1
			if e.Name == key.NameUpArrow {
				d = -1
			}
			r.moveLine(rows, d, e.Modifiers.Contain(key.ModShift))
		case "C":
			r.startComment()
		}
	}
}

// layoutReview draws the view, or the panes again once another tab is
// selected.
func (u *ui) layoutReview(gtx gl.Context, st *model.State) {
	if u.nav.workspace != u.review.ws {
		u.review.hide()
		u.layoutPanes(gtx, st)
		return
	}
	u.drawReview(gtx, st)
	if r := &u.review; r.focus {
		r.focus = false
		if r.editing {
			gtx.Execute(key.FocusCmd{Tag: &r.edit})
		} else {
			// why: a key that opened the view came after reviewKeys polled
			// this frame, and Gio focuses only a tag polled for focus.
			for {
				if _, ok := gtx.Event(key.FocusFilter{Target: &r.tag}); !ok {
					break
				}
			}
			gtx.Execute(key.FocusCmd{Tag: &r.tag})
		}
	}
}

// tickReview runs the review's deliveries each frame, open or not.
func (u *ui) tickReview(gtx gl.Context, st *model.State) {
	if at := u.deliver(st, gtx.Now); !at.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: at})
	}
}
