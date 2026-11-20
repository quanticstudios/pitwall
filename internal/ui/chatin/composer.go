package chatin

import (
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"io"
	"math"
	"slices"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/anim"
	"github.com/quanticstudios/pitwall/internal/ui/kit"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Composer is a desktop chat's send box: a card under the transcript with
// the draft, its pasted images, and a row with the permission mode, the
// model and effort, the context meter and Send or Stop. Enter sends, or
// queues while Claude works, Ctrl+Enter sends into the running turn,
// Shift+Enter adds a line, Shift+Tab cycles the mode, Escape stops the
// turn. Keep one per chat and its draft survives tab switches.
type Composer struct {
	ed    widget.Editor
	draft Draft

	sendBtn, stopBtn  widget.Clickable // the main button, and the quiet Stop beside Queue
	modeBtn, modelBtn widget.Clickable
	effortBtn         widget.Clickable
	meterBtn          widget.Clickable          // the context meter, for its hover card
	gone              map[string]bool           // queued messages removed here that the chat still lists
	rm                map[int]*widget.Clickable // per image, its remove x
	hov               map[int]*gesture.Hover    // per image, the pointer on its thumbnail
	side              int                       // a thumbnail's side in pixels, as the last frame drew it
	items             map[string]*widget.Clickable
	menu              bool                                  // a popover is open
	which             *widget.Clickable                     // the control whose popover is open or fading out
	popT              anim.Value                            // its fade and scale in, 0 to 1
	scrim, pop        int                                   // the popover's click-outside and own tags
	anchors           map[*widget.Clickable]image.Rectangle // each picker chip's rect in the card
	barY              int                                   // the control row's top in the card
	cardSize          image.Point
	hover             map[*widget.Clickable]time.Time
	tipSeen           time.Time // the last frame a tooltip showed

	reading int            // clipboard or dropped images on their way
	reads   chan imageRead // their results, read on the UI goroutine
	note    string         // why the last image or send failed
	noteAt  string         // the text when an image's note went up: taking its token out is no edit that clears it
	last    *sent          // the last send, until it is answered

	mode, model, effort held
	bypass              bool // the chat has run in bypass permissions, so Shift+Tab may go back to it
	out                 []any
	inited              bool
}

// Env is what a chat gives its composer each frame.
type Env struct {
	Chat  string
	Snap  *session.Snapshot
	Width unit.Dp // the column the card is centered in
	Focus bool    // take key focus: nothing else in the window holds it
	// Images is whether the daemon takes image attachments. ReadImage
	// reads the clipboard's image off the UI goroutine, and Invalidate
	// redraws the window once it is in.
	Images     bool
	ReadImage  func(context.Context) ([]byte, error)
	Invalidate func()
	// Key, when set, offers a digit typed into the empty box, or Enter on
	// it, to the transcript's pending request; true when it took the key.
	Key func(name string) bool
	// Now is whether the daemon takes ChatSend How "now" (proto.ChatNowLevel);
	// Ctrl+Enter sends "steer" to one that does not.
	Now bool
}

type imageRead struct {
	n    int // the image's number in the draft
	data []byte
	err  error
}

// sent is a send kept for a moment, to bring back if the daemon refuses
// it.
type sent struct {
	text  string
	draft Draft
	at    time.Time
}

// held is a pick the composer shows before the chat reports it: the
// value, the chat's value when picked, and how long it holds.
type held struct {
	v, from string
	until   time.Time
}

const holdFor = 5 * time.Second

func (h *held) show(cur string, now time.Time) string {
	if h.v != "" && (cur != h.from || now.After(h.until)) {
		*h = held{}
	}
	if h.v != "" {
		return h.v
	}
	return cur
}

// maxImages is the daemon's limit of attachments to one message.
const maxImages = 8

// Messages returns the proto messages to send since the last call:
// ChatSend, ChatInterrupt and ChatSet.
func (c *Composer) Messages() []any {
	m := c.out
	c.out = nil
	return m
}

// Text is the draft's text, tokens included.
func (c *Composer) Text() string { return unglue(c.ed.Text()) }

// SetText replaces the draft's text, the caret at its end.
func (c *Composer) SetText(s string) {
	c.ed.SetText(glue(s))
	c.ed.SetCaret(c.ed.Len(), c.ed.Len())
	c.draft.Sync(s)
}

// Images is the draft's images in token order.
func (c *Composer) Images() []Image { return c.draft.Ordered(c.Text()) }

// Slash is the command word the draft starts with while the caret is in
// it, for a slash-command menu.
func (c *Composer) Slash() (string, bool) {
	_, end := c.ed.Selection()
	return Slash(c.Text(), end)
}

// Result is the daemon's answer to one of the window's Chat* requests:
// the request, and why it was refused, "" when it went through.
type Result struct {
	Req     any
	Message string
}

// Result takes the daemon's answer to a request at now. A refusal shows
// its message. A ChatSend refused within moments, with the box still
// empty, comes back into the box, and a refused queue removal brings its
// row back.
func (c *Composer) Result(r Result, now time.Time) {
	if r.Message != "" {
		c.note = r.Message
	}
	switch m := r.Req.(type) {
	case proto.ChatSend:
		if l := c.last; r.Message != "" && l != nil && now.Sub(l.at) < holdFor && c.ed.Len() == 0 && len(c.draft.Images) == 0 {
			c.draft = l.draft
			c.SetText(l.text)
		}
		c.last = nil
	case proto.ChatQueue:
		if r.Message != "" {
			delete(c.gone, m.ID)
		}
	}
}

// AddImage puts picture b at the caret as an [Image #N] token, with its
// chip above the text, or notes why it can't.
func (c *Composer) AddImage(b []byte) {
	if len(c.draft.Images) >= maxImages {
		c.note = "A message takes 8 images at most"
		return
	}
	c.ed.Insert(glue(c.draft.Add(Image{})) + " ")
	c.fill(c.draft.last, b, nil)
}

// fill gives pending image n its picture, or takes it and its token out
// with a note saying why. An image whose token went meanwhile is dropped.
func (c *Composer) fill(n int, b []byte, err error) {
	i := slices.IndexFunc(c.draft.Images, func(im Image) bool { return im.N == n })
	if i < 0 {
		return
	}
	var data []byte
	var mime string
	var thumb *image.NRGBA
	if err == nil {
		data, mime, err = Fit(b)
	}
	side := c.thumbSide()
	if err == nil {
		thumb, err = Cover(data, side)
	}
	if err != nil {
		c.RemoveImage(n)
		c.note, c.noteAt = "Can't attach the image: "+err.Error(), c.Text()
		return
	}
	c.note = ""
	c.draft.Images[i] = Image{N: n, Data: data, Mime: mime, Thumb: paint.NewImageOp(thumb), side: side}
}

// waiting reports whether an image in the draft is still on its way.
func (c *Composer) waiting() bool {
	return slices.ContainsFunc(c.draft.Images, func(im Image) bool { return im.Data == nil })
}

// Reading puts a token for an image at the caret now and reads the
// image off the UI goroutine; a later frame fills it in. Without
// env.Images it only notes that the daemon takes no images.
func (c *Composer) Reading(env Env, read func(context.Context) ([]byte, error)) {
	if !env.Images {
		c.note = "Update the daemon to send images"
		return
	}
	if c.reads == nil {
		c.reads = make(chan imageRead, maxImages)
	}
	if len(c.draft.Images) >= maxImages || c.reading >= maxImages {
		c.note = "A message takes 8 images at most"
		return
	}
	c.ed.Insert(glue(c.draft.Add(Image{})) + " ")
	n := c.draft.last
	c.reading++
	inval := env.Invalidate
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b, err := read(ctx)
		c.reads <- imageRead{n, b, err}
		if inval != nil {
			inval()
		}
	}()
}

// RemoveImage drops image n and its token from the draft.
func (c *Composer) RemoveImage(n int) {
	if s, e := TokenSpan(c.Text(), n); s >= 0 {
		c.ed.SetCaret(s, e)
		c.ed.Insert("")
	}
	c.draft.Images = slices.DeleteFunc(c.draft.Images, func(im Image) bool { return im.N == n })
}

// Button is what the composer's main button shows and does.
type Button uint8

const (
	ButtonSendOff Button = iota // idle and empty: a dim arrow that does nothing
	ButtonSend                  // idle with a draft: the arrow, which sends
	ButtonStop                  // working and empty: the square, which stops the turn
	ButtonQueue                 // working with a draft: Queue, with a quiet Stop beside it
)

// ButtonOf is the main button for a chat with s and a draft or none.
// A pending request counts as working: a message then queues.
func ButtonOf(s *session.Snapshot, draft bool) Button {
	working := s.Busy() || Pending(s)
	switch {
	case working && draft:
		return ButtonQueue
	case working:
		return ButtonStop
	case draft:
		return ButtonSend
	}
	return ButtonSendOff
}

func (c *Composer) hasDraft() bool {
	return strings.TrimSpace(c.Text()) != "" || len(c.draft.Images) > 0
}

// PullBack puts queued message q back in the box, after any draft, its
// images renumbered into the draft.
func (c *Composer) PullBack(q session.QueuedInput) {
	text := q.Text
	type swap struct{ mark, tok string }
	var swaps []swap
	for i, a := range q.Attachments {
		data, err := base64.StdEncoding.DecodeString(a.Data)
		if a.Kind != "image" || err != nil {
			continue
		}
		thumb, err := Cover(data, c.thumbSide())
		if err != nil {
			continue
		}
		tok := c.draft.Add(Image{Data: data, Mime: a.MimeType, Thumb: paint.NewImageOp(thumb), side: c.thumbSide()})
		old := ""
		if n, ok := strings.CutPrefix(a.ID, "image-"); ok {
			old = "[Image #" + n + "]"
		}
		// why: through a mark, so #1 renumbered to #2 is not then taken for the old #2.
		mark := "\x00" + itoa(i) + "\x00"
		if old != "" && strings.Contains(text, old) {
			text = strings.Replace(text, old, mark, 1)
		} else {
			text = strings.TrimRight(text, " ") + " " + mark
		}
		swaps = append(swaps, swap{mark, tok})
	}
	for _, sw := range swaps {
		text = strings.Replace(text, sw.mark, sw.tok, 1)
	}
	if cur := c.Text(); strings.TrimSpace(cur) != "" {
		text = strings.TrimRight(cur, "\n") + "\n" + text
	}
	c.SetText(text)
}

// toggle opens b's popover, or closes it when it is the one open.
func (c *Composer) toggle(now time.Time, b *widget.Clickable) {
	open := !c.menu || c.which != b
	c.which = b
	c.setMenu(now, open)
}

// CloseMenu closes an open popover at once, as when the composer is no
// longer drawn: its tab or pane went out of view.
func (c *Composer) CloseMenu() {
	if c.menu {
		c.menu = false
		c.popT.Set(time.Now(), 0, 0)
	}
}

// setMenu opens or closes the popover: 150 ms in, 100 ms out.
func (c *Composer) setMenu(now time.Time, open bool) {
	c.menu = open
	if open {
		c.popT.Set(now, 1, anim.Dialog)
	} else {
		c.popT.Set(now, 0, anim.Fade)
	}
}

// submit sends the draft, or queues it behind the running turn; now sends
// it into the running turn instead, as Ctrl+Enter does.
func (c *Composer) submit(env Env, at time.Time, now bool) {
	text := c.Text()
	imgs := c.draft.Ordered(text)
	if strings.TrimSpace(text) == "" && len(imgs) == 0 {
		return
	}
	if c.waiting() {
		c.note = "Wait for the image to attach"
		return
	}
	m := proto.ChatSend{Chat: env.Chat, Text: text, Attachments: Attachments(imgs)}
	if env.Snap.Busy() || Pending(env.Snap) {
		m.How = "queue"
		if now && env.Now {
			m.How = "now"
		} else if now {
			m.How = "steer"
		}
	}
	c.out = append(c.out, m)
	c.last = &sent{text: text, draft: Draft{Images: imgs, last: c.draft.last}, at: at}
	c.note = ""
	c.ed.SetText("")
	c.draft.Clear()
}

// cycleMode moves the mode on from what the chat shows.
func (c *Composer) cycleMode(env Env, now time.Time) {
	cur := ModeOf(env.Snap)
	next := NextMode(c.mode.show(cur, now), c.bypass)
	c.mode = held{next, cur, now.Add(holdFor)}
	c.out = append(c.out, proto.ChatSet{Chat: env.Chat, Mode: next})
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func (c *Composer) click(key string) *widget.Clickable {
	if c.items == nil {
		c.items = map[string]*widget.Clickable{}
	}
	b := c.items[key]
	if b == nil {
		b = new(widget.Clickable)
		c.items[key] = b
	}
	return b
}

// events takes this frame's keys, pastes and clicks, before the editor
// sees them.
func (c *Composer) events(gtx layout.Context, env Env) {
	ed := &c.ed
	for {
		select {
		case r := <-c.reads:
			c.reading--
			c.fill(r.n, r.data, r.err)
			continue
		default:
		}
		break
	}
	for {
		// A paste: text goes in as it is; no text means an image, as in a pane.
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
			ed.Insert(string(b))
		case env.ReadImage != nil:
			c.Reading(env, env.ReadImage)
		}
	}
	for {
		start, end := ed.Selection()
		fs := []event.Filter{
			key.Filter{Focus: ed, Name: key.NameEscape},
			key.Filter{Focus: ed, Name: key.NameTab, Required: key.ModShift},
			key.Filter{Focus: ed, Name: key.NameReturn, Required: key.ModShortcut},
			key.Filter{Focus: ed, Name: key.NameEnter, Required: key.ModShortcut},
		}
		// A token right before the caret goes whole on Backspace, and on a
		// word delete with only spaces after it.
		tok, word := -1, -1
		if t := []rune(c.Text()); start == end && end <= len(t) {
			tok = c.draft.TokenBefore(string(t), end)
			k := end
			for k > 0 && t[k-1] == ' ' {
				k--
			}
			word = c.draft.TokenBefore(string(t), k)
		}
		if tok >= 0 {
			fs = append(fs, key.Filter{Focus: ed, Name: key.NameDeleteBackward})
		}
		if word >= 0 {
			fs = append(fs, key.Filter{Focus: ed, Name: key.NameDeleteBackward, Required: key.ModCtrl},
				key.Filter{Focus: ed, Name: key.NameDeleteBackward, Required: key.ModAlt})
		}
		ev, ok := gtx.Event(fs...)
		if !ok {
			break
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		switch e.Name {
		case key.NameEscape:
			if c.menu {
				c.setMenu(gtx.Now, false)
			} else if env.Snap.Busy() {
				c.out = append(c.out, proto.ChatInterrupt{Chat: env.Chat})
			}
		case key.NameTab:
			c.cycleMode(env, gtx.Now)
		case key.NameReturn, key.NameEnter:
			c.submit(env, gtx.Now, true)
		case key.NameDeleteBackward:
			if e.Modifiers != 0 {
				tok = word
			}
			ed.SetCaret(tok, end)
			ed.Insert("")
			c.draft.Sync(c.Text())
		}
	}
	empty := ed.Len() == 0 && len(c.draft.Images) == 0
	for {
		ev, ok := ed.Update(gtx)
		if !ok {
			break
		}
		switch ev.(type) {
		case widget.SubmitEvent:
			// A digit typed in the same frame goes first: 1 then Enter picks,
			// then submits, in the order they were pressed.
			if t := c.Text(); empty && env.Key != nil && len(t) == 1 && t >= "1" && t <= "9" && env.Key(t) {
				ed.SetText("")
			}
			if empty && env.Key != nil && env.Key(string(key.NameReturn)) {
				break
			}
			c.submit(env, gtx.Now, false)
		case widget.ChangeEvent:
			c.draft.Sync(c.Text())
			if !c.waiting() && c.Text() != c.noteAt {
				c.note, c.noteAt = "", ""
			}
		}
	}
	if t := c.Text(); empty && env.Key != nil && len(t) == 1 && t >= "1" && t <= "9" && env.Key(t) {
		ed.SetText("")
	}
	for n, b := range c.rm {
		if b.Clicked(gtx) {
			c.RemoveImage(n)
		}
		if !slices.ContainsFunc(c.draft.Images, func(im Image) bool { return im.N == n }) {
			delete(c.rm, n)
		}
	}
	if c.sendBtn.Clicked(gtx) {
		switch ButtonOf(env.Snap, c.hasDraft()) {
		case ButtonStop:
			c.out = append(c.out, proto.ChatInterrupt{Chat: env.Chat})
		case ButtonSend, ButtonQueue:
			c.submit(env, gtx.Now, false)
		}
		gtx.Execute(key.FocusCmd{Tag: ed})
	}
	if c.stopBtn.Clicked(gtx) {
		c.out = append(c.out, proto.ChatInterrupt{Chat: env.Chat})
		gtx.Execute(key.FocusCmd{Tag: ed})
	}
	for id := range c.gone {
		if !slices.ContainsFunc(env.Snap.QueuedInputs, func(q session.QueuedInput) bool { return q.ID == id }) {
			delete(c.gone, id)
		}
	}
	for _, q := range env.Snap.QueuedInputs {
		edit, rm := c.click("qedit:"+q.ID).Clicked(gtx), c.click("qrm:"+q.ID).Clicked(gtx)
		if (!edit && !rm) || c.gone[q.ID] {
			continue
		}
		if c.gone == nil {
			c.gone = map[string]bool{}
		}
		c.gone[q.ID] = true
		c.out = append(c.out, proto.ChatQueue{Chat: env.Chat, Action: "remove", ID: q.ID})
		if edit {
			c.PullBack(q)
			gtx.Execute(key.FocusCmd{Tag: ed})
		}
	}
	if c.modeBtn.Clicked(gtx) {
		c.toggle(gtx.Now, &c.modeBtn)
	}
	if c.modelBtn.Clicked(gtx) {
		c.toggle(gtx.Now, &c.modelBtn)
	}
	if c.effortBtn.Clicked(gtx) {
		c.toggle(gtx.Now, &c.effortBtn)
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &c.scrim, Kinds: pointer.Press})
		if !ok {
			break
		}
		// why: a press on the open popover's own chip is left to its click, which toggles it shut.
		if e, ok := ev.(pointer.Event); ok && !e.Position.Round().In(c.anchors[c.which]) {
			c.setMenu(gtx.Now, false)
		}
	}
	if c.menu && c.which == &c.modeBtn {
		cur := ModeOf(env.Snap)
		for _, m := range ModeItems(c.bypass) {
			if c.click("mode:" + m).Clicked(gtx) {
				c.mode = held{m, cur, gtx.Now.Add(holdFor)}
				c.out = append(c.out, proto.ChatSet{Chat: env.Chat, Mode: m})
				c.setMenu(gtx.Now, false)
				gtx.Execute(key.FocusCmd{Tag: ed})
			}
		}
	}
	if c.menu && c.which == &c.modelBtn {
		for _, m := range remote.Models {
			if c.click("model:" + m).Clicked(gtx) {
				c.model = held{m, deref(env.Snap.Model), gtx.Now.Add(holdFor)}
				c.out = append(c.out, proto.ChatSet{Chat: env.Chat, Model: m})
				c.setMenu(gtx.Now, false)
			}
		}
	}
	if c.menu && c.which == &c.effortBtn {
		for _, e := range remote.Efforts {
			if c.click("effort:" + e).Clicked(gtx) {
				c.effort = held{e, deref(env.Snap.Effort), gtx.Now.Add(holdFor)}
				c.out = append(c.out, proto.ChatSet{Chat: env.Chat, Effort: e})
				c.setMenu(gtx.Now, false)
			}
		}
	}
}

// Layout handles the frame's input and draws the card centered in
// gtx's width, at most env.Width wide, with the window's background
// around it.
func (c *Composer) Layout(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	if !c.inited {
		c.inited = true
		c.ed.Submit = true
		c.popT.Set(gtx.Now, 0, 0)
	}
	c.ed.LineHeight, c.ed.LineHeightScale = th.Sp(theme.Large)*1.5, 1 // follows the UI size
	if c.menu && !env.Focus {                                         // the keys went to another pane, a dialog or a peek
		c.setMenu(gtx.Now, false)
	}
	if env.Focus && !gtx.Focused(&c.ed) && !c.menu {
		gtx.Execute(key.FocusCmd{Tag: &c.ed})
	}
	c.events(gtx, env)
	full := gtx.Constraints.Max.X
	w := max(0, min(full-2*gtx.Dp(theme.SpaceXL), gtx.Dp(env.Width)))
	cg := gtx
	cg.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Constraints.Max.Y)}
	m := op.Record(gtx.Ops)
	queued := c.queued(cg, th, env)
	qcall := m.Stop()
	m = op.Record(gtx.Ops)
	card := c.card(cg, th, env)
	call := m.Stop()
	top := gtx.Dp(theme.SpaceM)
	if queued.Size.Y > 0 {
		top += queued.Size.Y + gtx.Dp(theme.SpaceS)
	}
	h := top + card.Size.Y + gtx.Dp(theme.SpaceL)
	paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: image.Pt(full, h)}.Op())
	qoff := op.Offset(image.Pt((full-w)/2, gtx.Dp(theme.SpaceM))).Push(gtx.Ops)
	qcall.Add(gtx.Ops)
	qoff.Pop()
	off := op.Offset(image.Pt((full-w)/2, top)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	if t := c.popT.Get(gtx); c.menu || t > 0 {
		c.popover(gtx, th, env, t)
	}
	off.Pop()
	return layout.Dimensions{Size: image.Pt(full, h)}
}

// card is the rounded box: chips, the editor, the control row.
func (c *Composer) card(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	m := op.Record(gtx.Ops)
	c.barY = 0
	d := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { d := c.chips(gtx, th); c.barY += d.Size.Y; return d }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { d := c.field(gtx, th, env); c.barY += d.Size.Y; return d }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return c.bar(gtx, th, env) }),
	)
	c.cardSize = d.Size
	call := m.Stop()
	r := gtx.Dp(theme.RadiusCard)
	rect := image.Rectangle{Max: d.Size}
	ring := th.BorderStrong
	if gtx.Focused(&c.ed) {
		ring = theme.Mix(th.Surface, th.Primary, 0.5)
	}
	paint.FillShape(gtx.Ops, ring, clip.UniformRRect(rect, r).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, th.Surface, clip.UniformRRect(rect.Inset(gtx.Dp(1)), r-gtx.Dp(1)).Op(gtx.Ops))
	call.Add(gtx.Ops)
	if c.note != "" {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return d }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Top: theme.SpaceS, Left: theme.SpaceL}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return text(gtx, th, th.UIFont, th.Sp(theme.Small), th.Readable(th.Red, th.Bg), c.note)
				})
			}),
		)
	}
	return d
}

// thumbDp is a draft thumbnail's side.
const thumbDp unit.Dp = 64

// thumbSide is a thumbnail's side in pixels at the last frame's scale.
func (c *Composer) thumbSide() int {
	if c.side == 0 {
		return 2 * int(thumbDp)
	}
	return c.side
}

// chips are the draft's images as square thumbnails, each with a remove
// x while the pointer is on it; an image on its way is a blank square.
// The [Image #N] token in the text names each one.
func (c *Composer) chips(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	c.side = gtx.Dp(thumbDp)
	if len(c.draft.Images) == 0 {
		return layout.Dimensions{}
	}
	if c.rm == nil {
		c.rm, c.hov = map[int]*widget.Clickable{}, map[int]*gesture.Hover{}
	}
	var kids []layout.FlexChild
	chip := func(im *Image) layout.FlexChild {
		return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: theme.SpaceS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return c.chip(gtx, th, im)
			})
		})
	}
	for i := range c.draft.Images {
		kids = append(kids, chip(&c.draft.Images[i]))
	}
	return layout.Inset{Top: 10, Left: theme.SpaceL, Right: theme.SpaceL}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{}.Layout(gtx, kids...)
	})
}

func (c *Composer) chip(gtx layout.Context, th *theme.Theme, im *Image) layout.Dimensions {
	side := c.side
	if im.Data != nil && im.side != side { // the UI size or the screen's scale changed
		if t, err := Cover(im.Data, side); err == nil {
			im.Thumb, im.side = paint.NewImageOp(t), side
		}
	}
	r := gtx.Dp(theme.RadiusPopover)
	rect := image.Rectangle{Max: image.Pt(side, side)}
	shape := clip.UniformRRect(rect, r)
	h := c.hov[im.N]
	if h == nil {
		h = new(gesture.Hover)
		c.hov[im.N] = h
	}
	cl := shape.Push(gtx.Ops)
	h.Add(gtx.Ops)
	if im.Data != nil && im.side == side {
		// why: one thumbnail pixel to one screen pixel, so nothing rescales it.
		im.Thumb.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
	} else {
		paint.Fill(gtx.Ops, th.SurfaceElevated)
	}
	cl.Pop()
	paint.FillShape(gtx.Ops, th.BorderSubtle, clip.Stroke{Path: shape.Path(gtx.Ops), Width: float32(gtx.Dp(1))}.Op())
	b := c.rm[im.N]
	if b == nil {
		b = new(widget.Clickable)
		c.rm[im.N] = b
	}
	if !h.Update(gtx.Source) && !b.Hovered() && !gtx.Focused(b) {
		return layout.Dimensions{Size: rect.Max}
	}
	// An 18dp x, 3 in from the corner, in a 28dp hit target.
	xs, hit := gtx.Dp(18), gtx.Dp(28)
	in := (hit - xs) / 2
	at := op.Offset(image.Pt(side-gtx.Dp(3)-xs-in, gtx.Dp(3)-in)).Push(gtx.Ops)
	xg := gtx
	xg.Constraints = layout.Exact(image.Pt(hit, hit))
	b.Layout(xg, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp("Remove image #" + itoa(im.N)).Add(gtx.Ops)
		fill := withAlpha(th.Bg, 0xd9)
		switch {
		case b.Pressed():
			fill = th.Pressed
		case b.Hovered():
			fill = th.Hover
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rect(in, in, in+xs, in+xs), xs/2).Op(gtx.Ops))
		is := gtx.Dp(12)
		o := op.Offset(image.Pt((hit-is)/2, (hit-is)/2)).Push(gtx.Ops)
		sidebar.Icon(gtx, "x", is, th.Fg)
		o.Pop()
		return layout.Dimensions{Size: image.Pt(hit, hit)}
	})
	at.Pop()
	return layout.Dimensions{Size: rect.Max}
}

// field is the editor: one line at rest, growing to ten, then scrolling;
// the placeholder while empty, and each image token drawn as a chip.
func (c *Composer) field(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	return layout.Inset{Top: theme.SpaceM, Bottom: theme.SpaceXS, Left: theme.SpaceL, Right: theme.SpaceL}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		line := gtx.Sp(c.ed.LineHeight)
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, line)
		gtx.Constraints.Max.Y = 10 * line
		size := th.Sp(theme.Large)
		if c.ed.Len() == 0 {
			lg := gtx
			lg.Constraints.Min = image.Point{}
			text(lg, th, th.UIFont, size, th.Muted, Placeholder(env.Snap))
		}
		d := c.ed.Layout(gtx, th.Shaper, th.UIFont, size, colorOp(gtx, th.Fg), colorOp(gtx, theme.Mix(th.Surface, th.Primary, 0.35)))
		c.tokens(gtx, th)
		return d
	})
}

// tokens covers each image token in the editor with a small blue chip
// whose label, at the text's size, sits on the line's baseline.
func (c *Composer) tokens(gtx layout.Context, th *theme.Theme) {
	txt := c.Text()
	bg, fg := th.Chip(th.Blue, th.Surface)
	size := th.Sp(theme.Large)
	var regions []widget.Region
	for _, im := range c.draft.Images {
		s, _ := TokenSpan(txt, im.N)
		if s < 0 {
			continue
		}
		e := s + len([]rune(Token(im.N)))
		// why: the no-break space keeps a token on one line; should it still
		// split, on a line narrower than the token, the chip covers its start.
		regions = c.ed.Regions(s, e, regions[:0])
		for _, rg := range regions[:min(1, len(regions))] {
			lm := op.Record(gtx.Ops)
			ld := text(gtx, th, th.UIFont, size, fg, "Image #"+itoa(im.N))
			label := lm.Stop()
			base := rg.Bounds.Max.Y - rg.Baseline // the line's baseline
			ly := base - (ld.Size.Y - ld.Baseline)
			em, gap, padX, padY := gtx.Sp(size), gtx.Dp(theme.SpaceXS), gtx.Dp(6), gtx.Dp(theme.Space2XS)
			w := ld.Size.X + 2*padX
			withIcon := w+em+gap <= rg.Bounds.Dx()+2*padX
			if withIcon {
				w += em + gap
			}
			x := rg.Bounds.Min.X + (rg.Bounds.Dx()-w)/2
			r := image.Rect(x, ly-padY, x+w, ly+ld.Size.Y+padY)
			paint.FillShape(gtx.Ops, bg, clip.UniformRRect(r, gtx.Dp(5)).Op(gtx.Ops))
			lx := x + padX
			if withIcon {
				// why: its center on the x-height's middle, about a quarter em above the baseline.
				o := op.Offset(image.Pt(lx, base-em/4-em/2)).Push(gtx.Ops)
				sidebar.Icon(gtx, "image", em, fg)
				o.Pop()
				lx += em + gap
			}
			o := op.Offset(image.Pt(lx, ly)).Push(gtx.Ops)
			label.Add(gtx.Ops)
			o.Pop()
		}
	}
}

// bar is the control row: the mode, the model, then the meter and Send.
func (c *Composer) bar(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	now := gtx.Now
	if ModeOf(env.Snap) == ModeBypass {
		// why: the snapshot doesn't say whether Claude may enter bypass; one it ran in was started with it.
		c.bypass = true
	}
	mode := c.mode.show(ModeOf(env.Snap), now)
	modeCol, modeIcon, modeTip := modeLook(th, mode)
	mdl := c.model.show(deref(env.Snap.Model), now)
	eff := EffortName(c.effort.show(deref(env.Snap.Effort), now))
	effCol := pickFg(th)
	if eff == EffortName("") {
		effCol = th.Muted
	}
	label := "Model"
	if mdl != "" {
		label = ModelName(mdl)
	}
	used, total, metered := Meter(env.Snap.Usage)
	if c.anchors == nil {
		c.anchors = map[*widget.Clickable]image.Rectangle{}
	}
	rowH := gtx.Dp(36)
	x := gtx.Dp(theme.SpaceS)
	// anchor notes chip b, size big, at x along the row, and moves x past it and gap.
	anchor := func(b *widget.Clickable, size image.Point, gap int) {
		y := c.barY + (rowH-size.Y)/2
		c.anchors[b] = image.Rectangle{Min: image.Pt(x, y), Max: image.Pt(x+size.X, y+size.Y)}
		x += size.X + gap
	}
	return layout.Inset{Left: theme.SpaceS, Right: theme.SpaceS, Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Pt(gtx.Constraints.Max.X, rowH)
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d := c.pick(gtx, th, &c.modeBtn, modeIcon, ModeName(mode), modeCol, true)
				c.tip(gtx, th, &c.modeBtn, d.Size, modeTip, false)
				anchor(&c.modeBtn, d.Size, gtx.Dp(theme.SpaceXS))
				return d
			}),
			layout.Rigid(layout.Spacer{Width: theme.SpaceXS}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				d := c.pick(gtx, th, &c.modelBtn, "", label, pickFg(th), true)
				c.tip(gtx, th, &c.modelBtn, d.Size, "Model", false)
				anchor(&c.modelBtn, d.Size, gtx.Dp(theme.SpaceXS))
				return d
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				// shortcut: only Claude's efforts are known (remote.Efforts); the snapshot says nothing of other providers'.
				if env.Snap.Provider != model.ProviderClaude {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					d := c.pick(gtx, th, &c.effortBtn, "", eff, effCol, true)
					c.tip(gtx, th, &c.effortBtn, d.Size, "Effort", false)
					anchor(&c.effortBtn, d.Size, 0)
					return d
				})
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return layout.Dimensions{Size: image.Pt(gtx.Constraints.Min.X, 0)}
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if !metered {
					return layout.Dimensions{}
				}
				return c.meter(gtx, th, used, total)
			}),
			layout.Rigid(layout.Spacer{Width: theme.SpaceXS}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if ButtonOf(env.Snap, c.hasDraft()) != ButtonQueue {
					return layout.Dimensions{}
				}
				return layout.Inset{Right: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					d := c.ghostStop(gtx, th)
					c.tip(gtx, th, &c.stopBtn, d.Size, "Stop (Esc)", true)
					return d
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				b := ButtonOf(env.Snap, c.hasDraft())
				d := c.send(gtx, th, b)
				tip := map[Button]string{ButtonSendOff: "Send (Enter)", ButtonSend: "Send (Enter)", ButtonStop: "Stop (Esc)", ButtonQueue: "Queue: sends when Claude finishes (Enter). Send now: Ctrl+Enter"}[b]
				c.tip(gtx, th, &c.sendBtn, d.Size, tip, true)
				return d
			}),
		)
	})
}

func pickFg(th *theme.Theme) color.NRGBA { return theme.Mix(th.Fg, th.Muted, 0.3) }

// modeLook is the mode chip's color, icon and tooltip for mode m. Bypass
// is red with a shield, as Claude Code's TUI shows it in red.
func modeLook(th *theme.Theme, m string) (color.NRGBA, string, string) {
	switch m {
	case ModeAccept:
		return th.Purple, "pencil", "Permission mode (Shift+Tab)"
	case ModePlan:
		return th.Green, "map", "Permission mode (Shift+Tab)"
	case ModeBypass:
		return th.Readable(th.Red, th.Surface), "shield", "Bypass permissions: Claude runs every tool without asking (Shift+Tab)"
	}
	return pickFg(th), "circle", "Permission mode (Shift+Tab)"
}

// pick is a 28dp ghost control: an icon, a label, a chevron.
func (c *Composer) pick(gtx layout.Context, th *theme.Theme, b *widget.Clickable, icon, label string, col color.NRGBA, chevron bool) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return b.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp(label).Add(gtx.Ops)
		h, pad := gtx.Dp(28), gtx.Dp(theme.SpaceS)
		m := op.Record(gtx.Ops)
		var ws []layout.Widget
		if icon != "" {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions { return sidebar.Icon(gtx, icon, gtx.Dp(12), col) })
		}
		ws = append(ws, func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, medium(th.UIFont), th.Sp(theme.Small), col, label)
		})
		if chevron {
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				return sidebar.Icon(gtx, "chevron-down", gtx.Dp(12), th.Muted)
			})
		}
		g := gtx
		g.Constraints.Max.X -= 2 * pad
		d := row(g, gtx.Dp(6), h, ws...)
		call := m.Stop()
		size := image.Pt(d.Size.X+2*pad, h)
		fill := theme.Transparent
		switch {
		case b.Pressed():
			fill = th.Pressed
		case b.Hovered() || b == c.which && c.menu:
			fill = th.Hover
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		o := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		return layout.Dimensions{Size: size}
	})
}

// meter is the context meter: a ring that fills with the context used and
// the percentage beside it, yellow from 75% and red near the auto-compact
// point. Under the pointer it shows the tokens, the room left before
// auto-compact and the window.
func (c *Composer) meter(gtx layout.Context, th *theme.Theme, used, total int64) layout.Dimensions {
	col := th.Muted
	switch Level(used, total) {
	case MeterWarning:
		col = th.Readable(th.Yellow, th.Surface)
	case MeterDanger:
		col = th.Readable(th.Red, th.Surface)
	}
	pct := Percent(used, total)
	gtx.Constraints.Min = image.Point{}
	d := c.meterBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp("Context " + itoa(pct) + "% used").Add(gtx.Ops)
		h, pad := gtx.Dp(28), gtx.Dp(theme.SpaceS)
		m := op.Record(gtx.Ops)
		rd := row(gtx, gtx.Dp(6), h,
			func(gtx layout.Context) layout.Dimensions {
				return ring(gtx, gtx.Dp(14), float32(min(used, total))/float32(total), th.BorderStrong, col)
			},
			func(gtx layout.Context) layout.Dimensions {
				// why: tabular figures keep each digit's width, but 8% to 80% adds one; a slot as wide as 100% keeps the ring still.
				f, sz := medium(th.UIFont), th.Sp(theme.Small)
				m := op.Record(gtx.Ops)
				slot := text(gtx, th, f, sz, col, "100%").Size.X
				m.Stop()
				m = op.Record(gtx.Ops)
				d := text(gtx, th, f, sz, col, itoa(pct)+"%")
				call := m.Stop()
				w := max(slot, d.Size.X)
				o := op.Offset(image.Pt(w-d.Size.X, 0)).Push(gtx.Ops)
				call.Add(gtx.Ops)
				o.Pop()
				return layout.Dimensions{Size: image.Pt(w, d.Size.Y), Baseline: d.Baseline}
			})
		call := m.Stop()
		size := image.Pt(rd.Size.X+2*pad, h)
		ghostFill(gtx, th, &c.meterBtn, size)
		o := op.Offset(image.Pt(pad, 0)).Push(gtx.Ops)
		call.Add(gtx.Ops)
		o.Pop()
		return layout.Dimensions{Size: size}
	})
	c.hoverCard(gtx, th, &c.meterBtn, d.Size, true, func(gtx layout.Context) layout.Dimensions {
		left := AutoCompactAt(total) - used
		room := Tokens(left) + " left before auto-compact"
		if left <= 0 {
			room = "At the auto-compact point"
		}
		line := func(f font.Font, size unit.Sp, col color.NRGBA, s string) layout.FlexChild {
			return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Bottom: theme.Space2XS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return text(gtx, th, f, size, col, s)
				})
			})
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			line(medium(th.UIFont), th.Sp(theme.Small), th.Muted, "Context"),
			line(th.UIFont, th.Sp(theme.Body), th.Fg, Tokens(used)+" of "+Tokens(total)+" tokens, "+itoa(pct)+"%"),
			line(th.UIFont, th.Sp(theme.Small), th.Muted, room),
			line(th.UIFont, th.Sp(theme.Small), th.Muted, Tokens(total)+" window"),
		)
	})
	return d
}

// ring is a size by size ring, track colored, with frac of it from the
// top, clockwise, in col.
func ring(gtx layout.Context, size int, frac float32, track, col color.NRGBA) layout.Dimensions {
	w := float32(gtx.Dp(2))
	in := gtx.Dp(1)
	paint.FillShape(gtx.Ops, track, clip.Stroke{Path: clip.Ellipse(image.Rect(in, in, size-in, size-in)).Path(gtx.Ops), Width: w}.Op())
	if frac = min(frac, 0.999); frac > 0 {
		r := float32(size)/2 - float32(in)
		ctr := f32.Pt(float32(size)/2, float32(size)/2)
		var p clip.Path
		p.Begin(gtx.Ops)
		p.MoveTo(f32.Pt(ctr.X, ctr.Y-r))
		p.ArcTo(ctr, ctr, 2*math.Pi*frac)
		paint.FillShape(gtx.Ops, col, clip.Stroke{Path: p.End(), Width: w}.Op())
	}
	return layout.Dimensions{Size: image.Pt(size, size)}
}

// send is the 28dp main button for b: the arrow, dim while the box is
// empty; the square Stop; or Queue.
func (c *Composer) send(gtx layout.Context, th *theme.Theme, b Button) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return c.sendBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		s := gtx.Dp(28)
		rect := image.Rectangle{Max: image.Pt(s, s)}
		fill, fg := th.SurfaceElevated, th.Muted
		switch b {
		case ButtonStop:
			fill, fg = th.Fg, th.Bg
			semantic.LabelOp("Stop").Add(gtx.Ops)
		case ButtonQueue:
			fill, fg = th.Primary, th.OnPrimary
			semantic.LabelOp("Queue").Add(gtx.Ops)
		case ButtonSend:
			fill, fg = th.Primary, th.OnPrimary
			semantic.LabelOp("Send").Add(gtx.Ops)
		default:
			semantic.LabelOp("Send").Add(gtx.Ops)
		}
		switch live := b != ButtonSendOff; {
		case c.sendBtn.Pressed() && live:
			fill = theme.Mix(fill, th.Bg, 0.2)
		case c.sendBtn.Hovered() && live:
			fill = theme.Mix(fill, th.Fg, 0.12)
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect, gtx.Dp(7)).Op(gtx.Ops))
		is := gtx.Dp(14)
		o := op.Offset(image.Pt((s-is)/2, (s-is)/2)).Push(gtx.Ops)
		switch b {
		case ButtonStop:
			stopSquare(gtx, is, fg)
		case ButtonQueue:
			sidebar.Icon(gtx, "list-plus", is, fg)
		default:
			sidebar.Icon(gtx, "arrow-up", is, fg)
		}
		o.Pop()
		return layout.Dimensions{Size: rect.Max}
	})
}

// stopSquare is Stop's 10dp square, centered in a size box.
func stopSquare(gtx layout.Context, size int, col color.NRGBA) {
	q := gtx.Dp(10)
	o := (size - q) / 2
	paint.FillShape(gtx.Ops, col, clip.UniformRRect(image.Rect(o, o, o+q, o+q), gtx.Dp(2)).Op(gtx.Ops))
}

// ghostStop is the quiet 28dp Stop beside Queue.
func (c *Composer) ghostStop(gtx layout.Context, th *theme.Theme) layout.Dimensions {
	gtx.Constraints.Min = image.Point{}
	return c.stopBtn.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.LabelOp("Stop").Add(gtx.Ops)
		s := gtx.Dp(28)
		fill, fg := theme.Transparent, th.Muted
		switch {
		case c.stopBtn.Pressed():
			fill, fg = th.Pressed, th.Fg
		case c.stopBtn.Hovered():
			fill, fg = th.Hover, th.Fg
		}
		paint.FillShape(gtx.Ops, fill, clip.UniformRRect(image.Rectangle{Max: image.Pt(s, s)}, gtx.Dp(theme.RadiusControl)).Op(gtx.Ops))
		is := gtx.Dp(14)
		o := op.Offset(image.Pt((s-is)/2, (s-is)/2)).Push(gtx.Ops)
		stopSquare(gtx, is, fg)
		o.Pop()
		return layout.Dimensions{Size: image.Pt(s, s)}
	})
}

// queued is the messages waiting for the turn to end, above the card: a
// count when there are several, then a row each with the queue icon,
// the first line, Edit and a remove x.
func (c *Composer) queued(gtx layout.Context, th *theme.Theme, env Env) layout.Dimensions {
	var qs []session.QueuedInput
	for _, q := range env.Snap.QueuedInputs {
		if !c.gone[q.ID] {
			qs = append(qs, q)
		}
	}
	if len(qs) == 0 {
		return layout.Dimensions{}
	}
	var kids []layout.FlexChild
	if len(qs) > 1 {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: theme.SpaceM, Bottom: theme.SpaceXS}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return text(gtx, th, medium(th.UIFont), th.Sp(theme.Small), th.Muted, itoa(len(qs))+" queued")
			})
		}))
	}
	for _, q := range qs {
		kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions { return c.queuedRow(gtx, th, q) }))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
}

func (c *Composer) queuedRow(gtx layout.Context, th *theme.Theme, q session.QueuedInput) layout.Dimensions {
	h := gtx.Dp(30)
	w := gtx.Constraints.Max.X
	line, _, _ := strings.Cut(strings.TrimSpace(q.Text), "\n")
	if line == "" && len(q.Attachments) > 0 {
		line = "Image"
	}
	edit, rm := c.click("qedit:"+q.ID), c.click("qrm:"+q.ID)
	// The buttons first, from the right, to know what the text may take.
	m := op.Record(gtx.Ops)
	bg := gtx
	bg.Constraints = layout.Constraints{Max: image.Pt(w, h)}
	bd := row(bg, gtx.Dp(theme.Space2XS), h,
		func(gtx layout.Context) layout.Dimensions { return textButton(gtx, th, edit, "Edit") },
		func(gtx layout.Context) layout.Dimensions {
			d := iconButton(gtx, th, rm, "x", "Remove queued message")
			return d
		})
	bcall := m.Stop()
	o := op.Offset(image.Pt(w-bd.Size.X, 0)).Push(gtx.Ops)
	bcall.Add(gtx.Ops)
	o.Pop()
	tg := gtx
	tg.Constraints = layout.Constraints{Max: image.Pt(max(0, w-bd.Size.X-gtx.Dp(theme.SpaceS)-gtx.Dp(theme.SpaceM)), h)}
	o = op.Offset(image.Pt(gtx.Dp(theme.SpaceM), 0)).Push(gtx.Ops)
	row(tg, gtx.Dp(theme.SpaceS), h,
		func(gtx layout.Context) layout.Dimensions {
			return sidebar.Icon(gtx, "list-plus", gtx.Dp(14), th.Muted)
		},
		func(gtx layout.Context) layout.Dimensions {
			return text(gtx, th, th.UIFont, th.Sp(theme.Body), theme.Mix(th.Fg, th.Bg, 0.2), line)
		})
	o.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// popover is the model or effort picker over the transcript, above its
// control. A press outside closes it. Its origin is the card's top left.
func (c *Composer) popover(gtx layout.Context, th *theme.Theme, env Env, t float32) {
	m := op.Record(gtx.Ops)
	if c.menu {
		big := 1 << 15
		area := clip.Rect{Min: image.Pt(-big, -big), Max: image.Pt(big, big)}.Push(gtx.Ops)
		// why: a press outside closes the popover and still reaches what it lands on, another pane included.
		pass := pointer.PassOp{}.Push(gtx.Ops)
		event.Op(gtx.Ops, &c.scrim)
		pass.Pop()
		area.Pop()
	}
	now := gtx.Now
	curModel := ModelName(c.model.show(deref(env.Snap.Model), now))
	curEffort := c.effort.show(deref(env.Snap.Effort), now)
	curMode := c.mode.show(ModeOf(env.Snap), now)
	pw := gtx.Dp(288)
	switch c.which {
	case &c.effortBtn:
		pw = gtx.Dp(160)
	case &c.modeBtn:
		pw = gtx.Dp(220)
	}
	pg := gtx
	pg.Constraints = layout.Constraints{Min: image.Pt(pw, 0), Max: image.Pt(pw, gtx.Constraints.Max.Y)}
	rec := op.Record(gtx.Ops)
	d := layout.UniformInset(theme.SpaceXS).Layout(pg, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		var kids []layout.FlexChild
		if c.which == &c.modeBtn {
			for _, m := range ModeItems(c.bypass) {
				col, icon, _ := modeLook(th, m)
				if m != ModeBypass {
					col = th.Fg
				}
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return iconMenuRow(gtx, th, c.click("mode:"+m), icon, ModeName(m), col, m == curMode)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
		}
		if c.which == &c.effortBtn {
			for _, e := range remote.Efforts {
				kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return menuRow(gtx, th, c.click("effort:"+e), EffortName(e), e == curEffort)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
		}
		kids = append(kids, layout.Rigid(heading(th, "Model")))
		for _, v := range remote.Models {
			on := v != "default" && ModelName(v) == curModel
			kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return menuRow(gtx, th, c.click("model:"+v), ModelName(v), on)
			}))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, kids...)
	})
	body := rec.Stop()
	// Above its chip, left edges aligned, kept inside the card's width.
	bounds := image.Rectangle{Min: image.Pt(0, -1<<20), Max: c.cardSize}
	p := kit.Place(c.anchors[c.which], d.Size, bounds, kit.AboveStart, gtx.Dp(theme.SpaceXS), 0)
	o := op.Offset(p).Push(gtx.Ops)
	rect := image.Rectangle{Max: d.Size}
	// Fade and scale from 0.97 out of its bottom left corner, by the trigger.
	fade := paint.PushOpacity(gtx.Ops, t)
	sc := 0.97 + 0.03*t
	grow := op.Affine(f32.AffineId().Scale(f32.Pt(0, float32(d.Size.Y)), f32.Pt(sc, sc))).Push(gtx.Ops)
	kit.Surface(gtx, rect, gtx.Dp(theme.RadiusPopover), kit.Floating, th.BorderStrong, th.SurfaceElevated)
	// why: the popover swallows presses so they don't reach the scrim.
	cl := clip.Rect(rect).Push(gtx.Ops)
	event.Op(gtx.Ops, &c.pop)
	cl.Pop()
	body.Add(gtx.Ops)
	grow.Pop()
	fade.Pop()
	o.Pop()
	op.Defer(gtx.Ops, m.Stop())
}
