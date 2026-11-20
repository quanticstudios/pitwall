package panel

import (
	"image"
	"io"
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
	// Invalidate asks for a frame from another goroutine: the side panel's
	// markdown file and its pictures, read in the background.
	Invalidate func()

	list   layout.List
	blocks []block
	rows   []trow
	laid   int // transcript items laid out in the last frame
	live   bool
	epoch  time.Time
	now    time.Time

	open   map[string]bool // folds the user opened or closed, by id
	clicks map[string]*widget.Clickable
	sels   map[string]*widget.Selectable
	docs   map[string]*reply    // parsed replies by item id
	cards  map[string]cardCache // command outputs' cards by item id
	copied map[string]time.Time // copy buttons, while they show Copied
	thumbs thumbs
	focus  int // the transcript's own key focus tag
	drawn  []*widget.Selectable
	picked *widget.Selectable // the selectable with the selection

	answer  map[string]*[3]widget.Clickable // per pending approval, one per decisions entry
	sent    map[string]bool                 // requests answered here that the chat has not resolved yet
	answers []Answer                        // not taken by Answers yet
	reasons map[string]*widget.Editor       // approvals whose Deny or Refine opened the reason field
	inputs  []InputAnswer                   // not taken by Inputs yet
	asks    map[string]*ask                 // per pending question request
	done    []resolved                      // requests answered here, as one line each
	keyReq  string                          // the approval the number keys answer
	keyPlan bool                            // keyReq is a plan: 1 approves, 2 refines
	keyAsk  string                          // else the question they pick in
	typeIn  *widget.Editor                  // an Other field a key picked, to focus in the next Layout
	askQs   map[string][]session.Question   // pending questions by request
	what    map[string]string               // pending approvals' commands, for their line
	plans   map[string]bool                 // pending approvals that are plans
	items   int                             // len(Content) this frame
	last    string                          // Content's last item's id this frame
	diffs   map[string]diffCache            // edits' lines by item id
	tool    toolState                       // the tool rows' scroll, shells and images
	side    side                            // the Files and Changes panel
	changes map[string]changeCache          // turns' changed files by their first item id

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
}

// Answer is an approval answered with its buttons or keys.
type Answer struct {
	Request  string
	Decision string // allow, allow-session, deny
	Reason   string // what a denial tells the agent to do instead, "" for none
}

// InputAnswer is a question answered on its card: question id to the
// chosen labels or typed text, as proto.ChatAnswer carries.
type InputAnswer struct {
	Request string
	Answers map[string][]string
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
	prose          bool   // what is words, not a command
	denied         bool   // a permission denied, not a plan sent back
	after          string // the item before it when answered, "" for none
	at             int    // len(Content) when answered
	when           time.Time
}

// ask is a question card's state.
type ask struct {
	pick []int // per question, the option picked; len(options) is the typed answer, -1 none
	// multi is, per multiSelect question, each option checked, nil for a
	// single-select one; there pick is the option last toggled.
	multi [][]bool
	moved []bool // a multiSelect question Enter moved on from
	// active is the question the number keys act on, the one the user last
	// clicked into; -1 for the first unanswered one.
	active int
	other  []widget.Editor
	opts   [][]widget.Clickable // per question, one per option and one for the typed row
	submit widget.Clickable
}

// trow is one row of the list: a prompt or system line, a piece of a
// turn, an answered request's line, or what follows the items.
type trow struct {
	b       block
	seg     rowSeg
	i       int  // segItem: the item
	last    bool // segHead, segItem: the work log's box ends here
	open    bool // segHead: the log is open
	running bool // segHead, segReply: the turn runs
	res     *resolved
	tail    layout.Widget
	gap     unit.Dp // space above it
}

// rowSeg is which piece of its block a row draws.
type rowSeg uint8

const (
	segBlock   rowSeg = iota // a prompt or a system line
	segHead                  // a turn's work log header
	segItem                  // an item in an open work log
	segReply                 // a turn's reply
	segTodo                  // a turn's task list, its last task-list call i
	segChanges               // a finished turn's change card
)

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
// a number picks a question's option and Enter sends it. It reports
// whether the key did something. The window's composer calls it while
// it is empty; the transcript takes the keys itself when it has focus.
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
	opts := 0
	if a := t.asks[t.keyAsk]; a != nil && len(a.opts) > 0 {
		opts = len(a.opts[t.askQuestion(a)]) - 1
	}
	act, ok := actionFor(name, t.keyReq != "", opts)
	if !ok {
		return false
	}
	switch {
	case act.decision == "deny":
		t.openReason(t.keyReq)
	case act.decision != "":
		t.decide(t.keyReq, act.decision)
	case act.submit:
		a := t.asks[t.keyAsk]
		// Enter on a multiSelect question moves on to the next open one,
		// and sends the answers once none is left.
		if q := t.askQuestion(a); a.multiQ(q) != nil {
			a.moved[q], a.active = true, -1
			if next := a.current(); next != q && a.open(next) {
				return true
			}
		}
		t.sendAsk(t.keyAsk)
	default:
		a := t.asks[t.keyAsk]
		q := t.askQuestion(a)
		a.choose(q, act.option)
		if act.option == len(a.opts[q])-1 { // Other: type the answer
			t.typeIn = &a.other[q]
		}
	}
	return true
}

// choose picks option o of question q: a multiSelect question toggles
// it and keeps the keys, the typed row aside; a single-select pick moves
// the keys on to the next unanswered question.
func (a *ask) choose(q, o int) {
	a.pick[q], a.active = o, q
	if m := a.multiQ(q); m != nil {
		if o < len(m) {
			m[o] = !m[o]
		}
		return
	}
	if o < len(a.opts[q])-1 {
		a.active = -1
	}
}

// multiQ is question q's checks, nil unless it is multiSelect.
func (a *ask) multiQ(q int) []bool {
	if q < len(a.multi) {
		return a.multi[q]
	}
	return nil
}

// askQuestion is the question the number keys pick in: the first
// without an answer, or a multiSelect one Enter has not moved on from;
// else the last.
func (t *Transcript) askQuestion(a *ask) int { return a.current() }

// open is whether question i still waits on the user: no pick, or
// multiSelect and not moved on from.
func (a *ask) open(i int) bool {
	return a.pick[i] < 0 || a.multiQ(i) != nil && !a.moved[i]
}

// current is the question the number keys act on: the one the user is in,
// else the first without an answer, else the last.
func (a *ask) current() int {
	if a.active >= 0 && a.active < len(a.pick) {
		return a.active
	}
	for i := range a.pick {
		if a.open(i) {
			return i
		}
	}
	return len(a.pick) - 1
}

// init makes a new Transcript's maps. It sets fields one by one, so the
// thumbnails' lock, which a decode may hold, is never overwritten.
func (t *Transcript) init() {
	t.open, t.clicks, t.sels, t.selAt = map[string]bool{}, map[string]*widget.Clickable{}, map[string]*widget.Selectable{}, map[string]int{}
	t.docs, t.copied = map[string]*reply{}, map[string]time.Time{}
	t.answer, t.sent, t.asks = map[string]*[3]widget.Clickable{}, map[string]bool{}, map[string]*ask{}
	t.reasons = map[string]*widget.Editor{}
	t.list.Axis = layout.Vertical
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
		t.live = true
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
	t.done = append(t.done, resolved{id: id, line: line, what: what, prose: prose, denied: decision == "deny" && !t.plans[id], after: t.last, at: t.items, when: t.now})
}

// Refused takes back the answer to request id that the daemon refused:
// the line saying it was answered goes, and its card comes back while the
// request waits.
func (t *Transcript) Refused(id string) {
	delete(t.sent, id)
	t.done = slices.DeleteFunc(t.done, func(r resolved) bool { return r.id == id })
}

// sendAsk answers question request id with its card's picks, once each
// question has one.
func (t *Transcript) sendAsk(id string) {
	a := t.asks[id]
	if a == nil || t.sent[id] {
		return
	}
	ans, ok := a.answers(t.askQs[id])
	if !ok {
		return
	}
	t.sent[id] = true
	t.inputs = append(t.inputs, InputAnswer{Request: id, Answers: ans})
	var picked []string
	for _, v := range ans {
		picked = append(picked, v...)
	}
	slices.Sort(picked)
	t.done = append(t.done, resolved{id: id, line: "Answered", what: strings.Join(picked, ", "), prose: true, after: t.last, at: t.items, when: t.now})
}

// answers are the card's picks by question id, false while a question
// has none.
func (a *ask) answers(qs []session.Question) (map[string][]string, bool) {
	out := map[string][]string{}
	for i, q := range qs {
		if i >= len(a.pick) {
			return nil, false
		}
		other := strings.TrimSpace(a.other[i].Text())
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
			out[q.ID] = picked
			continue
		}
		switch p := a.pick[i]; {
		case p >= 0 && p < len(q.Options):
			out[q.ID] = []string{q.Options[p].Label}
		case p == len(q.Options) && other != "":
			out[q.ID] = []string{other}
		default:
			return nil, false
		}
	}
	return out, len(qs) > 0
}

// Layout draws s filling gtx's constraints, with Open in aide in a quiet
// menu when aide is set, and the Files and Changes panel at its right
// while it is open.
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
	t.now, t.live, t.laid, t.drawn = gtx.Now, false, 0, t.drawn[:0]
	t.frame++
	t.settle(gtx, s)
	t.findUpdate(gtx, s)
	if t.aideBtn.Clicked(gtx) && aide {
		t.aide, t.menu = true, false
	}
	for t.more.Clicked(gtx) {
		t.menu = !t.menu
	}
	size := gtx.Constraints.Max
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: size}.Op())

	// A press on the transcript takes the keys from the composer, and a
	// press outside a selection clears it.
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &t.focus, Kinds: pointer.Press}, key.FocusFilter{Target: &t.focus})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Kind == pointer.Press {
			gtx.Execute(key.FocusCmd{Tag: &t.focus})
			t.menu, t.msg.confirm = false, ""
		}
	}
	bg := clip.Rect{Max: size}.Push(gtx.Ops)
	gioevent.Op(gtx.Ops, &t.focus)
	bg.Pop()
	t.keys(gtx)
	if t.typeIn != nil {
		gtx.Execute(key.FocusCmd{Tag: t.typeIn})
		t.typeIn = nil
	}

	d := &cdraw{t: t, th: th, s: s, sec: gtx.Now.Sub(t.epoch).Seconds()}
	side := gtx.Dp(theme.SpaceXL)
	col := max(0, size.X-2*side)
	if t.Width > 0 {
		col = min(col, gtx.Dp(t.Width))
	}
	d.col, d.proseW = col, proseCap(col, d.avgChar(gtx))
	x0 := (size.X - col) / 2
	t.blocks = blocks(s.Content, t.blocks)
	added := t.prepended(s)
	t.buildRows(gtx, d)
	if added > 0 {
		t.keepPlace(s)
	}

	// A new prompt, sent here, queued or typed elsewhere, takes the
	// reader to the end wherever they scrolled.
	if id := lastPrompt(s); id != t.prompt {
		t.prompt = id
		t.ToEnd()
	}
	// It follows the newest row once it overflows, from the end the first
	// time it does; a short one stays at the top.
	follow := t.list.Position.Length > size.Y
	if follow && !t.list.ScrollToEnd {
		t.list.Position.BeforeEnd = false
	}
	t.list.ScrollToEnd = follow
	if !follow {
		defer func() {
			if t.list.Position.Length > size.Y { // it overflows now: the next frame goes to its end
				gtx.Execute(op.InvalidateCmd{})
			}
		}()
	}
	t.findJump(gtx, s, size.Y)
	t.list.Layout(gtx, len(t.rows)+2, t.msg.record(func(gtx layout.Context, i int) layout.Dimensions {
		switch i--; {
		case i < 0:
			d := layout.Dimensions{Size: image.Pt(size.X, gtx.Dp(theme.SpaceXL))}
			if t.older {
				d = t.loadingOlder(gtx, th, size.X)
			}
			t.topH = d.Size.Y
			return d
		case i >= len(t.rows):
			return layout.Dimensions{Size: image.Pt(size.X, gtx.Dp(theme.SpaceXL))}
		}
		r := &t.rows[i]
		t.wantWhole(s, r)
		gap := gtx.Dp(r.gap)
		t.msg.dy = gap
		gtx.Constraints = layout.Constraints{Max: image.Pt(col, gtx.Constraints.Max.Y)}
		off := op.Offset(image.Pt(x0, gap)).Push(gtx.Ops)
		var dims layout.Dimensions
		switch {
		case r.tail != nil:
			dims = r.tail(gtx)
		case r.res != nil:
			dims = d.resolvedLine(gtx, r.res)
		case r.seg == segHead:
			if !r.open {
				t.laid += r.b.hi - r.b.lo
				if r.b.reply >= 0 {
					t.laid--
				}
			}
			dims = d.logHead(gtx, r.b, r.open, r.running, r.last)
		case r.seg == segItem:
			t.laid++
			dims = d.logItem(gtx, r.i, r.last)
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
			dims = d.block(gtx, r.b)
		}
		off.Pop()
		if dims.Size.Y == 0 {
			return layout.Dimensions{Size: image.Pt(size.X, 0)}
		}
		return layout.Dimensions{Size: image.Pt(size.X, dims.Size.Y+gap)}
	}))
	t.msg.spans(t.list.Position)
	t.pickSelection(gtx)
	t.atTop(s, size.Y)
	t.forget(s)
	t.newOutput(gtx, d, s, size)
	t.drawAside(gtx, d, size)
	t.drawFind(gtx, d, size)
	t.drawConfirm(gtx, d, x0, size)
	t.drawTip(gtx, d, x0, size)
	t.zoomLayer(gtx, d, size)
	if aide {
		d.moreMenu(gtx, size)
	}
	if t.live {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(time.Second / 30)})
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
	t.keyReq, t.keyAsk, t.keyPlan, t.items = "", "", false, len(s.Content)
	t.last = ""
	if n := len(s.Content); n > 0 {
		t.last = s.Content[n-1].ID
	}
	if t.what == nil {
		t.what, t.plans = map[string]string{}, map[string]bool{}
	}
	for _, a := range s.PendingApprovals {
		pending[a.ID] = true
		t.what[a.ID] = requestWhat(a, t.Dir)
		if a.RequestType == session.RequestPlan {
			t.plans[a.ID], t.what[a.ID] = true, firstLine(strings.TrimLeft(deref(a.Detail), "# "))
		}
		if !t.sent[a.ID] {
			t.keyReq, t.keyPlan = a.ID, a.RequestType == session.RequestPlan
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
			a = &ask{moved: make([]bool, len(in.Questions)), active: -1}
			for _, q := range in.Questions {
				var m []bool
				pick := recommended(q)
				if q.Multi {
					m, pick = make([]bool, len(q.Options)), -1
				}
				a.pick = append(a.pick, pick)
				a.multi = append(a.multi, m)
				a.opts = append(a.opts, make([]widget.Clickable, len(q.Options)+1))
			}
			a.other = make([]widget.Editor, len(in.Questions))
			for i := range a.other {
				a.other[i].SingleLine, a.other[i].Submit = true, true
			}
			t.asks[in.ID] = a
		}
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
		for qi := range a.opts {
			for oi := range a.opts[qi] {
				if a.opts[qi][oi].Clicked(gtx) {
					a.choose(qi, oi)
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
					}
				case widget.SubmitEvent:
					t.sendAsk(id)
				}
			}
		}
		if a.submit.Clicked(gtx) && pending[id] {
			t.sendAsk(id)
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
		}
	}
}

// keys takes the number keys and Enter while nothing else holds the
// keyboard: the transcript, one of its selections, or no one.
func (t *Transcript) keys(gtx layout.Context) {
	mine := gtx.Focused(nil) || gtx.Focused(&t.focus) || t.picked != nil && gtx.Focused(t.picked)
	if !mine || t.keyReq == "" && t.keyAsk == "" {
		return
	}
	fs := []gioevent.Filter{key.Filter{Name: key.NameReturn}, key.Filter{Name: key.NameEnter}}
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

// pickSelection keeps one selection in the transcript: a new one clears
// the last.
func (t *Transcript) pickSelection(gtx layout.Context) {
	for _, s := range t.drawn {
		if s != t.picked && gtx.Focused(s) {
			if t.picked != nil {
				t.picked.ClearSelection()
			}
			t.picked = s
		}
	}
	if t.picked != nil && gtx.Focused(&t.focus) {
		t.picked.ClearSelection()
		t.picked = nil
	}
}

// buildRows lays the blocks, the answered requests' lines and the tail
// out as the list's rows: a turn as its work log's header, each item of
// the open log, and its reply. It takes the clicks on the logs' headers,
// so a fold changes the rows in the frame it is clicked.
func (t *Transcript) buildRows(gtx layout.Context, d *cdraw) {
	t.rows = t.rows[:0]
	running := runningTurns(d.s)
	stopped := interruptedTurns(d.s)
	quiet := quietSettings(d.s.Content)
	t.tool.shells.index(d.s.Content)
	k := 0
	for bi, b := range t.blocks {
		// A stopped turn ends on a quiet Interrupted line.
		if bi > 0 && t.blocks[bi-1].turn != "" && t.blocks[bi-1].turn != b.turn && stopped[t.blocks[bi-1].turn] {
			t.rows = append(t.rows, trow{tail: d.interrupted})
		}
		for ; k < len(t.done) && t.done[k].at <= b.lo; k++ {
			t.rows = append(t.rows, trow{res: &t.done[k]})
		}
		if b.kind != blockTurn {
			if !quiet[b.lo] {
				t.rows = append(t.rows, trow{b: b})
			}
			continue
		}
		run := running(b, b.hi == len(d.s.Content))
		logged := b.hi - b.lo - b.todos
		if b.reply >= 0 {
			logged--
		}
		if logged > 0 || run {
			// A work log stays one line, running or done, until the user
			// opens it.
			open := t.toggled(gtx, logID(d.s, b), false)
			t.rows = append(t.rows, trow{b: b, seg: segHead, open: open, running: run, last: true})
			for i := b.lo; open && i < b.hi; i++ {
				if i != b.reply && !isTodo(d.s.Content[i].Tool) && !t.tool.shells.isAttached(i) && !quiet[i] {
					t.rows[len(t.rows)-1].last = false
					t.rows = append(t.rows, trow{b: b, seg: segItem, i: i, last: true})
				}
			}
		}
		if b.todo >= 0 {
			t.rows = append(t.rows, trow{b: b, seg: segTodo, i: b.todo})
		}
		if b.reply >= 0 {
			// A request answered before the reply came stays with the work
			// it was about, above the reply.
			for ; k < len(t.done) && t.done[k].at <= b.reply; k++ {
				t.rows = append(t.rows, trow{res: &t.done[k]})
			}
			t.rows = append(t.rows, trow{b: b, seg: segReply, running: run})
		}
		if !run && len(t.changesOf(d, b)) > 0 {
			t.rows = append(t.rows, trow{b: b, seg: segChanges})
		}
	}
	if n := len(t.blocks); n > 0 && stopped[t.blocks[n-1].turn] {
		t.rows = append(t.rows, trow{tail: d.interrupted})
	}
	for ; k < len(t.done); k++ {
		t.rows = append(t.rows, trow{res: &t.done[k]})
	}
	for _, w := range d.tail() {
		t.rows = append(t.rows, trow{tail: w})
	}
	for i := range t.rows {
		t.rows[i].gap = rowGap(t.rows, i)
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
	prompt := func(r *trow) bool { return r.tail == nil && r.res == nil && r.b.kind == blockPrompt }
	sameTurn := r.tail == nil && r.res == nil && prev.tail == nil && prev.res == nil && r.b.kind == blockTurn && prev.b.kind == blockTurn && r.b.lo == prev.b.lo
	switch {
	case sameTurn && r.seg == segItem:
		return 0
	case sameTurn:
		return gapInTurn
	case prompt(r):
		return gapTurn
	case prompt(prev):
		return gapPrompt
	case r.tail == nil && r.res == nil && r.b.kind == blockTurn && prev.tail == nil && prev.res == nil:
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
