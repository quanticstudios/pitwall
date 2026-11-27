package chatin

import (
	"encoding/base64"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"gioui.org/io/key"
	"gioui.org/op/paint"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// Sent is a prompt the chat sent, as Up recalls it: the transcript item's
// id, its text with its [Image #N] tokens, its pictures, and Cut when the
// snapshot cut the item (session.Clip), so its whole copy is still to read.
type Sent struct {
	ID          string
	Text        string
	Attachments []session.Attachment
	Cut         bool
}

// Recallable is ps as Up and Down step through them: those with neither
// text nor a picture left out, and a prompt sent again right after itself
// kept once, as its later copy.
func Recallable(ps []Sent) []Sent {
	var out []Sent
	for _, p := range ps {
		if strings.TrimSpace(p.Text) == "" && !slices.ContainsFunc(p.Attachments, func(a session.Attachment) bool { return a.Kind == "image" }) {
			continue
		}
		if n := len(out); n > 0 && out[n-1].Text == p.Text {
			out[n-1] = p
			continue
		}
		out = append(out, p)
	}
	return out
}

// HistoryStep is where Up (back) or Down goes from entry active, the
// prompt the box shows unedited, -1 for none: the entry to show, or -1
// with ok to bring back what the box held. Up starts at the newest, only
// from an empty box; Down past the newest goes back to the box. ok is
// false when the key is not recall's: Up from a draft or from the oldest,
// Down from no recalled prompt.
func HistoryStep(n, active int, empty, back bool) (next int, ok bool) {
	switch {
	case back && active < 0:
		return n - 1, empty && n > 0
	case back:
		return active - 1, active > 0
	case active < 0:
		return 0, false
	case active+1 < n:
		return active + 1, true
	}
	return -1, true
}

// kept is the box set aside while it shows a recalled prompt or a queued
// message, to bring back after.
type kept struct {
	text  string
	draft Draft
}

// recall is the prompt Up put in the box: its id and the text it put
// there, while that text stands unedited, and the box from before.
type recall struct {
	id, text string
	cut      bool // the snapshot cut it: its whole copy replaces it once read
	keep     *kept
}

// queueEdit is the queued message Alt+Up loaded into the box to edit:
// its id, the text it loaded as and its own, and each image's and
// pasted text's attachment id by its token in the box; the box from
// before comes back after.
type queueEdit struct {
	id, text string
	orig     string // the message's own text
	ids      map[string]string
	keep     *kept
}

// Stash is a draft Ctrl+S set aside: its text, its images as a ChatSend
// carries them, its context parts (quotes, terminal excerpts, pasted
// texts), and when.
type Stash struct {
	Text        string               `json:"text"`
	Attachments []session.Attachment `json:"attachments,omitempty"`
	Parts       []Part               `json:"parts,omitempty"`
	At          time.Time            `json:"at"`
}

// MaxStashes is how many drafts a chat keeps stashed; the oldest goes.
const MaxStashes = 20

// SetStashes gives the composer its chat's stashed drafts, newest first,
// once, before it first draws.
func (c *Composer) SetStashes(s []Stash) {
	if !c.stashSet {
		c.stash, c.stashSet = s, true
	}
}

// StashChanged is the chat's stashed drafts, newest first, and whether
// they changed since the last call, to keep them.
func (c *Composer) StashChanged() ([]Stash, bool) {
	ch := c.stashDirty
	c.stashDirty = false
	return slices.Clone(c.stash), ch
}

// anyToken matches an image's or a part's token, with the space after it.
var anyToken = regexp.MustCompile(`\[(Image|Quote|Terminal|Pasted text) #[0-9]+\] ?`)

// pull puts text, its attachments atts and its parts in the box after
// any draft, sep between them, each numbered into the draft: the token
// of attachment image-N or pasted-N, or of a part, takes the new number;
// a token with nothing to take goes; an attachment with no token goes at
// the end. Images past the draft's limit stay out, with a note. It
// returns each new image's and pasted text's attachment id by its token.
func (c *Composer) pull(text string, atts []session.Attachment, parts []Part, sep string) map[string]string {
	type swap struct{ mark, tok string }
	var swaps []swap
	ids := map[string]string{}
	full := false
	put := func(tok, old, id string) {
		// why: through a mark, so #1 renumbered to #2 is not then taken for the old #2.
		mark := "\x00" + itoa(len(swaps)) + "\x00"
		if old != "" && strings.Contains(text, old) {
			text = strings.Replace(text, old, mark, 1)
		} else {
			text = strings.TrimRight(text, " ") + " " + mark
		}
		swaps = append(swaps, swap{mark, tok})
		if id != "" {
			ids[tok] = id
		}
	}
	for _, a := range atts {
		switch a.Kind {
		case "image":
			if len(c.draft.Images) >= maxImages {
				full = true
				continue
			}
			data, err := base64.StdEncoding.DecodeString(a.Data)
			if err != nil {
				continue
			}
			thumb, err := Cover(data, c.thumbSide())
			if err != nil {
				continue
			}
			old := ""
			if n, ok := strings.CutPrefix(a.ID, "image-"); ok {
				old = "[Image #" + n + "]"
			}
			put(c.draft.Add(Image{Data: data, Mime: a.MimeType, Thumb: paint.NewImageOp(thumb), side: c.thumbSide()}), old, a.ID)
		case "pasted-text":
			old := ""
			if n, ok := strings.CutPrefix(a.ID, "pasted-"); ok {
				old = "[Pasted text #" + n + "]"
			}
			put(c.draft.AddPart(Part{Kind: PartPasted, Text: a.Text, Label: a.Name}), old, a.ID)
		}
	}
	for _, p := range parts {
		old := p.Token()
		put(c.draft.AddPart(p), old, "")
	}
	if stripped := anyToken.ReplaceAllString(text, ""); stripped != text {
		text = strings.TrimRight(stripped, " ")
	}
	for _, sw := range swaps {
		text = strings.Replace(text, sw.mark, sw.tok, 1)
	}
	text = strings.TrimLeft(text, " ")
	if cur := c.Text(); strings.TrimSpace(cur) != "" {
		text = strings.TrimRight(cur, "\n") + sep + text
	}
	c.SetText(text)
	if full {
		c.note = "A message takes 8 images at most"
	}
	return ids
}

// setAside empties the box and returns what it held.
func (c *Composer) setAside() *kept {
	k := &kept{c.Text(), c.draft}
	c.draft = Draft{}
	c.ed.SetText("")
	return k
}

// bringBack puts k back in the box.
func (c *Composer) bringBack(k *kept) {
	c.draft = Draft{}
	if k != nil {
		c.draft = k.draft
		c.SetText(k.text)
		return
	}
	c.SetText("")
}

// recalling reports whether the box shows a recalled prompt unedited.
func (c *Composer) recalling() bool { return c.hist.id != "" && c.Text() == c.hist.text }

// empty reports whether the box holds nothing at all.
func (c *Composer) empty() bool { return c.ed.Len() == 0 && len(c.draft.Images) == 0 }

// caretLines reports whether the caret is on the box's first and on its
// last line as drawn, wrapped lines counting.
func (c *Composer) caretLines() (first, last bool) {
	line, _ := c.ed.CaretPos()
	_, end := c.ed.Selection()
	return line == 0, len(c.ed.Regions(end, c.ed.Len(), nil)) <= 1
}

// recallKeys reports whether Up and Down are recall's now, the menus
// that take the arrows closed and nothing selected.
func (c *Composer) recallKeys(env Env) (up, down bool) {
	if env.History == nil || c.arrowsTaken() || c.qedit.id != "" || c.ed.SelectionLen() > 0 {
		return false, false
	}
	if c.empty() {
		return true, false
	}
	if !c.recalling() {
		return false, false
	}
	return c.caretLines()
}

// arrowsTaken reports whether an open menu takes the arrow keys.
func (c *Composer) arrowsTaken() bool { return c.mention.open() || c.slash.open() || c.stashOpen() }

// recallStep runs Up (back) or Down on the chat's prompts.
func (c *Composer) recallStep(env Env, back bool) {
	ps := Recallable(env.History())
	active := -1
	if c.recalling() {
		active = slices.IndexFunc(ps, func(p Sent) bool { return p.ID == c.hist.id })
		if active < 0 { // its id changed as the transcript caught up
			for i, p := range ps {
				if p.Text == c.hist.text {
					active = i
				}
			}
		}
	}
	next, ok := HistoryStep(len(ps), active, c.empty(), back)
	switch {
	case !ok:
	case next < 0:
		k := c.hist.keep
		c.hist = recall{}
		c.bringBack(k)
	default:
		c.recallTo(env, ps[next])
	}
}

// recallTo puts prompt p in the box, the box from before the first recall
// set aside; a cut prompt's whole copy is asked for.
func (c *Composer) recallTo(env Env, p Sent) {
	k := c.hist.keep
	if !c.recalling() {
		k = c.setAside()
	} else {
		c.draft = Draft{}
		c.ed.SetText("")
	}
	c.pull(p.Text, p.Attachments, nil, "")
	c.hist = recall{id: p.ID, text: c.Text(), cut: p.Cut, keep: k}
	if p.Cut && env.Full != nil {
		env.Full(p.ID)
	}
}

// recallWhole puts a cut recalled prompt's whole copy in the box once it
// is read, while the box still shows it unedited.
func (c *Composer) recallWhole(env Env) {
	if !c.hist.cut || env.History == nil {
		return
	}
	if !c.recalling() {
		c.hist.cut = false
		return
	}
	for _, p := range env.History() {
		if p.ID == c.hist.id && !p.Cut {
			c.recallTo(env, p)
			return
		}
	}
}

// followUp is the ChatSend How for text sent at a working agent: the
// send key does [chat] follow_up's, other does the other one. Steering
// sends now; the send key queues a slash or ! command, and either key
// queues to an agent that can't take a message mid-turn, rather than
// stop its turn.
func followUp(env Env, text string, other bool) string {
	if !steering(env, other) || !env.Steers || !other && command(text) {
		return "queue"
	}
	if env.Now {
		return "now"
	}
	return "steer"
}

// steering reports whether the key, the send key or other, is the one
// that steers: Ctrl+Enter by default, the send key with follow_up steer.
func steering(env Env, other bool) bool { return env.Steer != other }

// joins reports whether a steer of text takes the chat's queue along:
// the daemon joins it (proto.JoinLevel), and text is no command, which
// goes alone.
func joins(env Env, text string, other bool) bool {
	return env.Join && steering(env, other) && !command(text)
}

// joinable is the queued messages a steer takes along: all but a
// command, which keeps its place.
func joinable(qs []session.QueuedInput) []session.QueuedInput {
	var out []session.QueuedInput
	for _, q := range qs {
		if !command(q.Text) {
			out = append(out, q)
		}
	}
	return out
}

// shortcut names the modifier key.ModShortcut is: Cmd on macOS.
var shortcut = map[bool]string{true: "Cmd", false: "Ctrl"}[runtime.GOOS == "darwin"]

// sendKeys names the send key and the other follow-up's key.
func sendKeys(env Env) (send, other string) {
	if env.CtrlEnter {
		return shortcut + "+Enter", shortcut + "+Shift+Enter"
	}
	return "Enter", shortcut + "+Enter"
}

// followKeys names the key that queues and the one that steers.
func followKeys(env Env) (queue, steer string) {
	send, other := sendKeys(env)
	if env.Steer {
		return other, send
	}
	return send, other
}

// placeholder is the empty box's one hint line: Placeholder, and while
// the agent works, which key queues and which steers, as "Enter queues ·
// Ctrl+Enter steers".
func placeholder(env Env) string {
	s := env.Snap
	if !s.Busy() || asking(s) || Pending(s) {
		return Placeholder(s)
	}
	queue, steer := followKeys(env)
	switch {
	case !env.Steers:
		return queue + " queues for when the turn ends"
	case env.Steer:
		return steer + " steers · " + queue + " queues"
	}
	return queue + " queues · " + steer + " steers"
}

// queueHint is the strip's line under a running turn when no reason
// holds the queue: when its messages go, and the key that steers them
// in. joined is QueueStatus's line on how many go as one, "" for none.
func queueHint(env Env, joined string) string {
	if !env.Snap.Busy() {
		return joined
	}
	line, it := "Queued: goes when the turn ends", "it"
	if joined != "" {
		line, it = joined+" when the turn ends", "them"
	}
	switch {
	case !env.Steers:
		return line + "; this agent takes nothing mid-turn"
	case !env.Join:
		return line
	}
	_, steer := followKeys(env)
	return line + " · " + steer + " steers " + it + " in now"
}

// sendTip is the main button's tooltip for b.
func sendTip(env Env, b Button) string {
	send, other := sendKeys(env)
	switch b {
	case ButtonSendOff, ButtonSend:
		return "Send (" + send + ")"
	case ButtonStop:
		return "Stop (Esc)"
	case ButtonQueue:
		switch {
		case !env.Steers:
			return "Queue: sends when the turn ends (" + send + "). This agent takes nothing mid-turn"
		case steering(env, true) && env.Join:
			return "Queue: sends when the turn ends (" + send + "). Steer it in now, with the queue: " + other
		case steering(env, true):
			return "Queue: sends when the turn ends (" + send + "). Steer it in now: " + other
		}
		return "Queue: sends when the turn ends (" + send + ")"
	case ButtonSteer:
		return "Steer: sends into the running turn (" + send + "). Queue: " + other
	case ButtonSave:
		return "Save the queued message (" + send + "). Esc cancels"
	}
	return ""
}

// stashOpen reports whether the stash menu is open.
func (c *Composer) stashOpen() bool { return c.menu && c.which == &c.stashBtn }

// stashKey runs Ctrl+S: a draft goes to the stash and the box empties;
// on an empty box the one stashed draft comes back, or the menu opens
// when there are several.
func (c *Composer) stashKey(at time.Time) {
	if c.stashOpen() {
		c.snapMenu(at, false)
		return
	}
	if c.hasDraft() {
		c.stashDraft(at)
		return
	}
	switch len(c.stash) {
	case 0:
		c.note = "Nothing stashed: Ctrl+S with a draft keeps it for later"
	case 1:
		c.unstash(0)
	default:
		c.stashSel = 0
		c.which = &c.stashBtn
		c.snapMenu(at, true)
	}
}

// stashDraft keeps the draft, newest first, and empties the box.
func (c *Composer) stashDraft(at time.Time) {
	if c.waiting() {
		c.note = "Wait for the image to attach"
		return
	}
	text := c.Text()
	s := Stash{Text: text, Attachments: Attachments(c.draft.Ordered(text)), Parts: slices.Clone(c.draft.Parts), At: at}
	c.stash = append([]Stash{s}, c.stash...)
	if len(c.stash) > MaxStashes {
		c.stash = c.stash[:MaxStashes]
	}
	c.stashDirty, c.note, c.hist = true, "", recall{}
	c.ed.SetText("")
	c.draft.Clear()
}

// unstash puts stashed draft i in the box, after any draft, and takes it
// out of the stash.
func (c *Composer) unstash(i int) {
	if i < 0 || i >= len(c.stash) {
		return
	}
	s := c.stash[i]
	c.stash = slices.Delete(slices.Clone(c.stash), i, i+1)
	c.stashSel = max(0, min(c.stashSel, len(c.stash)-1))
	c.stashDirty, c.note, c.hist = true, "", recall{}
	c.pull(s.Text, s.Attachments, s.Parts, "\n\n")
}

// dropStash deletes stashed draft i, and closes the menu once none is
// left.
func (c *Composer) dropStash(at time.Time, i int) {
	if i < 0 || i >= len(c.stash) {
		return
	}
	c.stash = slices.Delete(slices.Clone(c.stash), i, i+1)
	c.stashDirty = true
	c.stashSel = max(0, min(c.stashSel, len(c.stash)-1))
	if len(c.stash) == 0 && c.stashOpen() {
		c.snapMenu(at, false)
	}
}

// stashMenuKey runs a key on the open stash menu: Up and Down move,
// Enter restores, Delete or Ctrl+Backspace deletes, Escape closes.
func (c *Composer) stashMenuKey(at time.Time, e key.Event) {
	n := len(c.stash)
	switch e.Name {
	case key.NameUpArrow:
		c.stashSel = (c.stashSel - 1 + n) % max(n, 1)
	case key.NameDownArrow:
		c.stashSel = (c.stashSel + 1) % max(n, 1)
	case key.NameReturn, key.NameEnter:
		c.snapMenu(at, false)
		c.unstash(c.stashSel)
	case key.NameDeleteForward, key.NameDeleteBackward:
		c.dropStash(at, c.stashSel)
	case key.NameEscape:
		c.snapMenu(at, false)
	}
}

// StashPreview is the menu's line for s: its text on one line, cut at 90
// characters, or its image count.
func StashPreview(s Stash) string {
	t := strings.Join(strings.Fields(unglue(s.Text)), " ")
	t = strings.TrimSpace(anyToken.ReplaceAllString(t+" ", ""))
	if t != "" {
		if utf8.RuneCountInString(t) > 90 {
			t = string([]rune(t)[:90]) + "…"
		}
		return t
	}
	switch n := len(s.Attachments); n {
	case 0:
		return "(empty)"
	case 1:
		return "(1 image)"
	default:
		return "(" + itoa(n) + " images)"
	}
}

// Ago is how long before now t was, as the stash menu says it: now, 5m,
// 3h, 2d.
func Ago(t, now time.Time) string {
	switch d := now.Sub(t); {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return itoa(int(d/time.Minute)) + "m"
	case d < 24*time.Hour:
		return itoa(int(d/time.Hour)) + "h"
	default:
		return itoa(int(d/(24*time.Hour))) + "d"
	}
}

// editQueued loads the newest queued message into the box to edit, the
// draft set aside, and opens a folded strip so its row shows.
func (c *Composer) editQueued(env Env, now time.Time) {
	shown := c.shownQueue(env.Snap, now)
	if len(shown) == 0 {
		return
	}
	q := shown[len(shown)-1]
	k := c.setAside()
	if c.recalling() {
		k = c.hist.keep
	}
	c.hist = recall{}
	ids := c.pull(q.Text, q.Attachments, nil, "")
	c.qedit = queueEdit{id: q.ID, text: c.Text(), orig: q.Text, ids: ids, keep: k}
	c.q.open = true
}

// saveQueued saves the edit into its queued message, in place when its
// images are the ones it had, else queued again at the end with them;
// the draft from before comes back.
func (c *Composer) saveQueued(env Env) {
	text := c.Text()
	imgs := c.draft.Ordered(text)
	if c.waiting() {
		c.note = "Wait for the image to attach"
		return
	}
	if strings.TrimSpace(text) == "" && len(imgs) == 0 {
		c.note = "Type the message, or remove it from the queue"
		return
	}
	pasted := c.draft.Pasted(text)
	var toks []string
	for _, im := range imgs {
		toks = append(toks, Token(im.N))
	}
	for _, p := range c.draft.Parts {
		if p.Kind == PartPasted {
			toks = append(toks, p.Token())
		}
	}
	same := len(toks) == len(c.qedit.ids) && !slices.ContainsFunc(toks, func(t string) bool { return c.qedit.ids[t] == "" })
	out := c.draft.Expand(text)
	switch {
	case same:
		// why: the daemon keeps the message's attachments, so each token goes back to its number.
		var swaps []string
		for i, t := range toks {
			id := c.qedit.ids[t]
			old := ""
			if n, ok := strings.CutPrefix(id, "image-"); ok {
				old = "[Image #" + n + "]"
			} else if n, ok := strings.CutPrefix(id, "pasted-"); ok {
				old = "[Pasted text #" + n + "]"
			}
			if old != "" {
				mark := "\x00" + itoa(i) + "\x00"
				out = strings.Replace(out, t, mark, 1)
				swaps = append(swaps, mark, old)
			}
		}
		if out = strings.NewReplacer(swaps...).Replace(out); out != c.qedit.orig {
			c.out = append(c.out, proto.ChatQueue{Chat: env.Chat, Action: "edit", ID: c.qedit.id, Text: out})
		}
	default:
		c.out = append(c.out,
			proto.ChatQueue{Chat: env.Chat, Action: "remove", ID: c.qedit.id},
			proto.ChatSend{Chat: env.Chat, Text: out, Attachments: append(Attachments(imgs), pasted...), How: "queue"})
	}
	k := c.qedit.keep
	c.qedit, c.note = queueEdit{}, ""
	c.bringBack(k)
}

// cancelQueued drops the edit; the queued message stays as it was and
// the draft from before comes back.
func (c *Composer) cancelQueued() {
	k := c.qedit.keep
	c.qedit = queueEdit{}
	c.bringBack(k)
}

// queueEditGone ends an edit whose message left the queue, sent or
// removed meanwhile: a changed edit stays in the box when the draft set
// aside was empty, else that draft comes back.
func (c *Composer) queueEditGone(env Env) {
	if c.qedit.id == "" || slices.ContainsFunc(env.Snap.QueuedInputs, func(q session.QueuedInput) bool { return q.ID == c.qedit.id }) {
		return
	}
	k := c.qedit.keep
	changed := c.Text() != c.qedit.text
	c.qedit = queueEdit{}
	switch {
	case changed && (k == nil || strings.TrimSpace(k.text) == "" && len(k.draft.Images) == 0):
		c.note = "The message left the queue before you saved; your edit stays here"
	case changed:
		c.note = "The message left the queue before you saved"
		c.bringBack(k)
	default:
		c.bringBack(k)
	}
}

// steerOldest sends the oldest queued message into the running turn now,
// the draft left as it is.
func (c *Composer) steerOldest(env Env, now time.Time) {
	i := slices.IndexFunc(c.shownQueue(env.Snap, now), func(q session.QueuedInput) bool { return q.Error == "" })
	switch {
	case i < 0:
		c.note = "Nothing queued to send"
		return
	case !env.Snap.Busy():
		c.note = "No turn is running to send it into"
		return
	case !env.Steers:
		c.note = "This agent can't take a message mid-turn: Send now on its row stops the turn first"
		return
	}
	q := c.shownQueue(env.Snap, now)[i]
	how := "steer"
	if env.Queue {
		how = "now"
	}
	if c.q.gone == nil {
		c.q.gone = map[string]bool{}
	}
	c.q.gone[q.ID] = true
	c.out = append(c.out, proto.ChatQueue{Chat: env.Chat, Action: how, ID: q.ID})
}
