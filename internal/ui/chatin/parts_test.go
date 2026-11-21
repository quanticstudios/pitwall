package chatin

import (
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"gioui.org/io/key"
	"gioui.org/io/transfer"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

// A quote is a Markdown quote with its comment after it, a terminal
// excerpt a fence under its command that no backtick run in it closes;
// each is a paragraph of its own wherever its token stood. A pasted
// text's token stays for the daemon, and a token of no part stays.
func TestExpand(t *testing.T) {
	var d Draft
	q := d.AddPart(Part{Kind: PartQuote, Text: "Use a mutex.\n\nOr a channel.", Comment: " why not atomics? "})
	tm := d.AddPart(Part{Kind: PartTerminal, Text: "\nok  ./a\nFAIL ./b ```x```\n", Label: "go test ./..."})
	tn := d.AddPart(Part{Kind: PartTerminal, Text: "plain"})
	p := d.AddPart(Part{Kind: PartPasted, Text: "big", Label: "pasted-text-1.txt"})
	if q != "[Quote #1]" || tm != "[Terminal #1]" || tn != "[Terminal #2]" || p != "[Pasted text #1]" {
		t.Fatalf("tokens %q %q %q %q", q, tm, tn, p)
	}
	got := d.Expand("About " + q + " see " + tm + tn + " and " + p + " [Quote #9] end")
	want := "About\n\n> Assistant quote:\n> Use a mutex.\n>\n> Or a channel.\n\nComment: why not atomics?\n\nsee\n\n" +
		"Terminal output of `go test ./...`:\n````\nok  ./a\nFAIL ./b ```x```\n````\n\n" +
		"Terminal output:\n```\nplain\n```\n\nand [Pasted text #1] [Quote #9] end"
	if got != want {
		t.Fatalf("expand:\n%q\nwant\n%q", got, want)
	}
	if got := d.Expand(q); got != "> Assistant quote:\n> Use a mutex.\n>\n> Or a channel.\n\nComment: why not atomics?" {
		t.Fatalf("a token alone: %q", got)
	}
	// A quote of a token's text is not expanded again.
	var e Draft
	tok := e.AddPart(Part{Kind: PartQuote, Text: "[Quote #1]"})
	if got := e.Expand(tok); got != "> Assistant quote:\n> [Quote #1]" {
		t.Fatalf("a quote of its own token: %q", got)
	}
	if got := (Part{Kind: PartTerminal, Text: "x", Label: "echo `a`"}).block(); !strings.HasPrefix(got, "Terminal output of echo `a`:\n") {
		t.Fatalf("a command with a backtick: %q", got)
	}
	if got := d.Pasted("x " + p); len(got) != 1 || got[0].ID != "pasted-1" || got[0].Kind != "pasted-text" || got[0].Name != "pasted-text-1.txt" || got[0].Text != "big" || got[0].SizeBytes != 3 {
		t.Fatalf("pasted %+v", got)
	}
	if got := d.Pasted("no token"); len(got) != 0 {
		t.Fatalf("pasted without its token %+v", got)
	}
	d.Sync("only " + tm)
	if len(d.Parts) != 1 || d.Parts[0].Token() != tm {
		t.Fatalf("sync kept %+v", d.Parts)
	}
	if glue("[Pasted text #1]") != "[Pasted text #1]" || unglue(glue("[Quote #2] [Terminal #3]")) != "[Quote #2] [Terminal #3]" {
		t.Fatal("glue")
	}
}

// T3 Code's paste rules: 32 KiB or more, or past the message limit.
func TestFold(t *testing.T) {
	if Fold(strings.Repeat("a", FoldAt-1), 0) || !Fold(strings.Repeat("a", FoldAt), 0) {
		t.Fatal("the 32 KiB line")
	}
	if Fold("abc", MessageLimit-3) || !Fold("abc", MessageLimit-2) {
		t.Fatal("the message limit")
	}
	if Fold(strings.Repeat("é", FoldAt/2-1), 0) || !Fold(strings.Repeat("é", FoldAt/2), 0) {
		t.Fatal("bytes, not runes, against 32 KiB")
	}
	for n, want := range map[int]string{0: "0 B", 999: "999 B", 1000: "1 KB", 48_400: "48 KB", 1_250_000: "1.2 MB"} {
		if got := Size(n); got != want {
			t.Errorf("Size(%d) = %q, want %q", n, got, want)
		}
	}
	if Lines("") != 0 || Lines("a") != 1 || Lines("a\n") != 1 || Lines("a\nb") != 2 {
		t.Fatal("Lines")
	}
	if FirstLine("\n  \n  go   test\nx") != "go test" {
		t.Fatal("FirstLine")
	}
}

// pasteText is Ctrl+V, or Ctrl+Shift+V when inline, with text s on the
// clipboard.
func (h *harness) pasteText(s string, inline bool) {
	if inline {
		h.press("V", key.ModShortcut|key.ModShift)
	} else {
		h.press("V", key.ModShortcut)
	}
	h.r.Queue(transfer.DataEvent{Type: "application/text", Open: func() io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }})
	h.frame()
}

func sendOf(t *testing.T, m []any) proto.ChatSend {
	t.Helper()
	if len(m) != 1 {
		t.Fatalf("sent %v", m)
	}
	s, ok := m[0].(proto.ChatSend)
	if !ok {
		t.Fatalf("sent %T", m[0])
	}
	return s
}

// A paste of 32 KiB goes as a text file chip, sent as a pasted-text
// attachment with its token in place; Ctrl+Shift+V keeps it inline, and
// so does a daemon that takes no text files. A small paste past the
// message limit goes as a file too.
func TestPasteFolds(t *testing.T) {
	big := "first line\n" + strings.Repeat("x", FoldAt)
	h := newHarness(t)
	h.files = true
	h.typ("look at ")
	h.pasteText(big, false)
	if h.c.Text() != "look at [Pasted text #1] " || len(h.c.draft.Parts) != 1 {
		t.Fatalf("after a big paste: %q, %d parts", h.c.Text(), len(h.c.draft.Parts))
	}
	if p := h.c.draft.Parts[0]; p.Label != "pasted-text-1.txt" || p.Text != big {
		t.Fatalf("part %q %d bytes", p.Label, len(p.Text))
	}
	s := sendOf(t, h.press(key.NameReturn, 0))
	if s.Text != "look at [Pasted text #1] " || len(s.Attachments) != 1 || s.Attachments[0].Kind != "pasted-text" || s.Attachments[0].ID != "pasted-1" || s.Attachments[0].Text != big {
		t.Fatalf("send %q %+v", s.Text, len(s.Attachments))
	}
	if len(h.c.draft.Parts) != 0 {
		t.Fatal("the part outlived the send")
	}

	h.pasteText(big, true)
	if h.c.Text() != big || len(h.c.draft.Parts) != 0 {
		t.Fatalf("Ctrl+Shift+V folded the paste: %d parts", len(h.c.draft.Parts))
	}
	h.c.SetText("")
	h.pasteText("small", false) // the inline flag lasts one paste
	if h.c.Text() != "small" {
		t.Fatalf("a small paste: %q", h.c.Text())
	}
	h.c.SetText(strings.Repeat("y", MessageLimit-2))
	h.pasteText("abc", false)
	if !strings.HasSuffix(h.c.Text(), "[Pasted text #1] ") { // a send starts the numbers again
		t.Fatalf("a paste past the limit stayed inline: ...%q", h.c.Text()[len(h.c.Text())-20:])
	}
	h.c.SetText("")

	h.files = false
	h.pasteText(big, false)
	if h.c.Text() != big {
		t.Fatal("a daemon without text files got a file")
	}
	h.c.SetText("")
	h.files = true

	h.pasteText(strings.Repeat("z", MaxPasted+1), false)
	if h.c.Text() != "" || !strings.Contains(h.c.note, "8 MiB") {
		t.Fatalf("an 8 MiB paste: %q, note %q", h.c.Text(), h.c.note)
	}
	// Eight attachments: a big paste stays inline while it fits.
	h.c.note = ""
	for range 8 {
		h.c.draft.Add(Image{Data: []byte{1}})
	}
	h.pasteText(big, false)
	if h.c.Text() != big {
		t.Fatalf("a full message folded a paste that fits: %d parts", len(h.c.draft.Parts))
	}
	h.c.SetText(strings.Repeat("y", MessageLimit-2))
	h.pasteText("abc", false)
	if utf8.RuneCountInString(h.c.Text()) != MessageLimit-2 || !strings.Contains(h.c.note, "remove an attachment") {
		t.Fatalf("a full message took a paste past the limit: note %q", h.c.note)
	}
}

// A message past the limit stays in the box with a note.
func TestMessageLimit(t *testing.T) {
	h := newHarness(t)
	h.c.SetText(strings.Repeat("a", MessageLimit+1))
	h.frame()
	if m := h.press(key.NameReturn, 0); len(m) != 0 || !strings.Contains(h.c.note, "120000") {
		t.Fatalf("sent %d messages, note %q", len(m), h.c.note)
	}
	if utf8.RuneCountInString(h.c.Text()) != MessageLimit+1 {
		t.Fatal("the draft went")
	}
}

// Cite puts a quote at the caret, a word of its own; its comment opens on
// the pencil, Enter keeps it and Esc drops the edit; the card goes to
// the source; Backspace takes the token and the part; a send expands it
// and a refused one brings the quote back.
func TestQuote(t *testing.T) {
	h := newHarness(t)
	var revealed []Source
	h.reveal = func(s Source) bool { revealed = append(revealed, s); return s.Item == "a1" }
	src := Source{Item: "a1", FromKey: "a1/0", ToKey: "a1/2", From: 3, To: 9}
	h.typ("Explain")
	if !h.c.AddQuote("\nUse a mutex.\n", src) {
		t.Fatal("AddQuote refused")
	}
	h.frame()
	if h.c.Text() != "Explain [Quote #1] " {
		t.Fatalf("text %q", h.c.Text())
	}
	if h.c.AddQuote("  \n ", src) || h.c.AddQuote(strings.Repeat("a", MaxQuote+1), src) || !strings.Contains(h.c.note, "too long") {
		t.Fatalf("a blank or long quote went in; note %q", h.c.note)
	}
	h.c.note = ""
	tok := "[Quote #1]"
	u := h.c.parts[tok]
	if u == nil {
		t.Fatal("no card for the quote")
	}
	u.pen.Click()
	h.frame()
	h.frame()
	if h.c.editing != tok || !h.r.Source().Focused(&h.c.comment) {
		t.Fatalf("the comment field did not open with the keys: editing %q", h.c.editing)
	}
	h.c.comment.Insert("is this safe?")
	h.press(key.NameEscape, 0)
	if h.c.editing != "" || h.c.draft.Parts[0].Comment != "" {
		t.Fatalf("Esc kept the comment %q", h.c.draft.Parts[0].Comment)
	}
	h.frame()
	if !h.r.Source().Focused(&h.c.ed) {
		t.Fatal("the draft did not get the keys back")
	}
	u.pen.Click()
	h.frame()
	h.frame()
	h.c.comment.Insert("is this safe?")
	h.press(key.NameReturn, 0)
	if h.c.editing != "" || h.c.draft.Parts[0].Comment != "is this safe?" {
		t.Fatalf("Enter: editing %q, comment %q", h.c.editing, h.c.draft.Parts[0].Comment)
	}
	u.body.Click()
	h.frame()
	if len(revealed) != 1 || revealed[0] != src || h.c.note != "" {
		t.Fatalf("revealed %v, note %q", revealed, h.c.note)
	}
	h.c.draft.Parts[0].Src.Item = "gone"
	u.body.Click()
	h.frame()
	if !strings.Contains(h.c.note, "no longer") {
		t.Fatalf("a source that went: note %q", h.c.note)
	}
	h.c.draft.Parts[0].Src.Item = "a1"
	h.c.note = ""

	s := sendOf(t, h.press(key.NameReturn, 0))
	want := "Explain\n\n> Assistant quote:\n> Use a mutex.\n\nComment: is this safe?"
	if s.Text != want || len(s.Attachments) != 0 {
		t.Fatalf("send %q", s.Text)
	}
	h.c.Result(Result{Req: s, Message: "busy"}, h.now)
	if h.c.Text() != "Explain [Quote #1] " || h.c.draft.Part(PartQuote, 1) == nil || h.c.draft.Part(PartQuote, 1).Comment != "is this safe?" {
		t.Fatalf("the refused send came back as %q", h.c.Text())
	}
	h.c.SetText("x [Quote #1]")
	h.frame()
	h.press(key.NameDeleteBackward, 0)
	if h.c.Text() != "x " || len(h.c.draft.Parts) != 0 {
		t.Fatalf("Backspace left %q, %d parts", h.c.Text(), len(h.c.draft.Parts))
	}
}

// A terminal excerpt goes in at the caret and is named once the daemon
// says what printed it; one too long for a message goes as a text file;
// the card's x takes it out.
func TestTerminal(t *testing.T) {
	h := newHarness(t)
	h.files = true
	n := h.c.AddTerminal("\r\n$ go test\r\nFAIL\r\n\r\n", "")
	if n != 1 || h.c.Text() != "[Terminal #1] " {
		t.Fatalf("n %d text %q", n, h.c.Text())
	}
	if h.c.AddTerminal(" \n ", "x") != 0 || h.c.note == "" {
		t.Fatal("a blank selection went in")
	}
	h.c.LabelTerminal(n, " go test ")
	h.c.LabelTerminal(9, "nothing")
	h.frame()
	h.typ("why?")
	s := sendOf(t, h.press(key.NameReturn, 0))
	if want := "Terminal output of `go test`:\n```\n$ go test\nFAIL\n```\n\nwhy?"; s.Text != want {
		t.Fatalf("send %q", s.Text)
	}
	if n := h.c.AddTerminal(strings.Repeat("l\n", FoldAt), "make"); n != 0 || len(h.c.draft.Parts) != 1 || h.c.draft.Parts[0].Kind != PartPasted {
		t.Fatalf("a long excerpt: n %d, parts %+v", n, len(h.c.draft.Parts))
	}
	h.c.SetText("")
	h.frame()
	h.c.AddTerminal("one", "")
	h.frame()
	u := h.c.parts["[Terminal #1]"]
	if u == nil {
		t.Fatal("no card")
	}
	u.rm.Click()
	h.frame()
	if strings.TrimSpace(h.c.Text()) != "" || len(h.c.draft.Parts) != 0 {
		t.Fatalf("the x left %q", h.c.Text())
	}
}

// The x on an image the text names asks first: Keep leaves both, Esc
// too, Remove takes both. An image the text no longer names goes at once.
func TestRemoveImageAsks(t *testing.T) {
	h := newHarness(t)
	h.typ("see ")
	h.clip = pngOf(8, 6)
	h.paste("")
	h.waitImage()
	h.frame()
	h.c.rm[1].Click()
	h.frame()
	if h.c.confirm != 1 || len(h.c.Images()) != 1 {
		t.Fatalf("confirm %d, %d images", h.c.confirm, len(h.c.Images()))
	}
	h.c.keepB.Click()
	h.frame()
	if h.c.confirm != 0 || len(h.c.Images()) != 1 || h.c.Text() != "see [Image #1] " {
		t.Fatalf("Keep: confirm %d, text %q", h.c.confirm, h.c.Text())
	}
	h.c.rm[1].Click()
	h.frame()
	h.press(key.NameEscape, 0)
	if h.c.confirm != 0 || len(h.c.Images()) != 1 {
		t.Fatal("Esc removed the image")
	}
	h.c.rm[1].Click()
	h.frame()
	h.c.dropB.Click()
	h.frame()
	if len(h.c.Images()) != 0 || h.c.Text() != "see " {
		t.Fatalf("Remove left %q, %d images", h.c.Text(), len(h.c.Images()))
	}
	h.paste("")
	h.waitImage()
	h.c.SetText("see ") // the token went; the image stays
	h.frame()
	if len(h.c.Images()) != 1 {
		t.Fatal("the image went with its token")
	}
	h.c.rm[2].Click()
	h.frame()
	if h.c.confirm != 0 || len(h.c.Images()) != 0 {
		t.Fatalf("an unnamed image asked: confirm %d", h.c.confirm)
	}
	// An unnamed image still goes with the message, after the named ones.
	h.paste("")
	h.waitImage()
	h.paste("")
	h.waitImage()
	h.c.SetText("look [Image #4]")
	h.frame()
	s := sendOf(t, h.press(key.NameReturn, 0))
	if len(s.Attachments) != 2 || s.Attachments[0].ID != "image-4" || s.Attachments[1].ID != "image-3" {
		t.Fatalf("attachments %+v", s.Attachments)
	}
}

// A queued message's pasted text comes back as a chip, renumbered into
// the draft.
func TestPullBackPasted(t *testing.T) {
	h := newHarness(t)
	h.c.draft.AddPart(Part{Kind: PartPasted, Text: "a", Label: PastedName(1)})
	h.c.SetText("[Pasted text #1]")
	h.c.PullBack(session.QueuedInput{Text: "read [Pasted text #1]", Attachments: []session.Attachment{
		{ID: "pasted-1", Kind: "pasted-text", Name: "pasted-text-1.txt", Text: "queued"}}})
	if h.c.Text() != "[Pasted text #1]\nread [Pasted text #2]" {
		t.Fatalf("text %q", h.c.Text())
	}
	if p := h.c.draft.Part(PartPasted, 2); p == nil || p.Text != "queued" || p.Label != "pasted-text-1.txt" {
		t.Fatalf("part %+v", p)
	}
}
