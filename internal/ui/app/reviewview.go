package app

import (
	"bytes"
	"context"
	"fmt"
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

// readDiff, discardFiles, the turn and the staging functions are what the
// review view runs git through; tests replace them.
var (
	readDiff     = gitstat.Diff
	discardFiles = gitstat.Discard
	readStaging  = gitstat.ReadStaging
	applyCached  = gitstat.ApplyCached
	stageFiles   = gitstat.StageFiles
	readTurn     = gitstat.LatestTurn
	readTurnDiff = gitstat.TurnDiff
	undoTurn     = gitstat.UndoTurn
	redoTurn     = gitstat.RedoTurn
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
	// to, seq and at are the pane, proto.Deliver.Seq and time of the daemon's
	// taking it, while it is queued there.
	to  string
	seq uint64
	at  time.Time
	del widget.Clickable
}

// delivery is comments typed into pane once its agent waits for input.
// send is whether it carries a Send failures or Send review comments
// message, of which a pane waits for one at most.
type delivery struct {
	pane  string
	text  string
	notes []*note
	send  bool
	// images are paths pasted before text, as a pasted image is: a
	// reply's attached images.
	images []string
}

// reviewView is the review view: one tab's changes from the merge base
// with its base, the default branch unless the user picked another,
// committed or not, shown in place of its panes
// like the settings page. A goroutine reads the diff, the UI goroutine
// never waits on git.
type reviewView struct {
	on  bool
	ws  string // the tab shown; another tab closes the view
	dir string // the tab's live directory

	readers sync.WaitGroup // tests wait for canceled readers before restoring git hooks
	mu      sync.Mutex
	gen     int
	stop    context.CancelFunc
	kick    chan struct{} // reads the diff again now
	loaded  bool
	err     string
	base    string
	root    string
	files   []review.File
	// pick is the base the user picked for the Branch scope, "" for the
	// default branch; lost is a pick whose branch a read found gone, until
	// the window forgets it.
	pick, lost string
	// turnScope asks for This turn, the tab's latest checkpoint against
	// its files, over Branch; inTurn says files are that. turn is the
	// latest checkpoint, a zero one for none.
	turnScope, inTurn bool
	turn              gitstat.Turn
	// stageScope asks for Staging, the uncommitted change split into
	// unstaged and staged files, over the other two; inStage says files
	// are that.
	stageScope, inStage bool
	failed              string           // the last undo, redo or staging's error, which a read does not clear
	busy                bool             // an undo, redo or staging runs
	agentState          model.AgentState // the tab's agent's, which rereads This turn when it changes

	sel         int    // the selected file
	selPath     string // its File.Key, which keeps the selection across reads
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
	hunkClick          map[int]*widget.Clickable
	tag                int // takes the view's keys
	focus              bool

	edit             widget.Editor
	editing          bool
	editFrom, editTo int

	send, pr, pager, opinion, mark, discard, showHuge widget.Clickable
	branchScope, thisTurn, undo, redo                 widget.Clickable
	staging, stageFile                                widget.Clickable
	baseBtn                                           widget.Clickable // the base in the header: opens bases
	bases                                             basePicker
	prChip                                            widget.Clickable // the PR status in the header: opens the pull request
	status                                            string           // the line under the header: sent, queued, a failure

	notes      map[string][]*note // by repository root
	queue      []delivery
	waiting    map[string]int // per pane, the messages the daemon has yet to type
	seq        uint64         // the last proto.Deliver.Seq
	enterPane  string         // the pane whose paste waits for its Enter
	enterAt    time.Time
	discarding review.File // the file the discard dialog is about
	// discardBase is the base the view showed discarding against, which the
	// dialog names and Discard restores from.
	discardBase string
	undoFiles   int // the file count the undo dialog names
}

// show opens the view on tab ws, reading dir from base, "" for the
// default branch, on This turn when turn is set and the tab has a
// checkpoint.
func (r *reviewView) show(ws, dir, base string, turn bool, invalidate func()) {
	r.hide()
	r.on, r.ws, r.dir, r.focus = true, ws, dir, true
	r.sel, r.selPath, r.cur, r.anchor, r.scrollTo, r.rowsDirty, r.editing, r.status = 0, "", -1, -1, -1, true, false, ""
	r.fileList.Axis, r.diffList.Axis = gl.Vertical, gl.Vertical
	r.fileList.Position, r.diffList.Position = gl.Position{}, gl.Position{}
	r.mu.Lock()
	r.gen++
	r.loaded, r.err, r.files = false, "", nil
	r.turnScope, r.inTurn, r.turn = turn, false, gitstat.Turn{}
	r.stageScope, r.inStage, r.pick = false, false, base
	ctx, cancel := context.WithCancel(context.Background())
	r.stop, r.kick = cancel, make(chan struct{}, 1)
	gen, kick := r.gen, r.kick
	r.mu.Unlock()
	r.readers.Add(1)
	go r.load(ctx, gen, ws, dir, kick, invalidate)
}

// setScope shows Staging when stage is set, else This turn when turn is,
// else Branch, from a fresh read.
func (r *reviewView) setScope(turn, stage bool) {
	r.mu.Lock()
	if r.turnScope != turn || r.stageScope != stage {
		r.turnScope, r.stageScope, r.loaded, r.files = turn, stage, false, nil
	}
	r.mu.Unlock()
	r.reload()
}

// setBase diffs the Branch scope from base, "" for the default branch,
// from a fresh read.
func (r *reviewView) setBase(base string) {
	r.mu.Lock()
	if r.pick != base {
		r.pick, r.loaded, r.files = base, false, nil
	}
	r.mu.Unlock()
	r.reload()
}

// takeLost is a pick whose branch is gone, once.
func (r *reviewView) takeLost() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lost := r.lost
	r.lost = ""
	return lost
}

// picked is the base the user picked, "" for the default branch.
func (r *reviewView) picked() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pick
}

// turnState is the tab's latest checkpoint, whether the files shown are
// its turn's, and the last undo or redo's error.
func (r *reviewView) turnState() (turn gitstat.Turn, in bool, failed string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.turn, r.inTurn, r.failed
}

// staged reports whether the files shown are Staging's.
func (r *reviewView) staged() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inStage
}

// restoring reports whether an undo, redo or staging runs.
func (r *reviewView) restoring() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.busy
}

// markRoot is what marks are kept under: a turn's diff has hashes of its
// own, so its marks must not replace the branch's.
func (r *reviewView) markRoot(root string) string {
	if _, in, _ := r.turnState(); in {
		return root + "\x00turn"
	}
	return root
}

// hide closes the view and stops its reader. Comments and queued
// deliveries stay.
func (r *reviewView) hide() {
	r.on, r.editing = false, false
	r.bases.close()
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
// done, and invalidates when it changed: tab ws's latest turn's when This
// turn is asked for and there is one, else the branch's.
func (r *reviewView) load(ctx context.Context, gen int, ws, dir string, kick chan struct{}, invalidate func()) {
	defer r.readers.Done()
	t := time.NewTicker(filesEvery)
	defer t.Stop()
	var last []byte
	var parsed []review.File // last's files: an unchanged diff is not parsed again
	var p gitstat.Patch
	var files []review.File
	var errText string
	read := ""     // the checkpoint files are the turn diff against, "" for none
	kicked := true // the first read, or a kick
	for {
		c, cancel := context.WithTimeout(ctx, filesWait)
		r.mu.Lock()
		want, stage, pick := r.turnScope, r.stageScope, r.pick
		r.mu.Unlock()
		if pick != "" && !stage && branchRef(c, dir, pick) == "" { // the picked branch is gone
			r.mu.Lock()
			// why: a read of a closed or reopened view must not touch the
			// current one's pick; the files go too, so nothing such as
			// Discard acts on the old base's diff until the default's loads.
			if r.gen == gen && ctx.Err() == nil && r.pick == pick {
				r.pick, r.lost, r.loaded, r.files = "", pick, false, nil
			}
			r.mu.Unlock()
			pick = ""
		}
		turn, err := readTurn(c, dir, ws)
		if err != nil {
			turn = gitstat.Turn{} // no checkpoint to show
		}
		in := want && turn.Rev != "" && !stage
		if stage {
			read, parsed = "", nil
			p, files, errText = stagedFiles(c, dir)
		} else if !in || kicked || turn.Rev != read {
			// why: a turn's diff snapshots the files again, so a tick
			// reads it only for a new turn; a kick, such as the agent
			// changing state, reads it always.
			read = ""
			if in {
				p, err = readTurnDiff(c, dir, turn.Rev)
			} else {
				p, err = readDiff(c, dir, pick)
			}
			files, errText = nil, ""
			if err != nil {
				errText = err.Error()
			} else {
				if in {
					read = turn.Rev
				}
				if parsed == nil || !bytes.Equal(last, p.Diff) {
					parsed, last = review.Parse(p.Diff), p.Diff
				}
				files = slices.Clip(parsed)
				for _, path := range p.Untracked {
					b, ok := gitstat.ReadUntracked(p.Root, path)
					files = append(files, review.Untracked(path, b, !ok))
				}
			}
		}
		cancel()
		if ctx.Err() != nil {
			return
		}
		r.update(gen, p, files, errText, turn, in, stage, pick, invalidate)
		kicked = false
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
			kicked = true
		}
	}
}

func (r *reviewView) update(gen int, p gitstat.Patch, files []review.File, err string, turn gitstat.Turn, in, stage bool, pick string, invalidate func()) {
	r.mu.Lock()
	// A read of the scope or base asked for before a switch is dropped.
	current := r.gen == gen && stage == r.stageScope && in == (r.turnScope && turn.Rev != "" && !stage) && pick == r.pick
	changed := current && (!r.loaded || r.err != err || r.base != p.Base || r.turn != turn || r.inTurn != in || r.inStage != stage || !sameFiles(r.files, files))
	if changed {
		r.loaded, r.err, r.base, r.root, r.files, r.turn, r.inTurn, r.inStage = true, err, p.Base, p.Root, files, turn, in, stage
	}
	r.mu.Unlock()
	if changed {
		invalidate()
	}
}

func sameFiles(a, b []review.File) bool {
	return slices.EqualFunc(a, b, func(x, y review.File) bool { return x.Key() == y.Key() && x.Hash == y.Hash })
}

// stagedFiles reads dir's Staging as the Staging scope lists it: the
// unmerged files, the unstaged ones, the untracked ones, then the staged
// ones.
func stagedFiles(ctx context.Context, dir string) (gitstat.Patch, []review.File, string) {
	s, err := readStaging(ctx, dir)
	if err != nil {
		return gitstat.Patch{}, nil, err.Error()
	}
	var files []review.File
	for _, path := range s.Unmerged {
		files = append(files, review.File{Path: path, Status: 'U'})
	}
	files = append(files, review.Parse(s.Unstaged)...)
	for _, path := range s.Untracked {
		b, ok := gitstat.ReadUntracked(s.Root, path)
		files = append(files, review.Untracked(path, b, !ok))
	}
	for _, f := range review.Parse(s.Staged) {
		f.Staged = true
		files = append(files, f)
	}
	return gitstat.Patch{Root: s.Root}, files, ""
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
	if files[i].Key() != r.selPath {
		r.cur, r.anchor, r.editing = -1, -1, false
		r.diffList.Position = gl.Position{}
	}
	r.sel, r.selPath, r.rowsDirty = i, files[i].Key(), true
}

// follow keeps the selection on its path in a fresh read.
func (r *reviewView) follow(files []review.File) {
	if i := slices.IndexFunc(files, func(f review.File) bool { return f.Key() == r.selPath }); i >= 0 {
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
	if key := f.Key() + "\x00" + f.Hash; !r.rowsDirty && r.rows != nil && key == r.rowsKey {
		return r.rows
	}
	r.rowsDirty, r.rowsKey = false, f.Key()+"\x00"+f.Hash
	if f.Binary || r.huge(f) {
		r.rows = []review.Row{}
		return r.rows
	}
	open := r.opened[f.Key()]
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

// deliverAck is how long the daemon may take to show a delivery it was given
// in State.Queued; past it, a queue without it means the delivery is gone.
const deliverAck = 5 * time.Second

// settle moves the comments the daemon took on as State says: Sent once typed,
// pending again when its pane closed or its queue no longer has them, as after
// a daemon restart. It returns when it next needs a frame, zero for no need.
func (r *reviewView) settle(st *model.State, now time.Time) time.Time {
	var next time.Time
	for _, notes := range r.notes {
		for _, n := range notes {
			if n.state != noteQueued || n.to == "" {
				continue
			}
			p := findPane(st, n.to)
			switch {
			case st.Delivered[n.to] >= n.seq:
				n.state, n.to = noteSent, ""
			case p == nil || p.Exited:
				n.state, n.to = notePending, ""
				r.status = "The agent's pane closed; the comments were not sent"
			case r.waiting[n.to] > 0:
			case !now.Before(n.at.Add(deliverAck)):
				n.state, n.to = notePending, ""
				r.status = "The comments were not sent; send them again"
			case next.IsZero() || n.at.Add(deliverAck).Before(next):
				next = n.at.Add(deliverAck)
			}
		}
	}
	return next
}

// sentNote is what the window says of a delivery it gave p's ready agent: the
// daemon holds it while the user's draft sits in the input box or a form shows.
func sentNote(st *model.State, p *model.Pane) string {
	if st.Drafting[p.ID] {
		return "Goes to " + agentName(p) + " once its input box is empty"
	}
	if slices.ContainsFunc(st.Activities, func(a model.Activity) bool { return a.PaneID == p.ID && a.Menu }) {
		return "Goes to " + agentName(p) + " once its menu is answered"
	}
	return "Sent to " + agentName(p)
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
	r.enqueue(p.ID, review.Prompt(cs), notes)
	r.status = "Queued until " + agentName(p) + " waits for input"
	u.deliver(st, now)
}

// enqueue adds text, carrying notes, to pane's delivery, or starts one,
// and returns the delivery.
func (r *reviewView) enqueue(pane, text string, notes []*note) *delivery {
	i := slices.IndexFunc(r.queue, func(d delivery) bool { return d.pane == pane })
	if i < 0 {
		r.queue = append(r.queue, delivery{pane: pane})
		i = len(r.queue) - 1
	} else {
		text = "\n\n" + text
	}
	r.queue[i].text += text
	r.queue[i].notes = append(r.queue[i].notes, notes...)
	return &r.queue[i]
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

// deliver gives the first delivery whose agent is ready to the daemon, which
// types it past the user's draft. A daemon too old for that gets it typed
// here, as a bracketed paste and Enter replyDelay later, the way the phone's
// replies go in. A delivery whose pane is gone is dropped and its
// comments are pending again. It returns when it next needs a frame, zero
// for no need.
func (u *ui) deliver(st *model.State, now time.Time) time.Time {
	r := &u.review
	r.waiting = st.Queued
	next := r.settle(st, now)
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
		if u.link().Level >= proto.Since(proto.Deliver{}) {
			r.seq = max(r.seq+1, uint64(now.UnixNano()))
			if !u.send(proto.Deliver{Pane: d.pane, Text: d.text, Images: d.images, Seq: r.seq}) {
				return time.Time{}
			}
			log.Printf("review: gave %d comments, %d bytes, to the daemon for pane %s", len(d.notes), len(d.text), d.pane)
			for _, n := range d.notes {
				n.to, n.seq, n.at = d.pane, r.seq, now
			}
			r.queue = slices.Delete(r.queue, i, i+1)
			r.status = sentNote(st, p)
			return now
		}
		_, m, _ := u.b.Frame(d.pane)
		text := remote.Clean(d.text)
		if !m.BracketedPaste {
			text = strings.ReplaceAll(text, "\n", " ") // a bare newline would send the first line alone
		}
		for _, img := range d.images {
			if !u.send(proto.Input{Pane: d.pane, Data: input.PastePaths([]string{img}, m)}) {
				return time.Time{}
			}
		}
		if text != "" && !u.send(proto.Input{Pane: d.pane, Data: input.Paste(text, m)}) {
			return time.Time{}
		}
		log.Printf("review: sent %d comments, %d bytes, to pane %s", len(d.notes), len(text), d.pane)
		for _, n := range d.notes {
			n.state = noteSent
		}
		r.queue = slices.Delete(r.queue, i, i+1)
		r.enterPane, r.enterAt = d.pane, now.Add(replyDelay)
		r.status = "Sent to " + agentName(p)
		return r.enterAt
	}
	return next
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
	if curHost() != "" {
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
	u.reader.Close()
	invalidate := u.invalidate
	if invalidate == nil {
		invalidate = func() {}
	}
	// A finished turn opens on what it changed.
	as := agentStateOf(st, ws)
	done := as == model.StateCompleted || as == model.StatePROpen
	u.review.show(id, st.LivePath(*ws), u.nav.pickOf(st, id), done, invalidate)
	u.review.selPath, u.review.agentState = file, agentStateOf(st, ws)
}

// agentStateOf is the state of tab ws's agent, "" for none.
func agentStateOf(st *model.State, ws *model.Workspace) model.AgentState {
	if p := agentPane(st, ws); p != nil {
		if i := slices.IndexFunc(st.Activities, func(a model.Activity) bool { return a.PaneID == p.ID }); i >= 0 {
			return st.Activities[i].State
		}
	}
	return ""
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

// askDiscard aims the discard dialog at f, against the base the view
// shows f's diff from.
func (r *reviewView) askDiscard(f *review.File) {
	_, base, _, _, _ := r.snapshot()
	r.discarding, r.discardBase = *f, base
}

// discardFile runs the discard dialog's confirm: the file goes back to
// the merge base with the base its diff was shown from, or an untracked
// one is deleted, then the diff is read again.
func (u *ui) discardFile() {
	r := &u.review
	f := r.discarding
	paths := []string{f.Path}
	if f.OldPath != "" {
		paths = append(paths, f.OldPath)
	}
	dir, base, invalidate := r.dir, r.discardBase, u.invalidate
	if invalidate == nil {
		invalidate = func() {}
	}
	r.status = "Discarded " + f.Path
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), filesWait)
		defer cancel()
		err := discardFiles(ctx, dir, base, paths, f.Status == '?')
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

// undoTurnFiles runs the undo dialog's confirm: the tab's files go back to
// its latest checkpoint, then the diff is read again. With redo it puts
// the undone turn back instead, which needs no dialog: Redo shows only
// while nothing changed since the undo.
func (u *ui) undoTurnFiles(redo bool) {
	r := &u.review
	ws, dir, invalidate := r.ws, r.dir, u.invalidate
	if invalidate == nil {
		invalidate = func() {}
	}
	run, name, done := undoTurn, "Undo", "Undid the turn"
	if redo {
		run, name, done = redoTurn, "Redo", "Redid the turn"
	}
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return
	}
	r.busy, r.failed = true, ""
	r.mu.Unlock()
	r.status = done
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), filesWait)
		defer cancel()
		err := run(ctx, dir, ws)
		if err != nil {
			log.Printf("review: %s on tab %s: %q", strings.ToLower(name), ws, err)
		}
		r.mu.Lock()
		r.busy = false
		if err != nil {
			r.failed = name + " failed: " + err.Error()
		}
		r.mu.Unlock()
		r.reload()
		invalidate()
	}()
}

// stageHunk stages the selected file's hunk the selected line is in, or
// the one review.HunkAt picks, in the Staging scope; with unstage it
// unstages it, and with whole it does all of the file. A file whose hunks
// git cannot apply alone, such as an unmerged, untracked, added, deleted,
// renamed, binary or huge one, goes whole. The files are read again before the
// next one can run, so a quick second press stages the next hunk.
func (u *ui) stageHunk(f *review.File, unstage, whole bool) {
	r := &u.review
	if f == nil || !r.staged() || f.Staged != unstage {
		return
	}
	h, ok := review.HunkAt(review.Hunks(f.Lines, reviewCtx), r.cur)
	whole = whole || !ok || f.Status != 'M' || f.Binary || r.huge(f)
	verb := "Stage"
	if unstage {
		verb = "Unstage"
	}
	dir, invalidate := r.dir, u.invalidate
	if invalidate == nil {
		invalidate = func() {}
	}
	run := func(ctx context.Context) error {
		paths := []string{f.Path}
		if f.OldPath != "" {
			paths = append(paths, f.OldPath)
		}
		return stageFiles(ctx, dir, paths, unstage)
	}
	status := verb + "d " + f.Path
	if !whole {
		patch := review.Patch(f, h)
		run = func(ctx context.Context) error { return applyCached(ctx, dir, patch, unstage) }
		status = fmt.Sprintf("%sd a hunk of %s", verb, f.Path)
	}
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		return
	}
	r.busy, r.failed = true, ""
	gen, turn, pick := r.gen, r.turn, r.pick
	r.mu.Unlock()
	r.status = status
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), filesWait)
		defer cancel()
		err := run(ctx)
		if err != nil {
			log.Printf("review: %s %s: %q", strings.ToLower(verb), f.Path, err)
		}
		p, files, errText := stagedFiles(ctx, dir)
		r.update(gen, p, files, errText, turn, false, true, pick, invalidate)
		r.mu.Lock()
		r.busy = false
		if err != nil {
			r.failed = verb + " failed: " + err.Error()
		}
		r.mu.Unlock()
		invalidate()
	}()
}

// reviewKeys takes the view's keys before the window's shortcuts, the
// base list's while it is open: Escape closes the comment field or the
// view, j/k pick a file, n/p a hunk, the arrows a line (Shift extends), c
// comments on it, b opens the base list, and in Staging s and u stage and
// unstage its hunk, with Shift the whole file.
func (u *ui) reviewKeys(gtx gl.Context, st *model.State) {
	r := &u.review
	if !r.on || u.nav.switcherVisible() || u.modal.kind != modalNone || u.baseKeys(gtx, st) {
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
	for _, k := range []key.Name{"J", "K", "N", "P", "C", "B"} {
		filters = append(filters, key.Filter{Focus: &r.tag, Name: k})
	}
	filters = append(filters,
		key.Filter{Focus: &r.tag, Name: "S", Optional: key.ModShift},
		key.Filter{Focus: &r.tag, Name: "U", Optional: key.ModShift},
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
		case "B":
			u.openBases(st)
		case "S", "U":
			u.stageHunk(f, e.Name == "U", e.Modifiers.Contain(key.ModShift))
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
	if s := agentStateOf(st, findWorkspace(st, u.review.ws)); s != u.review.agentState {
		u.review.agentState = s
		u.review.reload()
	}
	u.dropLost()
	u.drawReview(gtx, st)
	u.drawBases(gtx, st)
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
