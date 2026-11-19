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
	// Copy puts text on the clipboard: the window's clipboard path. Nil
	// writes it directly.
	Copy func(gtx layout.Context, text string)

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
	copied map[string]time.Time // copy buttons, while they show Copied
	thumbs thumbs
	focus  int // the transcript's own key focus tag
	drawn  []*widget.Selectable
	picked *widget.Selectable // the selectable with the selection

	answer  map[string]*[3]widget.Clickable // per pending approval, one per decisions entry
	sent    map[string]bool                 // requests answered here that the chat has not resolved yet
	answers []Answer                        // not taken by Answers yet
	inputs  []InputAnswer                   // not taken by Inputs yet
	asks    map[string]*ask                 // per pending question request
	done    []resolved                      // requests answered here, as one line each
	keyReq  string                          // the approval the number keys answer
	keyAsk  string                          // else the question they pick in
	typeIn  *widget.Editor                  // an Other field a key picked, to focus in the next Layout
	askQs   map[string][]session.Question   // pending questions by request
	what    map[string]string               // pending approvals' commands, for their line
	items   int                             // len(Content) this frame
	diffs   map[string]diffCache            // edits' lines by item id

	more    widget.Clickable
	menu    bool
	pill    widget.Clickable // New output
	seen    string           // what the chat held when the reader was last at its end
	aideBtn widget.Clickable
	aide    bool // Open in aide was clicked, until OpenAide takes it
}

// Answer is an approval answered with its buttons or keys.
type Answer struct {
	Request  string
	Decision string // allow, allow-session, deny
}

// InputAnswer is a question answered on its card: question id to the
// chosen labels or typed text, as proto.ChatAnswer carries.
type InputAnswer struct {
	Request string
	Answers map[string][]string
}

// decisions are what the approval buttons answer, in their order.
var decisions = [3]string{"allow", "allow-session", "deny"}

// decisionLine is how an answered approval's line says the decision.
var decisionLine = map[string]string{"allow": "Allowed", "allow-session": "Allowed for this session", "deny": "Denied"}

// resolved is a request answered here: a line after the item that was
// last when it was answered.
type resolved struct {
	id, line, what string
	at             int // len(Content) when answered
	when           time.Time
}

// ask is a question card's state.
type ask struct {
	pick   []int // per question, the option picked; len(options) is the typed answer, -1 none
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
	segBlock rowSeg = iota // a prompt or a system line
	segHead                // a turn's work log header
	segItem                // an item in an open work log
	segReply               // a turn's reply
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
	opts := 0
	if a := t.asks[t.keyAsk]; a != nil && len(a.opts) > 0 {
		opts = len(a.opts[t.askQuestion(a)]) - 1
	}
	act, ok := actionFor(name, t.keyReq != "", opts)
	if !ok {
		return false
	}
	switch {
	case act.decision != "":
		t.decide(t.keyReq, act.decision)
	case act.submit:
		t.sendAsk(t.keyAsk)
	default:
		a := t.asks[t.keyAsk]
		q := t.askQuestion(a)
		a.pick[q] = act.option
		if act.option == len(a.opts[q])-1 { // Other: type the answer
			t.typeIn = &a.other[q]
		}
	}
	return true
}

// askQuestion is the question the number keys pick in: the first
// without an answer, else the last.
func (t *Transcript) askQuestion(a *ask) int {
	for i, p := range a.pick {
		if p < 0 {
			return i
		}
	}
	return len(a.pick) - 1
}

// init makes a new Transcript's maps. It sets fields one by one, so the
// thumbnails' lock, which a decode may hold, is never overwritten.
func (t *Transcript) init() {
	t.open, t.clicks, t.sels = map[string]bool{}, map[string]*widget.Clickable{}, map[string]*widget.Selectable{}
	t.docs, t.copied = map[string]*reply{}, map[string]time.Time{}
	t.answer, t.sent, t.asks = map[string]*[3]widget.Clickable{}, map[string]bool{}, map[string]*ask{}
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
	t.drawn = append(t.drawn, s)
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
func (t *Transcript) decide(id, decision string) {
	if id == "" || t.sent[id] {
		return
	}
	t.sent[id] = true
	t.answers = append(t.answers, Answer{Request: id, Decision: decision})
	if t.keyReq == id {
		t.keyReq = ""
	}
	t.done = append(t.done, resolved{id: id, line: decisionLine[decision], what: t.what[id], at: t.items, when: t.now})
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
	t.done = append(t.done, resolved{id: id, line: "Answered", what: strings.Join(picked, ", "), at: t.items, when: t.now})
}

// answers are the card's picks by question id, false while a question
// has none.
func (a *ask) answers(qs []session.Question) (map[string][]string, bool) {
	out := map[string][]string{}
	for i, q := range qs {
		if i >= len(a.pick) {
			return nil, false
		}
		switch p := a.pick[i]; {
		case p >= 0 && p < len(q.Options):
			out[q.ID] = []string{q.Options[p].Label}
		case p == len(q.Options) && strings.TrimSpace(a.other[i].Text()) != "":
			out[q.ID] = []string{strings.TrimSpace(a.other[i].Text())}
		default:
			return nil, false
		}
	}
	return out, len(qs) > 0
}

// Layout draws s filling gtx's constraints, with Open in aide in a quiet
// menu when aide is set.
func (t *Transcript) Layout(gtx layout.Context, th *theme.Theme, s *session.Snapshot, aide bool) layout.Dimensions {
	if t.open == nil {
		t.init()
	}
	if t.epoch.IsZero() {
		t.epoch = gtx.Now
	}
	t.now, t.live, t.laid, t.drawn = gtx.Now, false, 0, t.drawn[:0]
	t.settle(gtx, s)
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
			t.menu = false
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
	t.buildRows(gtx, d)

	// It follows the newest row once it overflows, from the end the first
	// time it does; a short one stays at the top.
	follow := t.list.Position.Length > size.Y
	if follow && !t.list.ScrollToEnd {
		t.list.Position.BeforeEnd = false
	}
	t.list.ScrollToEnd = follow
	t.list.Layout(gtx, len(t.rows)+2, func(gtx layout.Context, i int) layout.Dimensions {
		switch i--; {
		case i < 0:
			return layout.Dimensions{Size: image.Pt(size.X, gtx.Dp(theme.SpaceXL))}
		case i >= len(t.rows):
			return layout.Dimensions{Size: image.Pt(size.X, gtx.Dp(theme.SpaceXL))}
		}
		r := &t.rows[i]
		gap := gtx.Dp(r.gap)
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
	})
	t.pickSelection(gtx)
	t.newOutput(gtx, d, s, size)
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
	t.keyReq, t.keyAsk, t.items = "", "", len(s.Content)
	if t.what == nil {
		t.what = map[string]string{}
	}
	for _, a := range s.PendingApprovals {
		pending[a.ID] = true
		t.what[a.ID] = requestWhat(a)
		if !t.sent[a.ID] {
			t.keyReq = a.ID
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
			a = &ask{}
			for _, q := range in.Questions {
				a.pick = append(a.pick, recommended(q))
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
			if bs[i].Clicked(gtx) && pending[id] {
				t.decide(id, decisions[i])
			}
		}
		if !pending[id] {
			delete(t.answer, id)
		}
	}
	for id, a := range t.asks {
		for qi := range a.opts {
			for oi := range a.opts[qi] {
				if a.opts[qi][oi].Clicked(gtx) {
					a.pick[qi] = oi
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
	k := 0
	for _, b := range t.blocks {
		for ; k < len(t.done) && t.done[k].at <= b.lo; k++ {
			t.rows = append(t.rows, trow{res: &t.done[k]})
		}
		if b.kind != blockTurn {
			t.rows = append(t.rows, trow{b: b})
			continue
		}
		run := running(b, b.hi == len(d.s.Content))
		logged := b.hi - b.lo
		if b.reply >= 0 {
			logged--
		}
		if logged > 0 || run {
			open := t.toggled(gtx, logID(d.s, b), logOpen(run, b.failed))
			t.rows = append(t.rows, trow{b: b, seg: segHead, open: open, running: run, last: true})
			for i := b.lo; open && i < b.hi; i++ {
				if i != b.reply {
					t.rows[len(t.rows)-1].last = false
					t.rows = append(t.rows, trow{b: b, seg: segItem, i: i, last: true})
				}
			}
		}
		if b.reply >= 0 {
			t.rows = append(t.rows, trow{b: b, seg: segReply, running: run})
		}
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

// newOutput is the "New output" pill once the reader scrolled up and the
// chat added to it; a click goes back to the end and follows again.
func (t *Transcript) newOutput(gtx layout.Context, d *cdraw, s *session.Snapshot, size image.Point) {
	sig := strconv.Itoa(len(s.Content))
	if n := len(s.Content); n > 0 {
		sig += s.Content[n-1].UpdatedAt + strconv.Itoa(len(s.Content[n-1].Text))
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
		t.list.Position.BeforeEnd = false
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
