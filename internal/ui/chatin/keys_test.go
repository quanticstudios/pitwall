package chatin

import (
	"encoding/base64"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

func imageAtt(n int, data []byte) session.Attachment {
	return session.Attachment{ID: "image-" + itoa(n), Kind: "image", MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(data)}
}

func TestHistoryStep(t *testing.T) {
	for _, c := range []struct {
		n, active   int
		empty, back bool
		next        int
		ok          bool
	}{
		{3, -1, true, true, 2, true},    // Up on an empty box: the newest
		{3, -1, false, true, 0, false},  // Up on a draft: the caret's
		{0, -1, true, true, 0, false},   // nothing sent yet
		{3, 2, false, true, 1, true},    // Up again: older
		{3, 0, false, true, 0, false},   // the oldest stays
		{3, 1, false, false, 2, true},   // Down: newer
		{3, 2, false, false, -1, true},  // Down past the newest: the box back
		{3, -1, true, false, 0, false},  // Down with nothing recalled
		{3, -1, false, false, 0, false}, // nor on a draft
	} {
		next, ok := HistoryStep(c.n, c.active, c.empty, c.back)
		if ok != c.ok || ok && next != c.next {
			t.Errorf("%+v: %d %v", c, next, ok)
		}
	}
}

func TestRecallable(t *testing.T) {
	img := imageAtt(1, []byte("x"))
	got := Recallable([]Sent{
		{ID: "1", Text: "a"}, {ID: "2", Text: "a"}, {ID: "3", Text: "  "},
		{ID: "4", Text: "", Attachments: []session.Attachment{img}}, {ID: "5", Text: "b"}, {ID: "6", Text: "a"},
	})
	var ids []string
	for _, p := range got {
		ids = append(ids, p.ID)
	}
	if !slices.Equal(ids, []string{"2", "4", "5", "6"}) {
		t.Fatalf("recallable %v", ids)
	}
}

// Up on an empty box brings back the newest prompt, again goes further
// back, Down comes forward and past the newest empties the box. A
// recalled prompt's Up waits for the caret's first line, its Down for the
// last. An edit makes it a draft the arrows move in; Esc brings the box
// back before it stops the turn; sending sends the edit.
func TestPromptRecall(t *testing.T) {
	h := newHarness(t)
	hist := []Sent{{ID: "p1", Text: "first"}, {ID: "p2", Text: "second\nline two"}, {ID: "p3", Text: "third"}}
	h.opts = func(e *Env) { e.History = func() []Sent { return hist } }
	step := func(k key.Name, want string) {
		t.Helper()
		if m := h.press(k, 0); len(m) != 0 {
			t.Fatalf("%s sent %+v", k, m)
		}
		if h.c.Text() != want {
			t.Fatalf("after %s: %q, want %q", k, h.c.Text(), want)
		}
	}
	step(key.NameUpArrow, "third")
	step(key.NameUpArrow, "second\nline two")
	step(key.NameUpArrow, "second\nline two") // the caret goes up a line first
	if line, _ := h.c.ed.CaretPos(); line != 0 {
		t.Fatalf("the caret is on line %d", line)
	}
	step(key.NameUpArrow, "first")
	step(key.NameUpArrow, "first") // the oldest
	step(key.NameDownArrow, "second\nline two")
	step(key.NameDownArrow, "third")
	step(key.NameDownArrow, "")
	step(key.NameDownArrow, "")

	h.snap.SessionState = session.StateRunning
	step(key.NameUpArrow, "third")
	step(key.NameEscape, "") // the box back, the turn left running
	if m := h.press(key.NameEscape, 0); len(m) != 1 || m[0] != (proto.ChatInterrupt{Chat: "c1"}) {
		t.Fatalf("the second Esc sent %+v", m)
	}
	h.snap.SessionState = session.StateReady

	step(key.NameUpArrow, "third")
	h.typ("!")
	step(key.NameUpArrow, "third!") // a draft now: the caret's
	step(key.NameDownArrow, "third!")
	m := h.press(key.NameReturn, 0)
	if len(m) != 1 || m[0].(proto.ChatSend).Text != "third!" || h.c.hist.id != "" {
		t.Fatalf("Enter sent %+v", m)
	}
	h.typ("mine")
	step(key.NameUpArrow, "mine") // a draft is never replaced
	h.c.SetText("")
	h.frame()

	// A prompt with an image comes back with it, renumbered; a token whose
	// picture the transcript lacks goes.
	pic := pngOf(8, 6)
	hist = append(hist, Sent{ID: "p4", Text: "x [Image #1] look [Image #3] here", Attachments: []session.Attachment{imageAtt(3, pic)}})
	step(key.NameUpArrow, "x look [Image #1] here")
	if ims := h.c.Images(); len(ims) != 1 || ims[0].N != 1 || ims[0].Data == nil {
		t.Fatalf("images %+v", ims)
	}
	m = h.press(key.NameReturn, 0)
	if s := m[0].(proto.ChatSend); len(s.Attachments) != 1 || s.Attachments[0].ID != "image-1" {
		t.Fatalf("sent %+v", s)
	}
	h.c.SetText("")
	h.frame()

	// The same prompt twice in a row is one step.
	hist = []Sent{{ID: "a", Text: "one"}, {ID: "b", Text: "two"}, {ID: "c", Text: "two"}}
	step(key.NameUpArrow, "two")
	step(key.NameUpArrow, "one")
	step(key.NameDownArrow, "two")
	step(key.NameDownArrow, "")
}

// An open @ menu keeps the arrows while the box shows a recalled prompt.
func TestRecallKeepsMenus(t *testing.T) {
	h := newHarness(t)
	h.paths = []string{"main.go", "make.go"}
	h.opts = func(e *Env) {
		e.History = func() []Sent { return []Sent{{ID: "1", Text: "older"}, {ID: "2", Text: "look at @ma"}} }
	}
	h.press(key.NameUpArrow, 0)
	h.frame()
	if !h.c.mention.open() {
		t.Fatalf("the menu is closed on %q", h.c.Text())
	}
	h.press(key.NameDownArrow, 0)
	h.press(key.NameUpArrow, 0)
	if h.c.Text() != "look at @ma" || h.c.mention.sel != 0 {
		t.Fatalf("the arrows left %q, sel %d", h.c.Text(), h.c.mention.sel)
	}
}

// The slash menu keeps the arrows too, over a recalled command.
func TestRecallKeepsSlash(t *testing.T) {
	h := newHarness(t)
	h.snap.Commands = testCommands
	h.opts = func(e *Env) {
		e.History = func() []Sent { return []Sent{{ID: "1", Text: "older"}, {ID: "2", Text: "/co"}} }
	}
	h.press(key.NameUpArrow, 0)
	h.frame()
	if !h.c.slash.open() {
		t.Fatalf("the slash menu is closed on %q", h.c.Text())
	}
	h.press(key.NameDownArrow, 0)
	h.press(key.NameUpArrow, 0)
	h.press(key.NameDownArrow, 0)
	if h.c.Text() != "/co" || h.c.slash.sel != 1 {
		t.Fatalf("the arrows left %q, sel %d", h.c.Text(), h.c.slash.sel)
	}
}

// A cut prompt is asked for whole, and its whole copy replaces it while
// the box shows it unedited.
func TestRecallCut(t *testing.T) {
	h := newHarness(t)
	hist := []Sent{{ID: "p", Text: "head … tail", Cut: true}}
	var asked []string
	h.opts = func(e *Env) {
		e.History = func() []Sent { return hist }
		e.Full = func(id string) { asked = append(asked, id) }
	}
	h.press(key.NameUpArrow, 0)
	if h.c.Text() != "head … tail" || !slices.Equal(asked, []string{"p"}) {
		t.Fatalf("%q, asked %v", h.c.Text(), asked)
	}
	hist = []Sent{{ID: "p", Text: "head middle tail"}}
	h.frame()
	if h.c.Text() != "head middle tail" || h.c.hist.cut {
		t.Fatalf("the whole copy: %q", h.c.Text())
	}
	// An edit made before it came keeps the edit.
	h.c.SetText("")
	hist = []Sent{{ID: "q", Text: "cut", Cut: true}}
	h.press(key.NameUpArrow, 0)
	h.typ("!")
	hist = []Sent{{ID: "q", Text: "whole"}}
	h.frame()
	if h.c.Text() != "cut!" {
		t.Fatalf("the edit went: %q", h.c.Text())
	}
}

func TestFollowUp(t *testing.T) {
	for _, c := range []struct {
		steer, steers, now bool
		text               string
		other              bool
		want               string
	}{
		{false, true, true, "hi", false, "queue"},
		{false, true, true, "hi", true, "now"},
		{false, false, true, "hi", true, "queue"},    // to an agent that takes nothing mid-turn Ctrl+Enter queues, never stops the turn
		{false, true, true, "/compact", true, "now"}, // the steer key sends a command at once
		{false, true, false, "hi", true, "steer"},
		{true, true, true, "hi", false, "now"},
		{true, true, false, "hi", false, "steer"},
		{true, true, true, "hi", true, "queue"},
		{true, true, true, "/compact", false, "queue"}, // a command waits for the turn
		{true, true, true, "!ls", false, "queue"},
		{true, false, true, "hi", false, "queue"}, // never stops a turn on the send key
	} {
		got := followUp(Env{Steer: c.steer, Steers: c.steers, Now: c.now}, c.text, c.other)
		if got != c.want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

// [chat] send_key = "ctrl+enter": Enter and Shift+Enter add lines,
// Ctrl+Enter sends, Ctrl+Shift+Enter with a draft does the other
// follow-up. follow_up = "steer" steers on the send key.
func TestSendKeys(t *testing.T) {
	h := newHarness(t)
	ctrl, steer := true, false
	h.opts = func(e *Env) { e.CtrlEnter, e.Steer, e.Steers = ctrl, steer, true }
	h.frame()
	h.typ("a")
	if m := h.press(key.NameReturn, 0); len(m) != 0 || h.c.Text() != "a\n" {
		t.Fatalf("Enter: %+v, %q", m, h.c.Text())
	}
	if m := h.press(key.NameReturn, key.ModShift); len(m) != 0 || h.c.Text() != "a\n\n" {
		t.Fatalf("Shift+Enter: %+v, %q", m, h.c.Text())
	}
	h.typ("b")
	m := h.press(key.NameReturn, key.ModShortcut)
	if len(m) != 1 || m[0].(proto.ChatSend).Text != "a\n\nb" || m[0].(proto.ChatSend).How != "" {
		t.Fatalf("Ctrl+Enter sent %+v", m)
	}
	h.snap.SessionState = session.StateRunning
	h.typ("c")
	if m := h.press(key.NameReturn, key.ModShortcut); len(m) != 1 || m[0].(proto.ChatSend).How != "queue" {
		t.Fatalf("Ctrl+Enter while working sent %+v", m)
	}
	h.typ("d")
	if m := h.press(key.NameReturn, key.ModShortcut|key.ModShift); len(m) != 1 || m[0].(proto.ChatSend).How != "now" {
		t.Fatalf("Ctrl+Shift+Enter with a draft sent %+v", m)
	}

	ctrl, steer = false, true
	h.typ("e")
	if b := h.c.button(h.env()); b != ButtonSteer {
		t.Fatalf("button %v", b)
	}
	if m := h.press(key.NameReturn, 0); len(m) != 1 || m[0].(proto.ChatSend).How != "now" {
		t.Fatalf("Enter with follow_up steer sent %+v", m)
	}
	h.typ("/compact")
	if b := h.c.button(h.env()); b != ButtonQueue {
		t.Fatalf("a command's button %v", b)
	}
	if m := h.press(key.NameReturn, 0); len(m) != 1 || m[0].(proto.ChatSend).How != "queue" {
		t.Fatalf("a command sent %+v", m)
	}
	h.typ("f")
	if m := h.press(key.NameReturn, key.ModShortcut); len(m) != 1 || m[0].(proto.ChatSend).How != "queue" {
		t.Fatalf("Ctrl+Enter with follow_up steer sent %+v", m)
	}
}

// Ctrl+S keeps the draft and empties the box; on an empty box it brings
// back the one draft kept, or opens the menu on several: Up and Down
// move, Enter restores, Delete removes, Esc closes. Images ride along.
func TestStash(t *testing.T) {
	h := newHarness(t)
	ctrlS := func() []any { return h.press("S", key.ModCtrl) }
	if ctrlS(); h.c.note == "" || len(h.c.stash) != 0 {
		t.Fatalf("Ctrl+S on nothing: note %q", h.c.note)
	}
	h.typ("first ")
	h.clip = pngOf(8, 6)
	h.paste("")
	h.waitImage()
	if m := ctrlS(); len(m) != 0 || h.c.Text() != "" || len(h.c.Images()) != 0 {
		t.Fatalf("Ctrl+S sent %+v, left %q", m, h.c.Text())
	}
	list, changed := h.c.StashChanged()
	if !changed || len(list) != 1 || list[0].Text != "first [Image #1] " || len(list[0].Attachments) != 1 {
		t.Fatalf("stash %+v, changed %v", list, changed)
	}
	if _, changed := h.c.StashChanged(); changed {
		t.Fatal("changed twice")
	}
	ctrlS() // one: back at once
	if h.c.Text() != "first [Image #1] " || len(h.c.Images()) != 1 || len(h.c.stash) != 0 || h.c.stashOpen() {
		t.Fatalf("restored %q, %d images, %d left", h.c.Text(), len(h.c.Images()), len(h.c.stash))
	}
	ctrlS()
	h.typ("second")
	ctrlS()
	h.typ("third")
	ctrlS()
	ctrlS() // several: the menu, the newest lit
	if !h.c.stashOpen() || h.c.stashSel != 0 || func() bool { v, _ := h.c.popT.Eval(h.now); return v != 1 }() {
		t.Fatalf("menu open %v, sel %d", h.c.stashOpen(), h.c.stashSel)
	}
	h.press(key.NameDownArrow, 0)
	h.press(key.NameDownArrow, 0)
	h.press(key.NameDownArrow, 0) // wraps
	if h.c.stashSel != 0 {
		t.Fatalf("sel %d", h.c.stashSel)
	}
	h.press(key.NameUpArrow, 0) // wraps back: the oldest
	h.press(key.NameDeleteForward, 0)
	if len(h.c.stash) != 2 || h.c.stash[0].Text != "third" || h.c.stash[1].Text != "second" || h.c.stashSel != 1 {
		t.Fatalf("after Delete: %+v sel %d", h.c.stash, h.c.stashSel)
	}
	if m := h.press(key.NameReturn, 0); len(m) != 0 || h.c.Text() != "second" || h.c.stashOpen() {
		t.Fatalf("Enter: sent %+v, %q, open %v", m, h.c.Text(), h.c.stashOpen())
	}
	// The chip's menu with a draft in the box: a restore goes after it.
	h.c.toggle(h.now, &h.c.stashBtn)
	h.frame()
	h.press(key.NameReturn, 0)
	if h.c.Text() != "second\n\nthird" || len(h.c.stash) != 0 {
		t.Fatalf("restored after the draft: %q", h.c.Text())
	}
	h.press(key.NameEscape, 0)

	// Esc closes the menu, leaving the stash; at most MaxStashes stay.
	h.c.SetText("")
	for i := range MaxStashes + 3 {
		h.typ("draft " + itoa(i))
		ctrlS()
	}
	if len(h.c.stash) != MaxStashes || h.c.stash[0].Text != "draft 22" {
		t.Fatalf("%d kept, newest %q", len(h.c.stash), h.c.stash[0].Text)
	}
	ctrlS()
	h.press(key.NameEscape, 0)
	if h.c.stashOpen() || len(h.c.stash) != MaxStashes || h.c.Text() != "" {
		t.Fatal("Esc did more than close the menu")
	}
	// Ctrl+Backspace removes too, and the menu closes with the last one.
	h.c.stash = h.c.stash[:2]
	ctrlS()
	h.press(key.NameDeleteBackward, key.ModCtrl)
	h.press(key.NameDeleteBackward, key.ModCtrl)
	if len(h.c.stash) != 0 || h.c.stashOpen() {
		t.Fatalf("%d left, open %v", len(h.c.stash), h.c.stashOpen())
	}

	// An image on its way waits; SetStashes takes the first list alone.
	h.gate = make(chan struct{})
	h.paste("")
	ctrlS()
	if len(h.c.stash) != 0 || h.c.note == "" {
		t.Fatal("stashed an image on its way")
	}
	close(h.gate)
	h.waitImage()
	var c Composer
	c.SetStashes([]Stash{{Text: "a"}})
	c.SetStashes([]Stash{{Text: "b"}})
	if c.stash[0].Text != "a" {
		t.Fatal("SetStashes took a second list")
	}
}

func TestStashPreview(t *testing.T) {
	long := strings.Repeat("ab ", 40)
	for _, c := range []struct {
		s    Stash
		want string
	}{
		{Stash{Text: "fix\n  the   bug"}, "fix the bug"},
		{Stash{Text: "[Image #1] look"}, "look"},
		{Stash{Text: "[Image #1] ", Attachments: []session.Attachment{{}}}, "(1 image)"},
		{Stash{Attachments: []session.Attachment{{}, {}}}, "(2 images)"},
		{Stash{Text: long}, strings.TrimSpace(long)[:90] + "…"},
	} {
		if got := StashPreview(c.s); got != c.want {
			t.Errorf("%q: %q", c.s.Text, got)
		}
	}
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{10 * time.Second: "now", 5 * time.Minute: "5m", 3 * time.Hour: "3h", 50 * time.Hour: "2d"} {
		if got := Ago(now.Add(-d), now); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}

func queueSnap(qs ...session.QueuedInput) session.Snapshot {
	return session.Snapshot{SessionID: "c1", SessionState: session.StateRunning, QueuedInputs: qs}
}

// Alt+Up at the box's start loads the newest queued message, its row lit
// and the strip open; Enter saves it in place, Esc leaves it, and the
// draft from before comes back either way.
func TestQueueEdit(t *testing.T) {
	h := newHarness(t)
	h.snap = queueSnap(session.QueuedInput{ID: "a", Text: "older"}, session.QueuedInput{ID: "b", Text: "newer"}, session.QueuedInput{ID: "c", Text: "third"})
	h.typ("my draft")
	if h.press(key.NameUpArrow, key.ModAlt); h.c.qedit.id != "" {
		t.Fatal("Alt+Up away from the box's start")
	}
	h.c.ed.SetCaret(0, 0)
	h.frame()
	h.press(key.NameUpArrow, key.ModAlt)
	if h.c.Text() != "third" || h.c.qedit.id != "c" || !h.c.q.open || h.c.button(h.env()) != ButtonSave {
		t.Fatalf("loaded %q, editing %q", h.c.Text(), h.c.qedit.id)
	}
	if h.press(key.NameUpArrow, key.ModAlt); h.c.qedit.id != "c" {
		t.Fatal("a second Alt+Up moved the edit")
	}
	h.press(key.NameEscape, 0)
	if h.c.Text() != "my draft" || h.c.qedit.id != "" {
		t.Fatalf("Esc left %q", h.c.Text())
	}
	h.c.ed.SetCaret(0, 0)
	h.frame()
	h.press(key.NameUpArrow, key.ModAlt)
	h.typ(" edited")
	m := h.press(key.NameReturn, 0)
	if len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatQueue{Chat: "c1", Action: "edit", ID: "c", Text: "third edited"}) {
		t.Fatalf("Enter sent %+v", m)
	}
	if h.c.Text() != "my draft" || h.c.qedit.id != "" {
		t.Fatalf("after the save: %q", h.c.Text())
	}
	// Saved unchanged, nothing goes; emptied, it stays with a note.
	h.c.SetText("")
	h.frame()
	if h.press(key.NameUpArrow, key.ModAlt); h.c.qedit.id != "c" {
		t.Fatal("Alt+Up on an empty box")
	}
	if m := h.press(key.NameReturn, 0); len(m) != 0 || h.c.qedit.id != "" {
		t.Fatalf("an unchanged save sent %+v", m)
	}
	h.press(key.NameUpArrow, key.ModAlt)
	h.c.SetText("")
	h.frame()
	if m := h.press(key.NameReturn, key.ModShortcut); len(m) != 0 || h.c.qedit.id != "c" || h.c.note == "" {
		t.Fatalf("an empty save sent %+v", m)
	}
	h.press(key.NameEscape, 0)

	// Where the window keeps Alt+Up, Alt+Shift+Up edits.
	h.opts = func(e *Env) { e.QueueEditMods = key.ModAlt | key.ModShift }
	h.frame()
	if h.press(key.NameUpArrow, key.ModAlt); h.c.qedit.id != "" {
		t.Fatal("Alt+Up edited with Alt+Shift set")
	}
	if h.press(key.NameUpArrow, key.ModAlt|key.ModShift); h.c.qedit.id != "c" {
		t.Fatal("Alt+Shift+Up did not edit")
	}
	h.press(key.NameEscape, 0)
}

// A queued message's images load numbered from 1; saved with the same
// images, the text keeps the message's own numbers; with one removed or
// added, the message is queued again with its images.
func TestQueueEditImages(t *testing.T) {
	h := newHarness(t)
	pic := pngOf(8, 6)
	h.snap = queueSnap(session.QueuedInput{ID: "q", Text: "see [Image #2] and [Image #3]", Attachments: []session.Attachment{imageAtt(2, pic), imageAtt(3, pic)}})
	h.frame()
	h.press(key.NameUpArrow, key.ModAlt)
	if h.c.Text() != "see [Image #1] and [Image #2]" || len(h.c.Images()) != 2 {
		t.Fatalf("loaded %q", h.c.Text())
	}
	h.typ(" now")
	m := h.press(key.NameReturn, 0)
	if len(m) != 1 || m[0].(proto.ChatQueue).Text != "see [Image #2] and [Image #3] now" {
		t.Fatalf("saved %+v", m)
	}
	h.press(key.NameUpArrow, key.ModAlt) // with no frame between: the save's frame asked for it
	if h.c.qedit.id != "q" {
		t.Fatalf("second Alt+Up: %q, text %q", h.c.qedit.id, h.c.Text())
	}
	h.c.RemoveImage(1)
	h.frame()
	m = h.press(key.NameReturn, 0)
	if len(m) != 2 || !reflect.DeepEqual(m[0], proto.ChatQueue{Chat: "c1", Action: "remove", ID: "q"}) {
		t.Fatalf("saved without an image: %+v", m)
	}
	if s := m[1].(proto.ChatSend); s.How != "queue" || s.Text != "see and [Image #2]" || len(s.Attachments) != 1 {
		t.Fatalf("queued again %+v", s)
	}
}

// A message that leaves the queue mid-edit ends the edit: a changed one
// stays when the box was empty, else the draft comes back.
func TestQueueEditGone(t *testing.T) {
	h := newHarness(t)
	h.snap = queueSnap(session.QueuedInput{ID: "q", Text: "queued"})
	h.frame()
	h.press(key.NameUpArrow, key.ModAlt)
	h.typ(" more")
	h.snap.QueuedInputs = nil
	h.frame()
	if h.c.qedit.id != "" || h.c.Text() != "queued more" || h.c.note == "" {
		t.Fatalf("changed, box was empty: %q, editing %q", h.c.Text(), h.c.qedit.id)
	}
	h.c.SetText("mine")
	h.c.ed.SetCaret(0, 0)
	h.snap.QueuedInputs = []session.QueuedInput{{ID: "r", Text: "queued"}}
	h.frame()
	h.press(key.NameUpArrow, key.ModAlt)
	h.typ("!")
	h.snap.QueuedInputs = nil
	h.frame()
	if h.c.Text() != "mine" || h.c.qedit.id != "" {
		t.Fatalf("changed, with a draft: %q", h.c.Text())
	}
	h.c.ed.SetCaret(0, 0)
	h.snap.QueuedInputs = []session.QueuedInput{{ID: "s", Text: "queued"}}
	h.frame()
	h.press(key.NameUpArrow, key.ModAlt)
	h.snap.QueuedInputs = nil
	h.frame()
	if h.c.Text() != "mine" || h.c.note != "" {
		t.Fatalf("unchanged: %q, note %q", h.c.Text(), h.c.note)
	}
}

// Ctrl+Shift+Enter sends the oldest queued message into the running turn
// now, past one that failed, and leaves the draft alone; it notes why
// when there is no turn or the agent can't take it.
func TestSteerOldest(t *testing.T) {
	h := newHarness(t)
	steers := true
	h.opts = func(e *Env) { e.Steers = steers }
	h.snap = queueSnap(session.QueuedInput{ID: "a", Text: "failed", Error: "no"}, session.QueuedInput{ID: "b", Text: "oldest"}, session.QueuedInput{ID: "c", Text: "newest"})
	h.typ("draft")
	m := h.press(key.NameReturn, key.ModShortcut|key.ModShift)
	if len(m) != 1 || !reflect.DeepEqual(m[0], proto.ChatQueue{Chat: "c1", Action: "now", ID: "b"}) || h.c.Text() != "draft" {
		t.Fatalf("sent %+v, left %q", m, h.c.Text())
	}
	if shown := h.c.shownQueue(&h.snap, h.now); len(shown) != 2 || shown[1].ID != "c" {
		t.Fatalf("shown %+v", shown)
	}
	h.old = true // a daemon before ChatQueue "now"
	if m := h.press(key.NameReturn, key.ModShortcut|key.ModShift); len(m) != 1 || m[0].(proto.ChatQueue).Action != "steer" {
		t.Fatalf("old daemon: %+v", m)
	}
	h.old = false
	steers = false
	h.snap.QueuedInputs = append(h.snap.QueuedInputs, session.QueuedInput{ID: "d", Text: "x"})
	if m := h.press(key.NameReturn, key.ModShortcut|key.ModShift); len(m) != 0 || h.c.note == "" {
		t.Fatalf("an agent that can't steer: %+v", m)
	}
	steers = true
	h.snap.SessionState = session.StateReady
	h.c.note = ""
	if m := h.press(key.NameReturn, key.ModShortcut|key.ModShift); len(m) != 0 || h.c.note == "" {
		t.Fatalf("no turn: %+v", m)
	}
}

// A key the editor takes changes what the composer's keys do at once,
// with no frame between: Home then Alt+Up edits, Backspace to an empty
// box then Up recalls.
func TestKeysFollowTheEditor(t *testing.T) {
	h := newHarness(t)
	h.opts = func(e *Env) { e.History = func() []Sent { return []Sent{{ID: "p", Text: "sent"}} } }
	h.snap = queueSnap(session.QueuedInput{ID: "q", Text: "queued"})
	h.typ("ab")
	h.press(key.NameHome, 0)
	if h.press(key.NameUpArrow, key.ModAlt); h.c.qedit.id != "q" || h.c.Text() != "queued" {
		t.Fatalf("Home, Alt+Up: editing %q, %q", h.c.qedit.id, h.c.Text())
	}
	h.press(key.NameEscape, 0)
	h.press(key.NameEnd, 0)
	h.press(key.NameDeleteBackward, 0)
	h.press(key.NameDeleteBackward, 0)
	if h.press(key.NameUpArrow, 0); h.c.Text() != "sent" {
		t.Fatalf("Backspace, Up: %q", h.c.Text())
	}
}

// A stash keeps the draft's quotes and pasted texts, a recalled prompt
// and a queued message bring their pasted texts back as chips, and a
// queue edit saved with them keeps the message's numbers.
func TestKeysKeepParts(t *testing.T) {
	h := newHarness(t)
	h.typ("see ")
	if !h.c.AddQuote("the passage", Source{Item: "r1"}) {
		t.Fatal("no quote")
	}
	h.frame()
	h.press("S", key.ModCtrl)
	if len(h.c.stash) != 1 || len(h.c.stash[0].Parts) != 1 || h.c.Text() != "" {
		t.Fatalf("stash %+v, box %q", h.c.stash, h.c.Text())
	}
	h.press("S", key.ModCtrl)
	if q := h.c.draft.Part(PartQuote, 1); q == nil || q.Text != "the passage" || q.Src.Item != "r1" || !strings.Contains(h.c.Text(), "[Quote #1]") {
		t.Fatalf("restored %q, parts %+v", h.c.Text(), h.c.draft.Parts)
	}
	h.c.SetText("")
	h.c.draft.Clear()
	h.frame()

	pasted := session.Attachment{ID: "pasted-2", Kind: "pasted-text", Name: "notes.txt", Text: "a long paste"}
	h.opts = func(e *Env) {
		e.History = func() []Sent {
			return []Sent{{ID: "p", Text: "read [Pasted text #2] please", Attachments: []session.Attachment{pasted}}}
		}
	}
	h.frame()
	h.press(key.NameUpArrow, 0)
	if p := h.c.draft.Part(PartPasted, 1); p == nil || p.Text != "a long paste" || h.c.Text() != "read [Pasted text #1] please" {
		t.Fatalf("recalled %q, parts %+v", h.c.Text(), h.c.draft.Parts)
	}
	h.press(key.NameEscape, 0)

	h.opts = nil
	h.snap = queueSnap(session.QueuedInput{ID: "q", Text: "read [Pasted text #2] please", Attachments: []session.Attachment{pasted}})
	h.frame()
	h.press(key.NameUpArrow, key.ModAlt)
	h.typ(" now")
	m := h.press(key.NameReturn, 0)
	if len(m) != 1 || m[0].(proto.ChatQueue).Text != "read [Pasted text #2] please now" {
		t.Fatalf("saved %+v", m)
	}
}
