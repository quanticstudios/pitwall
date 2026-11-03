package sidebar

// The reply sheet: a small editor anchored under a row, for answering an
// agent's question without opening its tab. Enter sends, Shift+Enter
// adds a line, Escape closes it and keeps the draft for the tab, Up on
// the first line brings back earlier replies. A long paste stands in the
// draft as one block, [Pasted text #1 · 42 lines], and Backspace after it
// takes the whole block. Ctrl+V with an image and no text on the
// clipboard attaches the image as [Image #1].

import (
	"image"
	"image/color"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Reply is a reply sent from a row's reply sheet: the app types it into
// the agent in PaneID, the tab's agent when "", as if the user typed it.
// Images are the paths of attached images on the agent's machine, pasted
// before Text the way a pasted image is.
type Reply struct {
	WorkspaceID, PaneID string
	Text                string
	Images              []string
}

// AttachImage asks the app for the image on the clipboard, saved where
// the agent in PaneID (the tab's agent when "") can read it. The app
// answers with Sidebar.ReplyImage.
type AttachImage struct{ WorkspaceID, PaneID string }

const (
	icReply        = "M20 18v-2a4 4 0 0 0-4-4H4M9 17l-5-5 5-5" // lucide reply
	replyW         = 320                                       // the sheet's width, dp
	replyMinLines  = 2                                         // the field's height in lines, empty
	replyMaxLines  = 8                                         // and before it scrolls
	replyHistMax   = 50                                        // sent replies Up goes back through
	replyDraftsMax = 16                                        // tabs whose closed drafts are kept

	// Adapted from tuios (MIT): internal/composer/composer.go
	// A paste of more lines or characters than these becomes a block.
	collapseLines = 3
	collapseChars = 800
)

// block is a paste or an image in a draft: its token in the text and
// what the token stands for, the pasted text or the image's path.
type block struct {
	token, text string
	image       bool
}

// draft is a reply being written: the editor's text, tokens included,
// and the blocks its tokens name.
type draft struct {
	text   string
	blocks []block
}

func (d draft) empty() bool { return strings.TrimSpace(d.text) == "" }

// next is the number the next block of a kind takes.
func (d draft) next(image bool) int {
	n := 1
	for _, b := range d.blocks {
		if b.image == image {
			n++
		}
	}
	return n
}

// pasteToken is the token of paste n of text: its lines when it has more
// than one, its characters when it is one long line.
// Adapted from tuios (MIT): internal/composer/composer.go
func pasteToken(n int, text string) string {
	lines := strings.Count(strings.TrimRight(text, "\n"), "\n") + 1
	what := strconv.Itoa(lines) + " lines"
	if lines == 1 {
		what = strconv.Itoa(utf8.RuneCountInString(text)) + " chars"
	}
	return "[Pasted text #" + strconv.Itoa(n) + " · " + what + "]"
}

// collapses reports whether a paste of text becomes a block.
func collapses(text string) bool {
	return strings.Count(strings.TrimRight(text, "\n"), "\n")+1 > collapseLines || utf8.RuneCountInString(text) > collapseChars
}

// expand is what d sends: its text with each paste in place of its token,
// and the paths of its images in the order their tokens stand, their
// tokens taken out. A token the user edited is plain text.
func (d draft) expand() (string, []string) {
	text := d.text
	type at struct {
		i    int
		path string
	}
	var found []at
	for _, b := range d.blocks {
		if i := strings.Index(text, b.token); b.image && i >= 0 {
			found = append(found, at{i, b.text})
		}
	}
	slices.SortFunc(found, func(a, b at) int { return a.i - b.i })
	var images []string
	for _, f := range found {
		images = append(images, f.path)
	}
	for _, b := range d.blocks {
		if b.image {
			text = strings.Replace(text, b.token+" ", "", 1)
			text = strings.Replace(text, b.token, "", 1)
		}
	}
	for _, b := range d.blocks {
		if !b.image {
			text = strings.Replace(text, b.token, b.text, 1)
		}
	}
	return text, images
}

// tokenBefore is the start, in runes, of the block token that ends at
// rune caret of d's text, or -1 when none does.
func (d draft) tokenBefore(caret int) int {
	r := []rune(d.text)
	if caret > len(r) {
		return -1
	}
	head := string(r[:caret])
	for _, b := range d.blocks {
		if strings.HasSuffix(head, b.token) {
			return caret - utf8.RuneCountInString(b.token)
		}
	}
	return -1
}

// replyState is the reply sheet, and the drafts and replies it keeps in
// memory for the window's life.
type replyState struct {
	ws, pane string // the tab the sheet is open on, "" when closed, and its agent's pane
	ed       widget.Editor
	blocks   []block // the open draft's
	focus    bool    // give the editor key focus next frame
	laidOut  bool    // the sheet was drawn this frame
	reading  bool    // an image is on its way from the app
	note     string  // a line in place of the key hints
	noteBad  bool    // the note is an error
	at       time.Time
	send     widget.Clickable

	drafts map[string]draft // closed drafts by tab
	order  []string         // drafts' tabs, oldest first
	hist   []draft          // sent replies, oldest first
	back   int              // how far Up went into hist, 0 for the draft
	live   draft            // the draft Up left
}

// cur is the open draft.
func (r *replyState) cur() draft { return draft{text: r.ed.Text(), blocks: r.blocks} }

// load puts d in the editor, the caret at its end.
func (r *replyState) load(d draft) {
	r.ed.SetText(d.text)
	r.ed.SetCaret(r.ed.Len(), r.ed.Len())
	r.blocks = slices.Clone(d.blocks)
}

// keep stores d as tab ws's draft, or forgets it when it is empty.
func (r *replyState) keep(ws string, d draft) {
	r.order = slices.DeleteFunc(r.order, func(o string) bool { return o == ws })
	if d.empty() {
		delete(r.drafts, ws)
		return
	}
	if r.drafts == nil {
		r.drafts = map[string]draft{}
	}
	r.drafts[ws] = d
	r.order = append(r.order, ws)
	for len(r.order) > replyDraftsMax {
		delete(r.drafts, r.order[0])
		r.order = r.order[1:]
	}
}

// remember adds a sent reply to the history.
func (r *replyState) remember(d draft) {
	r.hist = append(r.hist, d)
	if len(r.hist) > replyHistMax {
		r.hist = r.hist[1:]
	}
}

// recall moves through the history, older with up, and reports whether
// it did. Past the newest it brings back the draft Up left.
func (r *replyState) recall(up bool) bool {
	switch {
	case up && r.back < len(r.hist):
		if r.back == 0 {
			r.live = r.cur()
		}
		r.back++
	case !up && r.back > 0:
		r.back--
	default:
		return false
	}
	if r.back == 0 {
		r.load(r.live)
	} else {
		r.load(r.hist[len(r.hist)-r.back])
	}
	return true
}

// paste puts a paste at the caret: a long one as a block.
func (r *replyState) paste(s string) {
	if !collapses(s) {
		r.ed.Insert(s)
		return
	}
	d := r.cur()
	tok := pasteToken(d.next(false), s)
	r.blocks = append(r.blocks, block{token: tok, text: s})
	r.ed.Insert(tok)
}

// attach puts image path at the caret as a block.
func (r *replyState) attach(path string) {
	tok := "[Image #" + strconv.Itoa(r.cur().next(true)) + "]"
	r.blocks = append(r.blocks, block{token: tok, text: path, image: true})
	r.ed.Insert(tok + " ")
}

// setNote shows text in place of the key hints, red when bad.
func (r *replyState) setNote(text string, bad bool) { r.note, r.noteBad = text, bad }

// openReply opens the reply sheet on tab ws, for its agent in pane ("" for
// the tab's), with the draft it kept.
func (s *Sidebar) openReply(ws, pane string) {
	s.closeReply()
	s.closeMenus()
	r := &s.reply
	r.ws, r.pane, r.focus, r.at, r.back = ws, pane, true, s.now, 0
	r.ed.SingleLine, r.ed.Submit = false, true
	r.setNote("", false)
	r.load(r.drafts[ws])
}

// closeReply closes the sheet and keeps its draft for the tab.
func (s *Sidebar) closeReply() {
	r := &s.reply
	if r.ws == "" {
		return
	}
	r.keep(r.ws, r.cur())
	r.ws, r.pane, r.reading = "", "", false
}

// CloseReply closes the reply sheet, keeping its draft for the tab.
func (s *Sidebar) CloseReply() { s.closeReply() }

// Replying reports whether the reply sheet is open, on which tab.
func (s *Sidebar) Replying() string { return s.reply.ws }

// ReplyImage hands the reply sheet the image AttachImage asked for: its
// path on the agent's machine, or why there is none. An answer for a
// sheet that closed since is dropped.
func (s *Sidebar) ReplyImage(ws, path string, err error) {
	r := &s.reply
	if r.ws != ws || !r.reading {
		return
	}
	r.reading = false
	if err != nil {
		r.setNote("Can't attach the image: "+err.Error(), true)
		return
	}
	r.setNote("", false)
	r.attach(path)
}

// sendReply sends the open draft and closes the sheet; an empty one
// stays open and says so.
func (s *Sidebar) sendReply() {
	r := &s.reply
	d := r.cur()
	if r.reading {
		r.setNote("Wait for the image to attach", false)
		return
	}
	text, images := d.expand()
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		r.setNote("Type a reply to send it", false)
		return
	}
	s.events = append(s.events, Reply{WorkspaceID: r.ws, PaneID: r.pane, Text: text, Images: images})
	r.remember(d)
	ws := r.ws
	r.ws, r.pane = "", ""
	r.keep(ws, draft{})
}

// replyEvents takes this frame's keys, pastes and clicks for the sheet,
// before the editor sees them.
func (s *Sidebar) replyEvents(gtx layout.Context) {
	r := &s.reply
	if r.ws == "" {
		return
	}
	ed := &r.ed
	for {
		// A paste: the editor asks the clipboard, and the answer comes
		// here first. No text means an image, as in a pane.
		ev, ok := gtx.Event(transfer.TargetFilter{Target: ed, Type: "application/text"})
		if !ok {
			break
		}
		de, ok := ev.(transfer.DataEvent)
		if !ok {
			continue
		}
		rd := de.Open()
		b, _ := io.ReadAll(rd)
		rd.Close()
		switch {
		case len(b) > 0:
			r.paste(string(b))
		case r.reading:
		default:
			r.reading = true
			r.setNote("Reading the image on the clipboard…", false)
			s.events = append(s.events, AttachImage{WorkspaceID: r.ws, PaneID: r.pane})
		}
	}
	// Up on the first line and Down on the last go through the history;
	// Backspace after a block takes all of it. Each filter is asked only
	// when it acts, so the editor gets the key otherwise.
	for {
		start, end := ed.Selection()
		line, _ := ed.CaretPos()
		var fs []event.Filter
		if line == 0 && start == end {
			fs = append(fs, key.Filter{Focus: ed, Name: key.NameUpArrow})
		}
		if r.back > 0 && start == end && end == ed.Len() {
			fs = append(fs, key.Filter{Focus: ed, Name: key.NameDownArrow})
		}
		tok := -1
		if start == end {
			tok = r.cur().tokenBefore(end)
		}
		if tok >= 0 {
			fs = append(fs, key.Filter{Focus: ed, Name: key.NameDeleteBackward})
		}
		fs = append(fs, key.Filter{Focus: ed, Name: key.NameEscape})
		ev, ok := gtx.Event(fs...)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch e.Name {
		case key.NameUpArrow:
			if !r.recall(true) {
				ed.SetCaret(0, 0)
			}
		case key.NameDownArrow:
			r.recall(false)
		case key.NameDeleteBackward:
			ed.SetCaret(tok, end)
			ed.Insert("")
		case key.NameEscape:
			s.closeReply()
			return
		}
	}
	for {
		ev, ok := ed.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.SubmitEvent:
			s.sendReply()
			if r.ws == "" {
				return
			}
		case widget.ChangeEvent:
			if r.note != "" && !r.reading {
				r.setNote("", false)
			}
		}
	}
	for r.send.Clicked(gtx) {
		s.sendReply()
	}
	if !r.focus && !gtx.Focused(ed) {
		s.closeReply()
	}
}

// replySheet draws the sheet for tab ws, whose agent shows activity a,
// under anchor, the row's rect in the sidebar, which the drawing's
// origin is the top left of.
func (s *Sidebar) replySheet(gtx layout.Context, v *view, ws model.Workspace, a *model.Activity, anchor image.Rectangle) {
	th := v.th
	r := &s.reply
	r.laidOut = true
	if r.focus {
		gtx.Execute(key.FocusCmd{Tag: &r.ed})
		r.focus = false
	}
	s.catcher(gtx)
	w, pad := gtx.Dp(replyW), gtx.Dp(12)
	inner := w - 2*pad
	l1, l2 := rowLines(gtx, th)
	question := ""
	if a != nil && a.State == model.StateAwaitingInput {
		question = a.Detail
	}

	// The field first, to know its height: two lines at least, eight at
	// most, then it scrolls.
	fieldPad := gtx.Dp(8)
	fg := gtx
	fg.Constraints = layout.Constraints{Min: image.Pt(inner-2*fieldPad, replyMinLines*l1), Max: image.Pt(inner-2*fieldPad, replyMaxLines*l1)}
	m := op.Record(gtx.Ops)
	ed := r.ed.Layout(fg, th.Shaper, th.UIFont, th.Sp(theme.Body), material(gtx, th.Fg), material(gtx, theme.Mix(th.SurfaceSecondary, th.Primary, 0.35)))
	edCall := m.Stop()
	fieldH := max(ed.Size.Y, replyMinLines*l1) + 2*fieldPad

	// wrapped is txt in caption size, n lines at most, recorded.
	wrapped := func(txt string, n int, col color.NRGBA) (op.CallOp, int) {
		if txt == "" {
			return op.CallOp{}, 0
		}
		m := op.Record(gtx.Ops)
		g := gtx
		g.Constraints = layout.Constraints{Max: image.Pt(inner, n*l2)}
		h := widget.Label{MaxLines: n, WrapPolicy: text.WrapWords}.Layout(g, th.Shaper, th.UIFont, th.Sp(theme.Caption), txt, material(gtx, col)).Size.Y
		return m.Stop(), h
	}
	qCall, qH := wrapped(question, 3, th.Muted)
	noteCol := th.Readable(th.Muted, th.SurfaceSecondary)
	if r.noteBad {
		noteCol = th.Red
	}
	nCall, nH := wrapped(r.note, 2, noteCol)
	gap := gtx.Dp(8)
	btnH := answerHeight(gtx, th) + gtx.Dp(4)
	h := pad + l1 + gap + fieldH + gap + btnH + pad
	if qH > 0 {
		h += qH + gtx.Dp(4)
	}
	if nH > 0 {
		h += nH + gtx.Dp(6)
	}
	size := image.Pt(w, h)
	at := kit.Place(anchor, size, s.bounds(), kit.BelowStart, gtx.Dp(4), gtx.Dp(8)).Sub(anchor.Min)
	defer op.Offset(at).Push(gtx.Ops).Pop()
	defer s.popIn(gtx, r.at, size)()
	floatingSurface(gtx, th, size)
	s.blockClicks(gtx, size)

	// "Reply to <tab>", beside the agent's mark.
	y := pad
	g := gtx
	g.Constraints = layout.Exact(image.Pt(inner, l1))
	off := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
	items := []item{}
	if p := v.agent[ws.ID]; p != "" {
		items = append(items, item{w: func(gtx layout.Context) layout.Dimensions {
			AgentMark(gtx, p, gtx.Dp(14), th.Fg)
			return layout.Dimensions{Size: image.Pt(gtx.Dp(14), gtx.Dp(14))}
		}})
	}
	items = append(items, item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
		return label(gtx, th, semibold(th.UIFont), th.Sp(theme.Body), th.Fg, "Reply to "+Title(ws))
	}})
	hrow(g, l1, gtx.Dp(8), items...)
	off.Pop()
	y += l1
	if qH > 0 {
		y += gtx.Dp(4)
		off := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
		qCall.Add(gtx.Ops)
		off.Pop()
		y += qH
	}
	y += gap

	// The field: the window's background, ringed in the accent.
	field := image.Rect(pad, y, pad+inner, y+fieldH)
	rr := gtx.Dp(6)
	paint.FillShape(gtx.Ops, th.Bg, clip.UniformRRect(field, rr).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, theme.Mix(th.Bg, th.Primary, 0.6), clip.Stroke{Path: clip.UniformRRect(field, rr).Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
	off = op.Offset(field.Min.Add(image.Pt(fieldPad, fieldPad))).Push(gtx.Ops)
	if r.ed.Len() == 0 {
		lg := gtx
		lg.Constraints = layout.Exact(image.Pt(inner-2*fieldPad, l1))
		hrow(lg, l1, 0, item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Body), th.Readable(th.TextQuiet, th.Bg), "Type a reply")
		}})
	}
	edCall.Add(gtx.Ops)
	off.Pop()
	y += fieldH
	if nH > 0 {
		// What went wrong, or what is on its way, under the field.
		y += gtx.Dp(6)
		off := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
		nCall.Add(gtx.Ops)
		off.Pop()
		y += nH
	}
	y += gap

	// The keys, and Send.
	g = gtx
	g.Constraints = layout.Exact(image.Pt(inner, btnH))
	off = op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
	hrow(g, btnH, gtx.Dp(8),
		item{shrink: true, w: func(gtx layout.Context) layout.Dimensions {
			return label(gtx, th, th.UIFont, th.Sp(theme.Caption), th.Readable(th.TextQuiet, th.SurfaceSecondary), "Enter sends · Shift+Enter new line · Esc keeps")
		}},
		item{right: true, w: pillButton(&r.send, th, th.SurfaceSecondary, answerHeight(gtx, th), "Send", th.Primary, false)},
	)
	off.Pop()
}

// replyable reports whether tab id's row, with activity a, shows Reply:
// its agent asks a question. A shell's notice and a tab whose agents show
// as sub-rows have none.
func (s *Sidebar) replyable(v *view, id string, ghost bool, a *model.Activity) bool {
	return !ghost && a != nil && a.State == model.StateAwaitingInput && v.agent[id] != "" && len(s.shownSubs(v, id)) == 0
}

// replyButton draws a question's Reply, a yellow pill like the state's.
func replyButton(gtx layout.Context, th *theme.Theme, c *widget.Clickable, base color.NRGBA) layout.Dimensions {
	return pillButton(c, th, base, answerHeight(gtx, th), "Reply", th.Yellow, false)(gtx)
}
