package sidebar

import (
	"image"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/transfer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// Pastes past three lines or 800 characters become blocks, named by
// their lines or, for one long line, their characters.
func TestReplyPasteBlocks(t *testing.T) {
	long := strings.Repeat("x", 801)
	for text, want := range map[string]bool{"a\nb\nc": false, "a\nb\nc\n": false, "a\nb\nc\nd": true, long: true, strings.Repeat("x", 800): false} {
		if collapses(text) != want {
			t.Errorf("collapses(%q) = %v", text[:min(len(text), 12)], !want)
		}
	}
	if got := pasteToken(1, strings.Repeat("l\n", 42)); got != "[Pasted text #1 · 42 lines]" {
		t.Errorf("token %q", got)
	}
	if got := pasteToken(2, long); got != "[Pasted text #2 · 801 chars]" {
		t.Errorf("token %q", got)
	}
}

// expand puts each paste back in place of its token and takes the image
// tokens out, their paths in the order the tokens stand; a token the user
// edited is plain text.
func TestReplyExpand(t *testing.T) {
	paste := "one\ntwo\nthree\nfour"
	d := draft{
		text: "[Image #2] see [Pasted text #1 · 4 lines] and [Image #1] ok",
		blocks: []block{
			{token: "[Pasted text #1 · 4 lines]", text: paste},
			{token: "[Image #1]", text: "/s/a.png", image: true},
			{token: "[Image #2]", text: "/s/b.png", image: true},
		},
	}
	text, images := d.expand()
	if want := "see " + paste + " and ok"; text != want {
		t.Errorf("text %q, want %q", text, want)
	}
	if !slices.Equal(images, []string{"/s/b.png", "/s/a.png"}) {
		t.Errorf("images %q", images)
	}
	d.text = "[Image #1 and [Pasted text #1 · 4 lines"
	if text, images := d.expand(); text != d.text || images != nil {
		t.Errorf("edited tokens: %q %q", text, images)
	}
	if d.next(true) != 3 || d.next(false) != 2 {
		t.Errorf("next numbers %d %d", d.next(true), d.next(false))
	}
	d.text = "ab [Image #1]"
	if got := d.tokenBefore(13); got != 3 {
		t.Errorf("token before the end at %d, want 3", got)
	}
	if got := d.tokenBefore(12); got != -1 {
		t.Errorf("token before a broken end at %d, want -1", got)
	}
}

// Up goes back through sent replies, Down forward, and past the newest
// the draft Up left comes back. Drafts are kept per tab, at most 16, and
// an empty one is forgotten.
func TestReplyHistoryAndDrafts(t *testing.T) {
	var r replyState
	if r.recall(true) || r.recall(false) {
		t.Fatal("recall with no history moved")
	}
	r.remember(draft{text: "first"})
	r.remember(draft{text: "second"})
	r.load(draft{text: "typing"})
	for i, step := range []struct {
		up   bool
		ok   bool
		want string
	}{{true, true, "second"}, {true, true, "first"}, {true, false, "first"}, {false, true, "second"}, {false, true, "typing"}, {false, false, "typing"}} {
		if ok := r.recall(step.up); ok != step.ok || r.ed.Text() != step.want {
			t.Fatalf("step %d: ok %v, text %q", i, ok, r.ed.Text())
		}
	}
	for range replyHistMax + 5 {
		r.remember(draft{text: "x"})
	}
	if len(r.hist) != replyHistMax {
		t.Errorf("history holds %d", len(r.hist))
	}

	for i := range replyDraftsMax + 2 {
		r.keep(string(rune('a'+i)), draft{text: "d"})
	}
	if len(r.drafts) != replyDraftsMax || r.drafts["a"].text != "" || r.drafts["c"].text != "d" {
		t.Errorf("drafts %d, a %q, c %q", len(r.drafts), r.drafts["a"].text, r.drafts["c"].text)
	}
	r.keep("c", draft{text: "  "})
	if _, ok := r.drafts["c"]; ok || slices.Contains(r.order, "c") {
		t.Error("an empty draft was kept")
	}
}

// replyHarness is a sidebar with tab w, whose Claude in pane p asks a
// question, drawn by a router so keys and pastes reach the sheet.
type replyHarness struct {
	t   *testing.T
	s   Sidebar
	r   input.Router
	ops op.Ops
	st  model.State
}

func newReplyHarness(t *testing.T) *replyHarness {
	h := &replyHarness{t: t, st: model.State{
		Workspaces: []model.Workspace{{ID: "w", Name: "Session"}, {ID: "x", Name: "Other"}, {ID: "sh", Name: "logs"}},
		Panes:      []model.Pane{{ID: "p", WorkspaceID: "w", Provider: model.ProviderClaude}, {ID: "q", WorkspaceID: "x", Provider: model.ProviderCodex}, {ID: "t", WorkspaceID: "sh"}},
		Activities: []model.Activity{
			{PaneID: "t", WorkspaceID: "sh", Provider: model.ProviderTerminal, State: model.StateAwaitingInput, Detail: "server restarted"},
			{PaneID: "p", WorkspaceID: "w", Provider: model.ProviderClaude, State: model.StateAwaitingInput, Detail: "Which file?"},
			{PaneID: "q", WorkspaceID: "x", Provider: model.ProviderCodex, State: model.StateWorking},
		},
	}}
	h.frame()
	return h
}

func (h *replyHarness) frame() []Event {
	h.ops.Reset()
	gtx := layout.Context{Ops: &h.ops, Source: h.r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(288, 700)), Now: time.Now()}
	_, events := h.s.Layout(gtx, theme.Dark(), &h.st, "", "x")
	h.r.Frame(&h.ops)
	return slices.Clone(events)
}

// open opens the sheet on w and checks it took the keyboard.
func (h *replyHarness) open() {
	h.t.Helper()
	h.s.openReply("w", "p")
	h.frame()
	h.frame()
	if h.s.Replying() != "w" || !h.s.Editing() || !h.r.Source().Focused(&h.s.reply.ed) {
		h.t.Fatalf("the sheet is not open with focus: replying %q", h.s.Replying())
	}
}

// press presses k with mods and draws a frame.
func (h *replyHarness) press(k key.Name, mods key.Modifiers) []Event {
	h.r.Queue(key.Event{Name: k, Modifiers: mods, State: key.Press})
	return h.frame()
}

// paste pastes text with Ctrl+V, as the clipboard holds it.
func (h *replyHarness) paste(text string) []Event {
	h.press("V", key.ModShortcut)
	h.r.Queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser { return io.NopCloser(strings.NewReader(text)) }})
	return h.frame()
}

func (h *replyHarness) text() string { return h.s.reply.ed.Text() }

// The sheet's keys: Shift+Enter adds a line and Enter sends; an empty
// reply stays open and says so; Escape keeps the draft for the tab; Up
// brings back the last reply; a long paste is a block Backspace takes
// whole; a paste with no text attaches an image.
func TestReplySheetKeys(t *testing.T) {
	h := newReplyHarness(t)
	v := newView(layout.Context{}, theme.Dark(), &h.st, "", "x")
	if !h.s.replyable(v, "w", false, v.activity["w"]) || h.s.replyable(v, "x", false, v.activity["x"]) || h.s.replyable(v, "sh", false, v.activity["sh"]) {
		t.Fatal("Reply shows on the wrong rows")
	}

	h.open()
	if ev := h.press(key.NameReturn, 0); len(ev) != 0 || h.s.Replying() != "w" || h.s.reply.note != "Type a reply to send it" {
		t.Fatalf("empty Enter: events %v, replying %q, note %q", ev, h.s.Replying(), h.s.reply.note)
	}
	h.s.reply.ed.SetText("use a.go")
	h.s.reply.ed.SetCaret(8, 8)
	h.press(key.NameReturn, key.ModShift)
	h.s.reply.ed.Insert("then b.go")
	ev := h.press(key.NameReturn, 0)
	want := []Event{Reply{WorkspaceID: "w", PaneID: "p", Text: "use a.go\nthen b.go"}}
	if len(ev) != 1 || ev[0].(Reply).Text != "use a.go\nthen b.go" || ev[0].(Reply).PaneID != "p" || h.s.Replying() != "" {
		t.Fatalf("Enter sent %#v, want %#v; replying %q", ev, want, h.s.Replying())
	}

	h.open()
	if h.text() != "" {
		t.Fatalf("a sent reply left a draft %q", h.text())
	}
	h.press(key.NameUpArrow, 0)
	if h.text() != "use a.go\nthen b.go" {
		t.Fatalf("Up brought back %q", h.text())
	}
	h.press(key.NameDownArrow, 0)
	if h.text() != "" {
		t.Fatalf("Down left %q", h.text())
	}

	h.s.reply.ed.SetText("half a thought")
	h.press(key.NameEscape, 0)
	if h.s.Replying() != "" || h.s.Editing() {
		t.Fatal("Escape left the sheet open")
	}
	h.open()
	if h.text() != "half a thought" {
		t.Fatalf("the draft came back as %q", h.text())
	}

	h.s.reply.ed.SetCaret(h.s.reply.ed.Len(), h.s.reply.ed.Len())
	h.paste(" short")
	h.paste("1\n2\n3\n4\n5")
	if want := "half a thought short[Pasted text #1 · 5 lines]"; h.text() != want {
		t.Fatalf("pasted %q, want %q", h.text(), want)
	}
	h.press(key.NameDeleteBackward, 0)
	if h.text() != "half a thought short" {
		t.Fatalf("Backspace after a block left %q", h.text())
	}
	h.paste("1\n2\n3\n4\n5")

	ev = h.paste("")
	if !slices.Equal(ev, []Event{AttachImage{WorkspaceID: "w", PaneID: "p"}}) || !h.s.reply.reading {
		t.Fatalf("an empty paste sent %v", ev)
	}
	if ev := h.paste(""); len(ev) != 0 {
		t.Fatalf("a second image while one reads sent %v", ev)
	}
	h.s.ReplyImage("x", "/s/other.png", nil) // another tab's: dropped
	h.s.ReplyImage("w", "/s/paste/image-1.png", nil)
	h.frame()
	if !strings.HasSuffix(h.text(), "[Pasted text #2 · 5 lines][Image #1] ") {
		t.Fatalf("attached as %q", h.text())
	}
	ev = h.press(key.NameReturn, 0)
	if len(ev) != 1 {
		t.Fatalf("send with blocks: %v", ev)
	}
	got := ev[0].(Reply)
	if got.Text != "half a thought short1\n2\n3\n4\n5" || !slices.Equal(got.Images, []string{"/s/paste/image-1.png"}) {
		t.Fatalf("sent %q with %q", got.Text, got.Images)
	}

	// A failed read says why and attaches nothing.
	h.open()
	h.paste("")
	h.s.ReplyImage("w", "", errFake("the clipboard holds no text or image"))
	h.frame()
	if h.s.reply.note != "Can't attach the image: the clipboard holds no text or image" || !h.s.reply.noteBad || h.text() != "" {
		t.Fatalf("failed image: note %q, text %q", h.s.reply.note, h.text())
	}

	// The sheet closes, keeping its draft, once its row is gone.
	h.s.reply.ed.SetText("kept")
	h.st.Workspaces = h.st.Workspaces[1:]
	h.frame()
	if h.s.Replying() != "" || h.s.reply.drafts["w"].text != "kept" {
		t.Fatalf("row gone: replying %q, draft %q", h.s.Replying(), h.s.reply.drafts["w"].text)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
