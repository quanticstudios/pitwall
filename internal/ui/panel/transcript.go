package panel

import (
	"image"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	gioevent "gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Transcript draws a chat as a desktop chat app does, in a centered
// column: prompts as bubbles on the right, each turn's tool calls in one
// work log that folds to a line, replies as markdown with copy buttons,
// and approvals and questions as cards where the chat stopped, answered
// by their buttons or the number keys. Only the rows in view are laid
// out, so a 2,000-item chat costs a frame what a short one does. One
// Transcript holds one chat's folds, scroll and picks: keep one per chat.
type Transcript struct {
	// Width is the column's widest, [chat] width; 0 uses the whole area.
	Width unit.Dp
	// Dir is the chat's folder: tool rows and requests show the paths
	// under it relative to it.
	Dir string
	// Copy puts text on the clipboard: the window's clipboard path. Nil
	// writes it directly.
	Copy func(gtx layout.Context, text string)
	// Older, when set, reads the page of history before the oldest item
	// once the reader nears it, and reports whether there is one.
	Older func() bool
	// Full, when set, reads an item that came cut (session.Item.Cut)
	// whole, once it is laid out.
	Full func(item string)
	// Find, when set, asks the daemon for a query's matches in the whole
	// chat, which come back through Found; nil finds in the items the
	// window holds, reading the history back to its start.
	Find func(query string)
	// Open opens a link: one clicked in a reply, a URL or a file URL for a
	// path under Dir, or a tool row's, a URL or a file's absolute path. Nil
	// leaves links inert.
	Open func(link string)
	// Run, when set, takes the command of a shell code block's Run: it
	// types it where the user presses Enter to run it, never runs it.
	Run func(cmd string)
	// PRs are the pull requests pitwall tracks, as State.PRs: a tool row
	// whose output names one shows its live state.
	PRs map[string]model.PR
	// CanRewind and CanFork show the prompts' Rewind and Fork buttons,
	// for a chat whose daemon and agent take them.
	CanRewind, CanFork bool
	// TurnRewind says the chat rewinds and forks whole turns: they show only
	// on a prompt that opened its turn.
	TurnRewind bool
	// FilesRewind says the daemon checks and reverts the files on a rewind
	// (proto.RewindFilesLevel): the confirm lists them and offers Revert
	// files too.
	FilesRewind bool
	// Invalidate asks for a frame from another goroutine: the side panel's
	// markdown file and its pictures, read in the background.
	Invalidate func()
	// CopyOnSelect copies a mouse selection once the button comes up, as
	// a terminal pane's [term] copy_on_select does.
	CopyOnSelect bool
	// Servers is the side panel's Servers view: what the chat's tab runs
	// that listens on a port.
	Servers ServerList
	// Copied, when set, tells the window a selection was copied, to show
	// its notice over the transcript, area in the window.
	Copied func(gtx layout.Context, text string, area image.Rectangle)
	// ToolCalls is which tool calls show, [chat] tool_calls: ToolsAll
	// (also ""), ToolsFailed, ToolsNone or ToolsZen.
	ToolCalls string
	// ChatLayout is the transcript's layout, [chat] layout: LayoutColumn
	// (also "") or LayoutLedger.
	ChatLayout string
	// CodeScale is code's size over the chat's text size, [font]
	// chat_code_size over chat_size: code blocks, diffs, commands and tool
	// output. 0 is 1.
	CodeScale float32
	// Limit is the usage limit notice's resume, set by the window each
	// frame; nil shows the notice without its buttons.
	Limit *LimitResume
	// LiveSteps says the daemon accepts step-by-step TUI question actions
	// (proto.QuestionStepLevel).
	LiveSteps bool
	// Away is the chat's Since you left card (State.Away), set by the
	// window each frame, nil for none; AwayNow is the agent's state as the
	// card names it (model.AwayNow).
	Away    *model.Away
	AwayNow string

	list       layout.List
	listPx     float32 // the px per sp list's offsets were laid out at
	prevLayout string  // the last frame's resolved ChatLayout, for keeping scroll on switch
	blocks     []block
	blocksKey  blocksKey
	blocksSet  bool
	hasTurns   bool   // any item in s.Content carries a TurnID
	lastPrmpt  string // cached lastPrompt(s) for blocksKey
	baseLen    int    // prefix of rows that is cached across frames
	rows       []trow
	rowsKey    rowsKey
	rowsSet    bool
	rowsGen    uint64
	visFolds   []visFold
	prevFolds  []visFold
	drawnFolds map[string]visFold
	laid       int // transcript items laid out in the last frame
	live       bool
	wakeAt     time.Time
	epoch      time.Time
	now        time.Time
	hrefs      map[string]string // memoized os.Stat -> FileURL (or "") by abs path

	open   map[string]bool // folds the user opened or closed, by id
	fold   []int           // the items of the fold buildRows lays out
	clicks map[string]*widget.Clickable
	sels   map[string]*widget.Selectable
	docs   map[string]*reply    // parsed replies by item id
	cards  map[string]cardCache // command outputs' cards by item id
	copied map[string]time.Time // copy buttons, while they show Copied
	thumbs thumbs
	focus  int // the transcript's own key focus tag
	drawn  []*widget.Selectable
	// drawnWas is the frame before's drawn, kept for its array
	drawnWas []*widget.Selectable
	span     textSel     // the selection, see selection.go
	size     image.Point // the transcript's, in the last frame

	answer       map[string]*[3]widget.Clickable // per pending approval, one per decisions entry
	sent         map[string]bool                 // requests answered here that the chat has not resolved yet
	answers      []Answer                        // not taken by Answers yet
	reasons      map[string]*widget.Editor       // approvals whose Deny or Refine opened the reason field
	inputs       []InputAnswer                   // not taken by Inputs yet
	asks         map[string]*ask                 // per pending question request
	done         []resolved                      // requests answered here, as one line each
	keyReq       string                          // the approval the number keys answer
	keyPlan      bool                            // keyReq is a plan: 1 approves, 2 refines
	keyNoSession bool                            // keyReq's prompt offers no Allow for this session: 2 denies
	keyAsk       string                          // else the question they pick in
	typeIn       *widget.Editor                  // an Other field a key picked, to focus in the next Layout
	askQs        map[string][]session.Question   // pending questions by request
	asked        map[string]*askCache            // answered question calls' lines by item id
	what         map[string]string               // pending approvals' commands, for their line
	edits        map[string]string               // pending approvals' diffs, for their open line
	plans        map[string]bool                 // pending approvals that are plans
	items        int                             // len(Content) this frame
	last         string                          // Content's last item's id this frame
	diffs        map[string]diffCache            // edits' lines by item id
	tool         toolState                       // the tool rows' scroll, shells and images
	side         side                            // the Files and Changes panel
	changes      map[string]changeCache          // turns' changed files by their first item id

	more    widget.Clickable
	menu    bool
	pill    widget.Clickable // New output
	seen    string           // what the chat held when the reader was last at its end
	prompt  string           // the newest prompt's item id, to follow a new one
	aideBtn widget.Clickable
	aide    bool // Open in aide was clicked, until OpenAide takes it

	older  bool           // Older said there is history before the oldest item
	head   string         // the oldest item's id in the last frame
	anchor string         // the item of the first row in view in the last frame
	topH   int            // the height of the space above the rows when last laid out
	frame  int            // frames laid out
	selAt  map[string]int // the frame each selectable was last drawn in
	msg    msgState       // the prompts' hover buttons, the find bar and side questions
	cite   citeState      // the selection's toolbar and a quote's reveal, see cite.go
	mm     minimap        // the marks at the right edge
	away   away           // the Since you left card's jump and dismissal
}

// Answer is an approval answered with its buttons or keys.
type Answer struct {
	Request  string
	Decision string // allow, allow-session, deny
	Reason   string // what a denial tells the agent to do instead, "" for none
}

// InputAnswer is a question answered on its card: question id to the
// chosen labels or typed text, as proto.ChatAnswer carries. For a live
// TUI question on a daemon that supports step-by-step answering, Step
// and Target carry a single step ("answer", "goto", "submit").
type InputAnswer struct {
	Request string
	Answers map[string][]string
	Step    string
	Target  int
}

// decisions are what the approval buttons answer, in their order.
var decisions = [3]string{"allow", "allow-session", "deny"}

// planLine is how an answered plan's line says the decision.
var planLine = map[string]string{"allow": "Plan approved", "allow-session": "Plan approved", "deny": "Sent back to refine"}

// decisionLine is how an answered approval's line says the decision.
var decisionLine = map[string]string{"allow": "Allowed", "allow-session": "Allowed for this session", "deny": "Denied"}

// resolved is a request answered here: a line after the item that was
// last when it was answered.
type resolved struct {
	id, line, what string
	prose          bool        // what is words, not a command
	denied         bool        // a permission denied, not a plan sent back
	question       bool        // a question card's answers
	gone           bool        // a question line the transcript's own Answered line took over
	qs             []resolvedQ // the questions, for the open line
	cmd            string      // an approval's command or file, in full
	diff           string      // an allowed or denied edit's diff
	reason         string      // a denial's reason
	after          string      // the item before it when answered, "" for none
	at             int         // len(Content) when answered
	when           time.Time
}

// ask is a question card's state.
type ask struct {
	pick []int // per question, the option picked; len(options) is the typed answer, -1 none
	// multi is, per multiSelect question, each option checked, nil for a
	// single-select one; there pick is the option last toggled.
	multi [][]bool
	moved []bool // a multiSelect question Next/Enter moved on from
	// touched marks a question the user picked in or typed into; a
	// preselected recommended option alone leaves it untouched, so the card
	// never sends itself on a pick the user did not make.
	touched []bool
	// step is the step currently shown: 0..len(pick)-1 for a question, or
	// len(pick) for the final Review/Submit step when review is set.
	step int
	// active is the question the number keys act on when set explicitly;
	// -1 lets step govern.
	active int
	// tuiStep is the last step reported by a live TUI (in.Step), -1 before
	// any; hasTUI is whether the request is synced with a live TUI.
	tuiStep    int
	hasTUI     bool
	review     bool // card mirrors a final Review/Submit step
	done       []bool
	note       string              // error message from a refused live step
	lastTUIAns map[string][]string // last TUI-reported answers per question ID
	other      []widget.Editor
	opts       [][]widget.Clickable // per question, one per option and one for the typed row
	chips      []widget.Clickable   // step header chips: one per question, plus Submit when review
	revRows    []widget.Clickable   // clickable rows on the Review step
	back       widget.Clickable
	next       widget.Clickable
	submit     widget.Clickable
}

// trow is one row of the list: a prompt or system line, a piece of a
// turn, an answered request's line, or what follows the items.
type trow struct {
	b        block
	seg      rowSeg
	i        int       // segItem, segProse: the item
	top      bool      // segItem: a call on its own, not in a fold
	open     bool      // segHead: the fold is open; segTurn: the turn's work shows
	running  bool      // segHead, segReply, segTurn: the turn runs
	res      *resolved // open: its details show
	tail     layout.Widget
	gap      unit.Dp   // space above it
	rank     int       // its place in reading order, see rowRank
	gutter   bool      // Ledger: draw the time + agent gutter beside this row
	gutterAt time.Time // Ledger: the timestamp in the gutter
	divider  bool      // Ledger: draw a top hairline before this turn
	inTurn   bool      // Ledger: align row inside the agent's 768 dp text column
}

// rowSeg is which piece of its block a row draws.
type rowSeg uint8

const (
	segBlock       rowSeg = iota // a prompt or a system line
	segHead                      // a turn's work log header
	segItem                      // an item in an open work log
	segProse                     // an item written for the reader, between the folds
	segTurn                      // Worked for over a finished turn's work, Working for under a running one's
	segReply                     // a turn's reply
	segTodo                      // a turn's task list, its last task-list call i
	segChanges                   // a finished turn's change card
	segInterrupted               // a stopped turn's Interrupted line
)

func (r *trow) isTail() bool { return r.tail != nil || r.seg == segInterrupted }

type visFold struct {
	id  string
	def bool
	c   *widget.Clickable
}

type blocksKey struct {
	sessionID    string
	rev          int64
	contentLen   int
	firstID      string
	lastID       string
	lastUpdated  string
	lastTextLen  int
	lastToolOut  int
	lastToolStat string
	doneLen      int
	doneHash     uint64
}

type rowsKey struct {
	blocksKey
	sessionState  string
	running       bool
	turnsLen      int
	lastTurnState string
	lastTurnEnd   string
	toolMode      string
	openHash      uint64
	openLen       int
	dir           string
}

// Answers returns the approvals answered since the last call, once each.
func (t *Transcript) Answers() []Answer {
	a := t.answers
	t.answers = nil
	return a
}

// Inputs returns the questions answered since the last call, once each.
func (t *Transcript) Inputs() []InputAnswer {
	a := t.inputs
	t.inputs = nil
	return a
}

// OpenAide reports, once, that Open in aide was clicked.
func (t *Transcript) OpenAide() bool {
	o := t.aide
	t.aide = false
	return o
}

// Key answers the newest request by key name: 1, 2 or 3 an approval,
// a number picks a question's option, Left/Right step between questions,
// and Enter moves on or sends it. It reports whether the key did
// something. The window's composer calls it while it is empty; the
// transcript takes the keys itself when it has focus.
func (t *Transcript) Key(name string) bool {
	if t.keyPlan {
		if dec, ok := planKey(name); ok {
			if dec == "deny" {
				t.openReason(t.keyReq)
			} else {
				t.decide(t.keyReq, dec)
			}
			return true
		}
		return false
	}
	if t.keyReq == "" {
		if a := t.asks[t.keyAsk]; a != nil && !t.sent[t.keyAsk] && len(a.opts) > 0 {
			switch name {
			case string(key.NameLeftArrow):
				if a.numSteps() > 1 && a.step > 0 {
					t.goStep(t.keyAsk, a.step-1)
					return true
				}
				return false
			case string(key.NameRightArrow):
				if a.numSteps() > 1 && a.step < a.numSteps()-1 {
					t.goStep(t.keyAsk, a.step+1)
					return true
				}
				return false
			}
			if a.onReview() {
				if name == string(key.NameReturn) || name == string(key.NameEnter) || name == "1" {
					t.submitAsk(t.keyAsk)
					return true
				}
				return false
			}
		}
	}
	opts := 0
	if a := t.asks[t.keyAsk]; a != nil && len(a.opts) > 0 {
		opts = len(a.opts[t.askQuestion(a)]) - 1
	}
	act, ok := actionFor(name, t.keyReq != "", t.keyNoSession, opts)
	if !ok {
		return false
	}
	switch {
	case act.decision == "deny":
		t.openReason(t.keyReq)
	case act.decision != "":
		t.decide(t.keyReq, act.decision)
	case act.submit:
		t.advanceStep(t.keyAsk)
	default:
		a := t.asks[t.keyAsk]
		q := t.askQuestion(a)
		t.pick(t.keyAsk, q, act.option)
		if act.option == len(a.opts[q])-1 { // Other: type the answer
			t.typeIn = &a.other[q]
		}
	}
	return true
}

// pick chooses option o of question q on card id. A single-select pick
// advances to the next question at once, or sends on the last question
// when no review step is shown; Other and multiSelect picks wait for the
// typed text or Next/Enter.
func (t *Transcript) pick(id string, q, o int) {
	a := t.asks[id]
	if a == nil || t.sent[id] || q < 0 || q >= len(a.opts) { // a second click or key after the card went
		return
	}
	a.note = ""
	a.choose(q, o)
	if a.multiQ(q) != nil || o >= len(a.opts[q])-1 {
		return
	}
	qs := t.askQs[id]
	if a.hasTUI && t.LiveSteps && q < len(qs) && o < len(qs[q].Options) {
		vals := []string{qs[q].Options[o].Label}
		t.inputs = append(t.inputs, InputAnswer{Request: id, Step: "answer", Target: q, Answers: map[string][]string{qs[q].ID: vals}})
		if (len(qs) == 1 || q == len(qs)-1 && !a.waiting()) && !a.review {
			t.markSent(id, qs, a)
		} else {
			a.step = min(q+1, max(0, a.numSteps()-1))
		}
		return
	}
	if q == len(a.opts)-1 {
		switch {
		case a.review:
			a.step = len(a.opts)
		case !a.waiting():
			t.sendAsk(id)
		default:
			a.step = a.firstOpen()
		}
	}
}

// advanceStep confirms the current step of card id: sends it to a live
// TUI when LiveSteps is on, moves to the next question or Review step,
// or sends the whole card on the last question.
func (t *Transcript) advanceStep(id string) {
	a := t.asks[id]
	if a == nil || t.sent[id] || len(a.opts) == 0 {
		return
	}
	if a.onReview() {
		t.submitAsk(id)
		return
	}
	qs := t.askQs[id]
	q := a.current()
	if q < 0 || q >= len(qs) {
		return
	}
	a.note = ""
	vals, ok := a.answerFor(qs[q], q)
	if !ok {
		return
	}
	a.touch(q)
	if a.multiQ(q) != nil {
		a.moved[q] = true
	}
	a.active = -1
	if a.hasTUI && t.LiveSteps {
		t.inputs = append(t.inputs, InputAnswer{Request: id, Step: "answer", Target: q, Answers: map[string][]string{qs[q].ID: vals}})
		if (len(qs) == 1 || q == len(qs)-1 && !a.waiting()) && !a.review {
			t.markSent(id, qs, a)
		} else {
			a.step = min(q+1, max(0, a.numSteps()-1))
		}
		return
	}
	if q == len(qs)-1 {
		switch {
		case a.review:
			a.step = len(qs)
		case !a.waiting():
			t.sendAsk(id)
		default:
			a.step = a.firstOpen()
		}
		return
	}
	a.step = a.nextStep(q)
}

// goStep moves card id to step target, keeping earlier answers, and
// drives the live TUI to that step when LiveSteps is on.
func (t *Transcript) goStep(id string, target int) {
	a := t.asks[id]
	if a == nil || t.sent[id] {
		return
	}
	n := a.numSteps()
	if n == 0 {
		return
	}
	target = min(max(target, 0), n-1)
	if target == a.step {
		return
	}
	a.step = target
	a.active = -1
	a.note = ""
	if a.hasTUI && t.LiveSteps {
		t.inputs = append(t.inputs, InputAnswer{Request: id, Step: "goto", Target: target})
	}
}

// submitAsk confirms the Review step (or whole card) of card id.
func (t *Transcript) submitAsk(id string) {
	a := t.asks[id]
	if a == nil || t.sent[id] {
		return
	}
	qs := t.askQs[id]
	if a.hasTUI && t.LiveSteps {
		ans, _ := a.answers(qs)
		t.inputs = append(t.inputs, InputAnswer{Request: id, Step: "submit", Target: len(qs), Answers: ans})
		t.markSent(id, qs, a)
		return
	}
	t.sendAsk(id)
}

// Answer takes text typed in the composer as the answer to the current
// question: it fills that question's typed row and advances (or sends
// the card on the last question). It reports whether a question took
// the text.
func (t *Transcript) Answer(text string) bool {
	a := t.asks[t.keyAsk]
	if a == nil || t.sent[t.keyAsk] || strings.TrimSpace(text) == "" || len(a.opts) == 0 || a.onReview() {
		return false
	}
	q := a.current()
	a.other[q].SetText(text)
	if a.multiQ(q) == nil {
		a.pick[q] = len(a.opts[q]) - 1
	}
	t.advanceStep(t.keyAsk)
	return true
}

// numSteps is how many steps a shows: one per question, plus the Review
// step when review is set.
func (a *ask) numSteps() int {
	n := len(a.pick)
	if a.review && n > 0 {
		n++
	}
	return n
}

// onReview reports whether a is currently on its final Review step.
func (a *ask) onReview() bool { return a.review && len(a.pick) > 0 && a.step == len(a.pick) }

// touch marks question q as one the user answered themselves.
func (a *ask) touch(q int) {
	for len(a.touched) < len(a.pick) {
		a.touched = append(a.touched, false)
	}
	if q >= 0 && q < len(a.touched) {
		a.touched[q] = true
	}
}

// untouched is whether the user has not yet acted on question i.
func (a *ask) untouched(i int) bool { return i >= len(a.touched) || !a.touched[i] }

// waiting is whether a question on the card still waits on the user: open,
// or only preselected.
func (a *ask) waiting() bool {
	for i := range a.pick {
		if a.open(i) || a.untouched(i) {
			return true
		}
	}
	return false
}

// firstOpen is the first question that still needs an answer or
// confirmation, or the last question if all are answered.
func (a *ask) firstOpen() int {
	for i := range a.pick {
		if a.open(i) {
			return i
		}
	}
	for i := range a.pick {
		if a.untouched(i) {
			return i
		}
	}
	return max(0, len(a.pick)-1)
}

// nextStep is the step to move to after answering question q locally:
// the next unanswered/untouched question after q, else q+1.
func (a *ask) nextStep(q int) int {
	for i := q + 1; i < len(a.pick); i++ {
		if a.open(i) || a.untouched(i) {
			return i
		}
	}
	if q+1 < a.numSteps() {
		return q + 1
	}
	return q
}

// choose picks option o of question q: a multiSelect question toggles
// it and keeps the step, the typed row aside; a single-select pick
// advances to the next step.
func (a *ask) choose(q, o int) {
	a.pick[q], a.active = o, q
	a.step = q
	a.touch(q)
	if m := a.multiQ(q); m != nil {
		if o < len(m) {
			m[o] = !m[o]
		}
		return
	}
	if o < len(a.opts[q])-1 {
		a.active = -1
		if q+1 < len(a.pick) {
			a.step = a.nextStep(q)
		}
	}
}

// multiQ is question q's checks, nil unless it is multiSelect.
func (a *ask) multiQ(q int) []bool {
	if q >= 0 && q < len(a.multi) {
		return a.multi[q]
	}
	return nil
}

// askQuestion is the question the number keys pick in: the current step.
func (t *Transcript) askQuestion(a *ask) int { return a.current() }

// open is whether question i still waits on the user: no pick, or
// multiSelect and not moved on from.
func (a *ask) open(i int) bool {
	if i < len(a.done) && a.done[i] {
		return false
	}
	return a.pick[i] < 0 || a.multiQ(i) != nil && !a.moved[i]
}

// current is the question index the number keys act on: the visible step
// clamped to the questions slice.
func (a *ask) current() int {
	if len(a.pick) == 0 {
		return 0
	}
	if a.active >= 0 && a.active < len(a.pick) {
		return a.active
	}
	if a.step >= 0 && a.step < len(a.pick) {
		return a.step
	}
	return len(a.pick) - 1
}

// stepDone reports whether question i has a confirmed answer (either
// locally or confirmed by a live TUI).
func (a *ask) stepDone(qs []session.Question, i int) bool {
	if i < len(a.done) && a.done[i] {
		return true
	}
	if i < 0 || i >= len(qs) || a.untouched(i) {
		return false
	}
	if a.multiQ(i) != nil && !a.moved[i] {
		return false
	}
	_, ok := a.answerFor(qs[i], i)
	return ok
}

// answerFor returns the chosen answer(s) for question i, or false while
// it has none.
func (a *ask) answerFor(q session.Question, i int) ([]string, bool) {
	if i < 0 || i >= len(a.pick) {
		return nil, false
	}
	other := ""
	if i < len(a.other) {
		other = strings.TrimSpace(a.other[i].Text())
	}
	if m := a.multiQ(i); m != nil {
		var picked []string
		for oi, on := range m {
			if on && oi < len(q.Options) {
				picked = append(picked, q.Options[oi].Label)
			}
		}
		if other != "" {
			picked = append(picked, other)
		}
		if len(picked) == 0 {
			return nil, false
		}
		return picked, true
	}
	switch p := a.pick[i]; {
	case p >= 0 && p < len(q.Options):
		return []string{q.Options[p].Label}, true
	case p == len(q.Options) && other != "":
		return []string{other}, true
	default:
		return nil, false
	}
}

// init makes a new Transcript's maps. It sets fields one by one, so the
// thumbnails' lock, which a decode may hold, is never overwritten.
func (t *Transcript) init() {
	t.open, t.clicks, t.sels, t.selAt = map[string]bool{}, map[string]*widget.Clickable{}, map[string]*widget.Selectable{}, map[string]int{}
	t.docs, t.copied = map[string]*reply{}, map[string]time.Time{}
	t.answer, t.sent, t.asks = map[string]*[3]widget.Clickable{}, map[string]bool{}, map[string]*ask{}
	t.reasons = map[string]*widget.Editor{}
	t.list.Axis = layout.Vertical
	t.span.ord, t.span.size, t.span.typed, t.span.whole = map[string]int{}, map[*widget.Selectable]image.Point{}, map[string]ctext{}, map[*widget.Selectable]bool{}
}

func (t *Transcript) click(id string) *widget.Clickable {
	c := t.clicks[id]
	if c == nil {
		c = new(widget.Clickable)
		t.clicks[id] = c
	}
	return c
}

func (t *Transcript) sel(id string) *widget.Selectable {
	s := t.sels[id]
	if s == nil {
		s = new(widget.Selectable)
		t.sels[id] = s
	}
	t.selAt[id] = t.frame
	t.span.ord[id] = t.span.rank + t.span.seq
	t.span.seq++
	t.drawn = append(t.drawn, s)
	t.msg.keyOf(s, id)
	return s
}

// toggled flips fold id when its clickable was clicked, and reports
// whether it is open.
func (t *Transcript) toggled(gtx layout.Context, id string, def bool) bool {
	open := isOpen(t.open, id, def)
	c := t.clicks[id]
	for c != nil && c.Clicked(gtx) {
		open = !open
		t.open[id] = open
	}
	return open
}

// copy puts text on the clipboard, and key's button shows Copied.
func (t *Transcript) copy(gtx layout.Context, key, text string) {
	t.copied[key] = gtx.Now
	if t.Copy != nil {
		t.Copy(gtx, text)
		return
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(text))})
}

// copiedFor is how long a copy button shows its check.
const copiedFor = 1500 * time.Millisecond

func (t *Transcript) isCopied(key string) bool {
	at, ok := t.copied[key]
	if ok && t.now.Sub(at) < copiedFor {
		exp := at.Add(copiedFor)
		if t.wakeAt.IsZero() || exp.Before(t.wakeAt) {
			t.wakeAt = exp
		}
		return true
	}
	return false
}

// decide answers approval id once.
func (t *Transcript) decide(id, decision string) { t.decideWhy(id, decision, "") }

// decideWhy answers approval id once, a denial with reason.
func (t *Transcript) decideWhy(id, decision, reason string) {
	if id == "" || t.sent[id] {
		return
	}
	t.sent[id] = true
	t.answers = append(t.answers, Answer{Request: id, Decision: decision, Reason: reason})
	if t.keyReq == id {
		t.keyReq = ""
	}
	line := decisionLine[decision]
	if t.plans[id] {
		line = planLine[decision]
	}
	what, prose := t.what[id], t.plans[id]
	if reason != "" {
		what, prose = reason, true
	}
	t.done = append(t.done, resolved{id: id, line: line, what: what, prose: prose, denied: decision == "deny" && !t.plans[id], after: t.last, at: t.items, when: t.now,
		cmd: t.what[id], diff: t.edits[id], reason: reason})
}

// Refused takes back the answer to request id that the daemon refused:
// the line saying it was answered goes, and its card comes back while the
// request waits.
func (t *Transcript) Refused(id string) {
	delete(t.sent, id)
	t.done = slices.DeleteFunc(t.done, func(r resolved) bool { return r.id == id })
}

// StepError shows why the daemon refused a live TUI question step and
// restores the card to the step the TUI actually shows.
func (t *Transcript) StepError(id, msg string) {
	if t.sent[id] {
		t.Refused(id)
	}
	if a := t.asks[id]; a != nil {
		a.note = msg
		if a.hasTUI && a.tuiStep >= 0 {
			a.step = min(a.tuiStep, max(0, a.numSteps()-1))
		}
	}
}

// markSent marks question card id as sent and records its Answered line.
func (t *Transcript) markSent(id string, qs []session.Question, a *ask) {
	t.sent[id] = true
	if t.keyAsk == id {
		t.keyAsk = "" // a second Enter or number is the composer's again
	}
	ans, _ := a.answers(qs)
	var picked []string
	var rqs []resolvedQ
	for _, q := range qs { // in the card's order
		vals := ans[q.ID]
		if len(vals) == 0 && a.lastTUIAns != nil {
			vals = a.lastTUIAns[q.ID]
		}
		picked = append(picked, vals...)
		rqs = append(rqs, resolvedQ{header: q.Header, prompt: q.Prompt, options: q.Options, picked: vals})
	}
	t.done = append(t.done, resolved{id: id, line: "Answered", what: strings.Join(picked, ", "), prose: true, after: t.last, at: t.items, when: t.now,
		question: true, qs: rqs})
}

// sendAsk answers question request id with its card's picks, once each
// question has one.
func (t *Transcript) sendAsk(id string) {
	a := t.asks[id]
	if a == nil || t.sent[id] {
		return
	}
	qs := t.askQs[id]
	ans, ok := a.answers(qs)
	if !ok {
		return
	}
	t.inputs = append(t.inputs, InputAnswer{Request: id, Answers: ans})
	t.markSent(id, qs, a)
}

// answers are the card's picks by question id, false while a question
// has none.
func (a *ask) answers(qs []session.Question) (map[string][]string, bool) {
	out := map[string][]string{}
	for i, q := range qs {
		vals, ok := a.answerFor(q, i)
		if !ok {
			return nil, false
		}
		out[q.ID] = vals
	}
	return out, len(qs) > 0
}

// Layout draws s filling gtx's constraints, with Open in aide in a quiet
// menu when aide is set, and the Files and Changes panel at its right
// while it is open.
// keepScroll keeps the row at the top where it was when the chat's text
// size changes: the offset into it is in px, which scale with the size.
func (t *Transcript) keepScroll(px float32) {
	if t.listPx != 0 && px != t.listPx {
		t.list.Position.Offset = int(float32(t.list.Position.Offset)*px/t.listPx + 0.5)
		t.list.Position.OffsetLast = int(float32(t.list.Position.OffsetLast)*px/t.listPx + 0.5)
	}
	t.listPx = px
}

func (t *Transcript) Layout(gtx layout.Context, th *theme.Theme, s *session.Snapshot, aide bool) layout.Dimensions {
	return t.withSide(gtx, th, s, func(gtx layout.Context) layout.Dimensions { return t.layoutChat(gtx, th, s, aide) })
}

func (t *Transcript) layoutChat(gtx layout.Context, th *theme.Theme, s *session.Snapshot, aide bool) layout.Dimensions {
	if t.open == nil {
		t.init()
	}
	if t.epoch.IsZero() {
		t.epoch = gtx.Now
	}
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	prev := t.drawn
	t.now, t.live, t.wakeAt, t.laid, t.drawn, t.drawnWas, t.size = gtx.Now, false, time.Time{}, 0, t.drawnWas[:0], prev, size
	t.frame++
	t.settle(gtx, s)
	t.findUpdate(gtx, s)
	if t.aideBtn.Clicked(gtx) && aide {
		t.aide, t.menu = true, false
	}
	for t.more.Clicked(gtx) {
		t.menu = !t.menu
	}
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: size}.Op())

	sec := gtx.Now.Sub(t.epoch).Seconds()
	if anim.Reduced() {
		sec = 0
	}
	d := &cdraw{t: t, th: th, s: s, sec: sec}
	bk := t.makeBlocksKey(s)
	if !t.blocksSet || bk != t.blocksKey {
		t.blocks = blocks(s.Content, t.blocks, t.cuts(s)...)
		t.lastPrmpt = lastPrompt(s)
		t.hasTurns = false
		for i := range s.Content {
			if s.Content[i].TurnID != nil {
				t.hasTurns = true
				break
			}
		}
		t.blocksKey, t.blocksSet = bk, true
	}
	var col, x0, planH int
	if t.isLedger() {
		d.ledger = true
		d.geom = ledgerMetrics(gtx, s.Provider, size.X)
		d.col, d.proseW = d.geom.textW, d.geom.textW
		col, x0 = d.geom.textW, d.geom.textX
		planH = d.planStrip(gtx, size)
	} else {
		side := gtx.Dp(theme.SpaceXL)
		col = max(0, size.X-2*side)
		if t.Width > 0 {
			col = min(col, gtx.Dp(t.Width))
		}
		d.col, d.proseW = col, proseCap(col, d.avgChar(gtx))
		x0 = (size.X - col) / 2
	}

	listSize := image.Pt(size.X, max(0, size.Y-planH))
	t.size = listSize
	listOff := op.Offset(image.Pt(0, planH)).Push(gtx.Ops)
	listClip := clip.Rect{Max: listSize}.Push(gtx.Ops)
	lgtx := gtx
	lgtx.Constraints = layout.Exact(listSize)

	// The transcript's area holds all it draws, so a press or a drag on any
	// of it reaches the selection, on text under a row's button too.
	gioevent.Op(lgtx.Ops, &t.span)

	// A press on the transcript takes the keys from the composer, and a
	// press outside a selection clears it.
	for {
		ev, ok := lgtx.Event(pointer.Filter{Target: &t.focus, Kinds: pointer.Press}, key.FocusFilter{Target: &t.focus})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Kind == pointer.Press {
			lgtx.Execute(key.FocusCmd{Tag: &t.focus})
			t.menu, t.msg.confirm = false, ""
		}
	}
	bg := clip.Rect{Max: listSize}.Push(lgtx.Ops)
	gioevent.Op(lgtx.Ops, &t.focus)
	bg.Pop()
	t.keys(lgtx)
	if t.typeIn != nil {
		lgtx.Execute(key.FocusCmd{Tag: t.typeIn})
		t.typeIn = nil
	}

	t.pollVisFolds(lgtx)
	added := t.prepended(s)
	if d.ledger {
		t.buildLedgerRows(lgtx, d)
		t.rowsSet = false // why: the cached rows are Column's; switching back must rebuild them
	} else {
		rk := t.makeRowsKey(s, bk)
		if !t.rowsSet || rk != t.rowsKey {
			t.buildBaseRows(lgtx, d)
			t.rowsKey, t.rowsSet = rk, true
			t.rowsGen++
		}
		t.attachTail(d)
	}
	if added > 0 {
		t.keepPlace(s)
		for k := range t.span.ord { // the order follows the items, now added older ones
			t.span.ord[k] += added * rankItem * rankRow
		}
	}
	curLayout := LayoutColumn
	if t.isLedger() {
		curLayout = LayoutLedger
	}
	if t.prevLayout != "" && t.prevLayout != curLayout {
		t.keepLayoutScroll(s)
	}
	t.prevLayout = curLayout

	t.keepScroll(lgtx.Metric.PxPerSp)
	// A new prompt, sent here, queued or typed elsewhere, takes the
	// reader to the end wherever they scrolled.
	if t.lastPrmpt != t.prompt {
		t.prompt = t.lastPrmpt
		t.ToEnd()
	}
	// It follows the newest row once it overflows, from the end the first
	// time it does; a short one stays at the top.
	follow := t.list.Position.Length > listSize.Y
	if follow && !t.list.ScrollToEnd {
		t.list.Position.BeforeEnd = false
	}
	t.list.ScrollToEnd = follow
	if !follow {
		defer func() {
			if t.list.Position.Length > listSize.Y { // it overflows now: the next frame goes to its end
				gtx.Execute(op.InvalidateCmd{})
			}
		}()
	}
	t.findJump(lgtx, s, listSize.Y)
	t.revealJump(lgtx, s, prev, listSize.Y)
	t.awayJump(s)
	t.awayMark(s)
	t.autoScroll(lgtx, prev, listSize.Y)
	if t.span.drag { // the button held: the newest line does not pull the view away
		t.list.Position.BeforeEnd = true
	}
	t.prevFolds, t.visFolds = t.visFolds, t.prevFolds[:0]
	t.list.Layout(lgtx, len(t.rows)+2, t.msg.record(func(gtx layout.Context, i int) layout.Dimensions {
		switch i--; {
		case i < 0:
			topPad := gtx.Dp(theme.SpaceXL)
			if d.ledger {
				topPad = gtx.Dp(theme.SpaceM)
			}
			dm := layout.Dimensions{Size: image.Pt(listSize.X, topPad)}
			if t.older {
				dm = t.loadingOlder(gtx, th, listSize.X)
			}
			t.topH = dm.Size.Y
			return dm
		case i >= len(t.rows):
			botPad := gtx.Dp(theme.SpaceXL)
			if d.ledger {
				botPad = gtx.Dp(theme.Space2XL)
			}
			return layout.Dimensions{Size: image.Pt(listSize.X, botPad)}
		}
		r := &t.rows[i]
		t.span.rank, t.span.seq = r.rank*rankRow, 0
		t.wantWhole(s, r)
		gap := gtx.Dp(r.gap)
		topY := gap
		if i == t.away.row {
			topY += t.awayMarker(gtx, d, x0, col, topY)
		}
		if d.ledger && r.divider {
			paint.FillShape(gtx.Ops, th.Border, clip.Rect{Min: image.Pt(d.geom.x0, gap), Max: image.Pt(d.geom.x0+d.geom.ledgerW, gap+max(1, gtx.Dp(1)))}.Op())
			topY += gtx.Dp(theme.SpaceS + theme.Space2XS)
		}
		rowX, rowW := x0, col
		dx := 0
		if d.ledger {
			if r.gutter {
				gx, gh := x0-d.geom.gutterW, d.ledgerRowFirstLineH(gtx, r)
				if !d.geom.outside { // too narrow beside the text: the time and name go above it
					gx, gh = x0, d.ledgerHeadH(gtx)
				}
				at(gtx, image.Pt(gx, topY), func(gtx layout.Context) layout.Dimensions {
					d.ledgerGutter(gtx, r.gutterAt, gh, ledgerPromptRow(r))
					return layout.Dimensions{}
				})
				if !d.geom.outside {
					topY += gh + gtx.Dp(theme.SpaceXS)
				}
			}
			if r.inTurn && !r.isTail() && r.res == nil && r.seg == segItem {
				dx = gtx.Dp(rowPad - theme.Space2XS)
				rowW += dx
			}
		} else if !r.isTail() && r.res == nil && (r.seg == segHead || r.seg == segItem) {
			// A line of work starts at the prose's edge, as T3 Code's rows
			// do: its chevron where the text starts, its hover past it.
			dx = gtx.Dp(rowPad - theme.Space2XS)
			rowW += dx
		}
		t.msg.dy = topY
		gtx.Constraints = layout.Constraints{Max: image.Pt(rowW, gtx.Constraints.Max.Y)}
		off := op.Offset(image.Pt(rowX-dx, topY)).Push(gtx.Ops)
		var dims layout.Dimensions
		recordFold := func(id string, def bool) {
			f := visFold{id: id, def: def}
			t.visFolds = append(t.visFolds, f)
			if !gtx.Source.Enabled() && len(t.drawnFolds) < 64 {
				if t.drawnFolds == nil {
					t.drawnFolds = map[string]visFold{}
				}
				if _, ok := t.drawnFolds[id]; !ok {
					t.drawnFolds[id] = f
				}
			}
		}
		switch {
		case r.tail != nil:
			dims = r.tail(gtx)
		case r.seg == segInterrupted:
			dims = d.interrupted(gtx)
		case r.res != nil:
			recordFold("res:"+r.res.id, false)
			if r.b.kind == blockAsk {
				t.laid++
			}
			dims = d.resolvedLine(gtx, r.res, r.open)
		case r.seg == segHead:
			recordFold(logID(s, r.b), false)
			if !r.open {
				t.laid += r.b.hi - r.b.lo
			}
			dims = d.logHead(gtx, r)
		case r.seg == segTurn:
			if !r.running {
				recordFold(turnID(s, r.b), true)
			}
			dims = d.turnLine(gtx, r)
		case r.seg == segItem:
			t.laid++
			dims = d.logItem(gtx, r.i)
		case r.seg == segProse:
			t.laid++
			dims = d.proseRow(gtx, r.i)
		case r.seg == segChanges:
			dims = d.changeCard(gtx, r.b)
		case r.seg == segTodo:
			t.laid++
			dims = d.todoCard(gtx, &s.Content[r.i])
		case r.seg == segReply:
			t.laid++
			start, end, _ := turnTimes(s, r.b, r.b.hi == len(s.Content))
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			dims = d.reply(gtx, r.b.reply, r.running, start, end)
		default:
			t.laid += r.b.hi - r.b.lo
			if d.ledger && r.b.kind == blockPrompt {
				dims = d.ledgerPrompt(gtx, &s.Content[r.b.lo])
			} else {
				dims = d.block(gtx, r.b)
			}
		}
		off.Pop()
		if dims.Size.Y == 0 {
			return layout.Dimensions{Size: image.Pt(listSize.X, 0)}
		}
		return layout.Dimensions{Size: image.Pt(listSize.X, dims.Size.Y+topY)}
	}))
	if d.ledger {
		d.bottomFade(lgtx, listSize)
	}
	t.msg.spans(t.list.Position)
	t.awayTrack(s)
	t.awayMeasure(lgtx, s)
	t.span.rank, t.span.seq = math.MaxInt/2, 0 // what floats over the rows reads after them
	t.atTop(s, listSize.Y)
	t.forget(s)
	t.newOutput(lgtx, d, s, listSize)
	t.drawMinimap(lgtx, d, s, col, listSize)
	t.drawAside(lgtx, d, listSize)
	t.drawFind(lgtx, d, listSize)
	t.drawConfirm(lgtx, d, x0, listSize)
	t.drawTip(lgtx, d, x0, listSize)
	t.selectEvents(lgtx, prev) // once all the text took its presses
	t.drawCite(lgtx, d, s, prev, listSize)
	d.selMenu(lgtx, listSize)
	listClip.Pop()
	listOff.Pop()

	t.zoomLayer(gtx, d, size)
	if aide {
		d.moreMenu(gtx, size)
	}
	if t.live && !anim.Reduced() {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
	} else if !t.wakeAt.IsZero() {
		gtx.Execute(op.InvalidateCmd{At: t.wakeAt})
	}
	return layout.Dimensions{Size: size}
}

// settle drops the state of requests the chat resolved, and takes the
// clicks on the cards' buttons.
func (t *Transcript) settle(gtx layout.Context, s *session.Snapshot) {
	pending := map[string]bool{}
	if len(s.Content) < t.items { // a rewind took items away: so go the answers given after them
		t.done = slices.DeleteFunc(t.done, func(r resolved) bool {
			return r.after != "" && !slices.ContainsFunc(s.Content, func(it session.Item) bool { return it.ID == r.after })
		})
	}
	t.keyReq, t.keyAsk, t.keyPlan, t.keyNoSession, t.items = "", "", false, false, len(s.Content)
	t.last = ""
	if n := len(s.Content); n > 0 {
		t.last = s.Content[n-1].ID
	}
	if t.what == nil {
		t.what, t.plans, t.edits = map[string]string{}, map[string]bool{}, map[string]string{}
	}
	for _, a := range s.PendingApprovals {
		pending[a.ID] = true
		t.what[a.ID] = requestWhat(a, t.Dir)
		if a.Request != nil {
			t.edits[a.ID] = a.Request.Diff
		}
		if a.RequestType == session.RequestPlan {
			t.plans[a.ID], t.what[a.ID] = true, firstLine(strings.TrimLeft(deref(a.Detail), "# "))
		}
		if !t.sent[a.ID] {
			t.keyReq, t.keyPlan, t.keyNoSession = a.ID, a.RequestType == session.RequestPlan, a.NoSession
		}
	}
	if t.askQs == nil {
		t.askQs = map[string][]session.Question{}
	}
	for _, in := range s.PendingUserInputs {
		pending[in.ID] = true
		t.askQs[in.ID] = in.Questions
		if !t.sent[in.ID] {
			t.keyAsk = in.ID
		}
		a := t.asks[in.ID]
		if a == nil {
			a = &ask{active: -1, tuiStep: -1}
			t.asks[in.ID] = a
		}
		syncAsk(a, in)
	}
	for id, bs := range t.answer {
		for i := range bs {
			switch {
			case !bs[i].Clicked(gtx) || !pending[id]:
			case decisions[i] == "deny":
				t.openReason(id)
			default:
				t.decide(id, decisions[i])
			}
		}
		if !pending[id] {
			delete(t.answer, id)
		}
	}
	t.reasonEvents(gtx, pending)
	for id, a := range t.asks {
		for ci := range a.chips {
			if a.chips[ci].Clicked(gtx) && pending[id] {
				t.goStep(id, ci)
			}
		}
		for ri := range a.revRows {
			if a.revRows[ri].Clicked(gtx) && pending[id] {
				t.goStep(id, ri)
			}
		}
		if a.back.Clicked(gtx) && pending[id] && a.step > 0 {
			t.goStep(id, a.step-1)
		}
		if a.next.Clicked(gtx) && pending[id] {
			t.advanceStep(id)
		}
		for qi := range a.opts {
			for oi := range a.opts[qi] {
				if a.opts[qi][oi].Clicked(gtx) && pending[id] {
					t.pick(id, qi, oi)
					if oi == len(a.opts[qi])-1 {
						gtx.Execute(key.FocusCmd{Tag: &a.other[qi]})
					}
				}
			}
			for {
				ev, ok := a.other[qi].Update(gtx)
				if !ok {
					break
				}
				switch ev.(type) {
				case widget.ChangeEvent:
					if a.other[qi].Text() != "" {
						a.pick[qi] = len(a.opts[qi]) - 1
						a.touch(qi)
					}
				case widget.SubmitEvent:
					t.advanceStep(id)
				}
			}
		}
		if a.submit.Clicked(gtx) && pending[id] {
			if a.onReview() {
				t.submitAsk(id)
			} else {
				t.advanceStep(id)
			}
		}
		if !pending[id] {
			delete(t.asks, id)
			delete(t.askQs, id)
		}
	}
	for id := range t.sent {
		if !pending[id] {
			delete(t.sent, id)
			delete(t.what, id)
			delete(t.plans, id)
			delete(t.edits, id)
		}
	}
}

// syncAsk keeps a's per-question widgets and step state synchronized
// with snapshot request in (including live TUI step/done/answers).
func syncAsk(a *ask, in session.InputRequest) {
	n := len(in.Questions)
	for len(a.pick) < n {
		i := len(a.pick)
		q := in.Questions[i]
		pick := recommended(q)
		var m []bool
		if q.Multi {
			m, pick = make([]bool, len(q.Options)), -1
		}
		a.pick = append(a.pick, pick)
		a.multi = append(a.multi, m)
		a.moved = append(a.moved, false)
		a.touched = append(a.touched, false)
		a.opts = append(a.opts, make([]widget.Clickable, len(q.Options)+1))
		var ed widget.Editor
		ed.SingleLine, ed.Submit = true, true
		a.other = append(a.other, ed)
	}
	if len(a.pick) > n {
		a.pick, a.multi = a.pick[:n], a.multi[:n]
		a.moved, a.touched = a.moved[:n], a.touched[:n]
		a.opts, a.other = a.opts[:n], a.other[:n]
	}
	for i, q := range in.Questions {
		if len(a.opts[i]) != len(q.Options)+1 {
			a.opts[i] = make([]widget.Clickable, len(q.Options)+1)
			if a.pick[i] >= len(q.Options) {
				a.pick[i] = -1
			}
			if a.pick[i] < 0 && !q.Multi && a.untouched(i) {
				a.pick[i] = recommended(q)
			}
		}
		if q.Multi && len(a.multi[i]) != len(q.Options) {
			a.multi[i] = make([]bool, len(q.Options))
			a.pick[i] = -1
		} else if !q.Multi && a.multi[i] != nil {
			a.multi[i] = nil
		}
	}
	a.hasTUI = in.Step != nil
	a.review = in.Review
	steps := a.numSteps()
	if len(a.chips) != steps {
		a.chips = make([]widget.Clickable, steps)
	}
	if len(a.revRows) != n {
		a.revRows = make([]widget.Clickable, n)
	}
	if len(a.done) != n {
		a.done = make([]bool, n)
	}
	if in.Step != nil {
		sStep := min(max(*in.Step, 0), max(0, steps-1))
		stepChanged := sStep != a.tuiStep
		if stepChanged {
			a.tuiStep = sStep
			a.step = sStep
			a.active = -1
			a.note = ""
		}
		for i := 0; i < n; i++ {
			if i < len(in.Done) {
				a.done[i] = in.Done[i]
				if in.Done[i] {
					a.touch(i)
					if a.multiQ(i) != nil && i != a.step {
						a.moved[i] = true
					}
				}
			}
		}
		if a.lastTUIAns == nil {
			a.lastTUIAns = map[string][]string{}
		}
		for i, q := range in.Questions {
			vals, ok := in.Answered[q.ID]
			if !ok {
				continue
			}
			if !stepChanged && slices.Equal(a.lastTUIAns[q.ID], vals) {
				continue
			}
			a.lastTUIAns[q.ID] = slices.Clone(vals)
			a.applyTUIAnswer(i, q, vals)
		}
	}
	if steps > 0 && a.step >= steps {
		a.step = steps - 1
	}
	if a.step < 0 {
		a.step = 0
	}
}

// applyTUIAnswer reflects an answer reported by the live TUI onto
// question i's local widgets.
func (a *ask) applyTUIAnswer(i int, q session.Question, vals []string) {
	if i < 0 || i >= len(a.pick) {
		return
	}
	if m := a.multiQ(i); m != nil {
		clear(m)
		var extra []string
		for _, v := range vals {
			idx := slices.IndexFunc(q.Options, func(o session.PromptOption) bool { return o.Label == v })
			if idx >= 0 && idx < len(m) {
				m[idx] = true
			} else if strings.TrimSpace(v) != "" {
				extra = append(extra, v)
			}
		}
		if len(extra) > 0 {
			a.other[i].SetText(strings.Join(extra, ", "))
		}
		return
	}
	if len(vals) == 0 {
		return
	}
	v := vals[0]
	if idx := slices.IndexFunc(q.Options, func(o session.PromptOption) bool { return o.Label == v }); idx >= 0 {
		a.pick[i] = idx
		a.touch(i)
	} else if strings.TrimSpace(v) != "" {
		a.pick[i] = len(q.Options)
		a.other[i].SetText(v)
		a.touch(i)
	}
}

// keys takes the number keys, Enter, and step arrows while nothing else
// holds the keyboard: the transcript, one of its selections, or no one.
func (t *Transcript) keys(gtx layout.Context) {
	mine := gtx.Focused(nil) || gtx.Focused(&t.focus)
	if !mine || t.keyReq == "" && t.keyAsk == "" {
		return
	}
	fs := []gioevent.Filter{key.Filter{Name: key.NameReturn}, key.Filter{Name: key.NameEnter}}
	if t.keyReq == "" && t.keyAsk != "" {
		fs = append(fs, key.Filter{Name: key.NameLeftArrow}, key.Filter{Name: key.NameRightArrow})
	}
	for _, n := range "123456789" {
		fs = append(fs, key.Filter{Name: key.Name(string(n))})
	}
	for {
		ev, ok := gtx.Event(fs...)
		if !ok {
			return
		}
		if e, ok := ev.(key.Event); ok && e.State == key.Press {
			t.Key(string(e.Name))
		}
	}
}

// pollVisFolds applies clicks on fold headers drawn in the previous frame
// before checking the row cache so a click flips the fold on the same frame.
func (t *Transcript) pollVisFolds(gtx layout.Context) {
	pollOne := func(f visFold, g layout.Context) {
		c := t.clicks[f.id]
		if c == nil {
			return
		}
		open := isOpen(t.open, f.id, f.def)
		changed := false
		for c.Clicked(g) {
			open = !open
			changed = true
		}
		if changed {
			t.open[f.id] = open
		}
	}
	for _, f := range t.visFolds {
		pollOne(f, gtx)
	}
	if !gtx.Source.Enabled() {
		for _, f := range t.drawnFolds {
			pollOne(f, layout.Context{})
		}
	}
}

func (t *Transcript) makeBlocksKey(s *session.Snapshot) blocksKey {
	k := blocksKey{
		sessionID:  s.SessionID,
		rev:        s.Revision,
		contentLen: len(s.Content),
		doneLen:    len(t.done),
	}
	if n := len(s.Content); n > 0 {
		k.firstID = s.Content[0].ID
		last := &s.Content[n-1]
		k.lastID = last.ID
		k.lastUpdated = last.UpdatedAt
		k.lastTextLen = len(last.Text)
		if last.Tool != nil {
			k.lastToolOut = len(last.Tool.Output)
			k.lastToolStat = last.Tool.Status
		}
	}
	if len(t.done) > 0 {
		var h uint64 = 14695981039346656037
		for i := range t.done {
			r := &t.done[i]
			for j := range len(r.id) {
				h ^= uint64(r.id[j])
				h *= 1099511628211
			}
			h ^= uint64(r.at)
			h *= 1099511628211
			if r.gone {
				h ^= 1
				h *= 1099511628211
			}
		}
		k.doneHash = h
	}
	return k
}

func openMapHash(m map[string]bool) uint64 {
	var h uint64
	for k, v := range m {
		var kh uint64 = 14695981039346656037
		for i := range len(k) {
			kh ^= uint64(k[i])
			kh *= 1099511628211
		}
		if v {
			kh ^= 0x9e3779b97f4a7c15
		}
		h ^= kh
	}
	return h
}

func (t *Transcript) makeRowsKey(s *session.Snapshot, bk blocksKey) rowsKey {
	k := rowsKey{
		blocksKey:    bk,
		sessionState: s.SessionState,
		running:      s.Running,
		turnsLen:     len(s.Turns),
		toolMode:     t.toolMode(),
		openHash:     openMapHash(t.open),
		openLen:      len(t.open),
		dir:          t.Dir,
	}
	if n := len(s.Turns); n > 0 {
		last := &s.Turns[n-1]
		k.lastTurnState = last.State
		if last.CompletedAt != nil {
			k.lastTurnEnd = *last.CompletedAt
		}
	}
	return k
}

// buildBaseRows lays the blocks and answered requests' lines out as the
// cached base rows (without per-frame tail closures).
func (t *Transcript) buildBaseRows(gtx layout.Context, d *cdraw) {
	t.rows = t.rows[:0]
	running := runningTurns(d.s)
	stopped := interruptedTurns(d.s)
	quiet := quietSettings(d.s.Content)
	mode := t.toolMode()
	t.tool.shells.index(d.s.Content)
	k := 0
	lastOf := map[string]int{} // each turn's last block: only it runs
	start := 0                 // the row of the current turn's first part
	for bi, b := range t.blocks {
		lastOf[b.turn] = bi
	}
	for bi, b := range t.blocks {
		// A stopped turn ends on a quiet Interrupted line.
		if bi > 0 && t.blocks[bi-1].turn != "" && t.blocks[bi-1].turn != b.turn && stopped[t.blocks[bi-1].turn] {
			t.rows = append(t.rows, trow{seg: segInterrupted})
		}
		for ; k < len(t.done) && t.done[k].at <= b.lo; k++ {
			t.resRow(gtx, block{}, &t.done[k])
		}
		if b.kind == blockAsk {
			t.resRow(gtx, b, t.askLine(d.s, b.lo))
			t.changeRow(d, b, running(b, b.hi == len(d.s.Content)))
			continue
		}
		if b.kind != blockTurn {
			if !quiet[b.lo] {
				t.rows = append(t.rows, trow{b: b})
			}
			continue
		}
		last := b.turn == "" || lastOf[b.turn] == bi
		run := running(b, b.hi == len(d.s.Content)) && last
		// A finished turn's work sits under its Worked for line, which
		// hides it on a click, as T3 Code folds a turn; it shows by default.
		// A turn an answer split is one turn here: the line goes above its
		// first part, times the whole turn, and hides the work of every part
		// but not the lines that answered it.
		whole := b
		whole.lo = b.from
		if b.lo == b.from {
			start = len(t.rows)
		}
		show := t.toggled(gtx, turnID(d.s, whole), true)
		t.turnRows(gtx, d, b, run, quiet, mode)
		// Zen has no Worked for line, as T3 Code folds no turn in it.
		if work := len(t.rows) > start; work && !run && last && mode != ToolsZen {
			if !show {
				t.rows = append(t.rows[:start], slices.DeleteFunc(slices.Clone(t.rows[start:]), func(r trow) bool { return r.res == nil })...)
			}
			t.rows = slices.Insert(t.rows, start, trow{b: whole, seg: segTurn, open: show})
		}
		if b.todo >= 0 && mode != ToolsZen {
			t.rows = append(t.rows, trow{b: b, seg: segTodo, i: b.todo})
		}
		// Zen shows a reply once its turn ends: before, it may be words
		// between calls.
		if b.reply >= 0 && !(mode == ToolsZen && run) {
			// A request answered before the reply came stays with the work
			// it was about, above the reply.
			for ; k < len(t.done) && t.done[k].at <= b.reply; k++ {
				t.resRow(gtx, block{}, &t.done[k])
			}
			t.rows = append(t.rows, trow{b: b, seg: segReply, running: run})
		}
		if run {
			t.rows = append(t.rows, trow{b: whole, seg: segTurn, running: true})
		}
		t.changeRow(d, b, run)
	}
	if n := len(t.blocks); n > 0 && stopped[t.blocks[n-1].turn] {
		t.rows = append(t.rows, trow{seg: segInterrupted})
	}
	for ; k < len(t.done); k++ {
		t.resRow(gtx, block{}, &t.done[k])
	}
	for i := range t.rows {
		t.rows[i].gap = rowGap(t.rows, i)
		t.rows[i].rank = rowRank(t.rows, i)
	}
	t.baseLen = len(t.rows)
}

// attachTail appends this frame's tail widgets (pending approvals, errors,
// working line) in place after the cached base rows without copying baseRows.
func (t *Transcript) attachTail(d *cdraw) {
	if len(t.rows) > t.baseLen {
		clear(t.rows[t.baseLen:])
		t.rows = t.rows[:t.baseLen]
	}
	for _, w := range d.tail() {
		t.rows = append(t.rows, trow{tail: w})
	}
	for i := t.baseLen; i < len(t.rows); i++ {
		t.rows[i].gap = rowGap(t.rows, i)
		t.rows[i].rank = rowRank(t.rows, i)
	}
}

// turnRows lays out turn b's work log, split around what was written for
// the reader: each such item is a row of its own in reading order, and
// each run of calls between two of them is a fold, "summary, fold,
// summary, fold". A fold of one call is that call's row, as T3 Code draws
// one, except the newest while the turn runs, whose header says what it
// does now.
func (t *Transcript) turnRows(gtx layout.Context, d *cdraw, b block, run bool, quiet map[int]bool, mode string) {
	g := t.fold[:0]
	defer func() { t.fold = g }()
	fold := func(final bool) {
		if len(g) == 0 {
			return
		}
		gb := b
		gb.lo, gb.hi, gb.reply, gb.tools = g[0], g[len(g)-1]+1, -1, 0
		thoughts := 0
		for _, i := range g {
			switch d.s.Content[i].Kind {
			case session.KindToolCall:
				gb.tools++
			case session.KindReasoningText, session.KindReasoningSummaryText:
				thoughts++
			}
		}
		items := g // read before g grows again
		g = g[:0]
		// With calls hidden, hooks and notices left alone make no line.
		if mode != ToolsAll && gb.tools == 0 && thoughts == 0 {
			return
		}
		if len(items) == 1 && gb.tools == 1 && !(run && final) {
			t.rows = append(t.rows, trow{b: gb, seg: segItem, i: gb.lo, top: true})
			return
		}
		open := t.toggled(gtx, logID(d.s, gb), false)
		t.rows = append(t.rows, trow{b: gb, seg: segHead, open: open, running: run && final})
		for _, i := range items {
			if !open {
				break
			}
			t.rows = append(t.rows, trow{b: gb, seg: segItem, i: i})
		}
	}
	for i := b.lo; i < b.hi; i++ {
		it := &d.s.Content[i]
		switch {
		case i == b.reply && !(mode == ToolsZen && run && report(it.Text)) || quiet[i] || isTodo(it.Tool) || t.tool.shells.isAttached(i):
		case mode == ToolsZen:
			if zenShows(it) {
				kind := segProse
				if it.Kind == session.KindToolCall {
					kind = segItem
				}
				t.rows = append(t.rows, trow{b: b, seg: kind, i: i, top: true})
			}
		case it.Kind == session.KindToolCall && callHidden(mode, it.Tool):
		case outside(it):
			fold(false)
			t.rows = append(t.rows, trow{b: b, seg: segProse, i: i})
		default:
			g = append(g, i)
		}
	}
	fold(true)
}

// rankItem and rankRow space the selection's order keys: rows by their
// item, then the selectables in a row as it draws them.
const (
	rankItem = 8
	rankRow  = 1024
)

// rowRank is row i's place in reading order, from its item, so a row
// keeps it while folds open and close above it and new items come in
// below: the selection's order follows it.
func rowRank(rows []trow, i int) int {
	r := &rows[i]
	k := 0
	switch {
	case r.isTail():
	case r.res != nil && r.b.kind == blockAsk: // the question call's own line
		k = r.b.lo*rankItem + 3
	case r.res != nil:
		k = r.res.at*rankItem + 1
	case r.seg == segHead:
		k = r.b.lo*rankItem + 2
	case r.seg == segItem, r.seg == segTodo, r.seg == segProse:
		k = r.i*rankItem + 3
	case r.seg == segReply:
		k = r.b.reply*rankItem + 3
	case r.seg == segChanges:
		k = (r.b.hi-1)*rankItem + 5
	default:
		k = r.b.lo*rankItem + 3
	}
	if i > 0 {
		k = max(k, rows[i-1].rank+1)
	}
	return k
}

// resRow adds resolved line r, open when the user opened it, unless the
// transcript's own line took it over; b is its question's block, if any.
func (t *Transcript) resRow(gtx layout.Context, b block, r *resolved) {
	if !r.gone {
		t.rows = append(t.rows, trow{b: b, res: r, open: t.toggled(gtx, "res:"+r.id, false)})
	}
}

// changeRow adds the change card of b's turn, all its parts, under its
// last part once it has stopped running.
func (t *Transcript) changeRow(d *cdraw, b block, run bool) {
	whole := b
	whole.kind, whole.lo = blockTurn, b.from
	if !run && !b.more && len(t.changesOf(d, whole)) > 0 {
		t.rows = append(t.rows, trow{b: whole, seg: segChanges})
	}
}

// rowGap is the space above row i: 32 from the end of a turn to the
// next prompt, 16 from a prompt to its turn, none inside a work log, 12
// to anything else.
func rowGap(rows []trow, i int) unit.Dp {
	if i == 0 {
		return 0
	}
	r, prev := &rows[i], &rows[i-1]
	prompt := func(r *trow) bool { return !r.isTail() && r.res == nil && r.b.kind == blockPrompt }
	sameTurn := !r.isTail() && r.res == nil && !prev.isTail() && prev.res == nil && r.b.kind == blockTurn && prev.b.kind == blockTurn && r.b.turn == prev.b.turn
	switch {
	case sameTurn && r.seg == segItem && !r.top:
		return 0
	case sameTurn && r.seg != segTodo && r.seg != segChanges && prev.seg != segTodo:
		return theme.SpaceS // T3 Code's 8 under a line of work or prose
	case sameTurn:
		return gapInTurn
	case prompt(r):
		return gapTurn
	case prompt(prev):
		return gapPrompt
	case !r.isTail() && r.res == nil && r.b.kind == blockTurn && !prev.isTail() && prev.res == nil:
		return gapTurn
	}
	return gapInTurn
}

// ToEnd scrolls to the newest row and follows it again, as sending does.
func (t *Transcript) ToEnd() {
	t.list.Position.BeforeEnd = false
}

// lastPrompt is the id of s's newest prompt, "" for none.
func lastPrompt(s *session.Snapshot) string {
	for i := len(s.Content) - 1; i >= 0; i-- {
		if s.Content[i].Kind == session.KindUserPrompt {
			return s.Content[i].ID
		}
	}
	return ""
}

// newOutput is the "New output" pill once the reader scrolled up and the
// chat added to it; a click goes back to the end and follows again.
func (t *Transcript) newOutput(gtx layout.Context, d *cdraw, s *session.Snapshot, size image.Point) {
	sig := "" // the newest item, so a page of older ones is not new output
	if n := len(s.Content); n > 0 {
		sig += s.Content[n-1].ID + s.Content[n-1].UpdatedAt + strconv.Itoa(len(s.Content[n-1].Text))
	}
	sig += strconv.Itoa(len(s.PendingApprovals) + len(s.PendingUserInputs))
	if !t.list.Position.BeforeEnd {
		t.seen = sig
		return
	}
	if t.seen == "" || t.seen == sig {
		return
	}
	for t.pill.Clicked(gtx) {
		t.ToEnd()
		t.seen = sig
		return
	}
	th := d.th
	call, td := measure(gtx, func(gtx layout.Context) layout.Dimensions {
		return d.lbl(gtx, medium(th.UIFont), th.Sp(theme.Small), th.Fg, "New output")
	})
	w, h := td.Size.X+2*gtx.Dp(theme.SpaceM)+gtx.Dp(18), gtx.Dp(hit)
	p := image.Pt((size.X-w)/2, size.Y-gtx.Dp(theme.SpaceS)-h)
	at(gtx, p, func(gtx layout.Context) layout.Dimensions {
		return clickable(gtx, &t.pill, func(gtx layout.Context) layout.Dimensions {
			fill := th.SurfaceElevated
			if t.pill.Hovered() {
				fill = theme.Mix(fill, th.Fg, 0.08)
			}
			rrect(gtx, image.Rect(0, 0, w, h), gtx.Dp(14), fill, th.BorderStrong)
			icon(gtx, image.Pt(gtx.Dp(theme.SpaceM), (h-gtx.Dp(14))/2), "chevron-down", 14, th.Muted)
			at(gtx, image.Pt(gtx.Dp(theme.SpaceM)+gtx.Dp(18), (h-td.Size.Y)/2), func(gtx layout.Context) layout.Dimensions {
				call.Add(gtx.Ops)
				return td
			})
			return layout.Dimensions{Size: image.Pt(w, h)}
		})
	})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
