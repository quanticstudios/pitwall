package chatin

import (
	"slices"
	"testing"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
)

var testCommands = []session.Command{
	{Name: "decompose", Description: "Split a task"},
	{Name: "compact", Description: "Free up context by summarizing the conversation so far"},
	{Name: "config", Description: "Open settings", Terminal: true},
	{Name: "context", Description: "Show current context usage"},
	{Name: "model", Description: "Set the AI model for Claude Code"},
	{Name: "Code-Review", Description: "Review the current diff"},
}

func names(cs []session.Command) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

// Names that start with the query come first, then the ones that have
// it inside, each in the list's order; case does not count.
func TestSlashMatches(t *testing.T) {
	for q, want := range map[string][]string{
		"co":  {"compact", "config", "context", "Code-Review", "decompose"},
		"CON": {"config", "context"},
		"":    names(testCommands),
		"mod": {"model"},
		"zz":  nil,
	} {
		if got := names(SlashMatches(testCommands, q)); !slices.Equal(got, want) {
			t.Errorf("SlashMatches(%q) = %q, want %q", q, got, want)
		}
	}
}

// Typing / at the start opens the menu and typing narrows it; Up and
// Down move, Tab or Enter fills the command in with a space and sends
// nothing, Esc closes it until the text changes, a click picks a row. A
// / later in the draft, or a chat without commands, opens nothing.
func TestSlashMenu(t *testing.T) {
	h := newHarness(t)
	h.typ("/")
	if h.c.slash.open() {
		t.Fatal("the menu opened on a chat with no commands")
	}
	h.c.SetText("")
	h.snap.Commands = testCommands
	h.typ("/")
	if got := names(h.c.slash.rows); !slices.Equal(got, names(testCommands)) {
		t.Fatalf("/ lists %q", got)
	}
	h.typ("co")
	if got := names(h.c.slash.rows); len(got) != 5 || got[0] != "compact" || got[4] != "decompose" {
		t.Fatalf("/co lists %q", got)
	}
	h.press(key.NameDownArrow, 0)
	h.press(key.NameDownArrow, 0)
	h.press(key.NameUpArrow, 0)
	if h.c.slash.sel != 1 {
		t.Fatalf("Down, Down, Up: sel %d", h.c.slash.sel)
	}
	if m := h.press(key.NameTab, 0); len(m) != 0 || h.c.Text() != "/config " || h.c.slash.open() {
		t.Fatalf("Tab: sent %+v, text %q, open %v", m, h.c.Text(), h.c.slash.open())
	}

	h.c.SetText("")
	h.typ("/mo")
	if m := h.press(key.NameReturn, 0); len(m) != 0 || h.c.Text() != "/model " {
		t.Fatalf("Enter: sent %+v, text %q", m, h.c.Text())
	}
	if m := h.press(key.NameReturn, 0); len(m) != 1 || m[0].(proto.ChatSend).Text != "/model " {
		t.Fatalf("Enter after the pick sent %+v", m)
	}

	h.c.SetText("")
	h.typ("/con")
	h.press(key.NameEscape, 0)
	if h.c.slash.open() {
		t.Fatal("Esc left the menu open")
	}
	if h.frame(); h.c.slash.open() {
		t.Fatal("the menu came back on the text Esc closed it on")
	}
	if h.typ("t"); !h.c.slash.open() || h.c.slash.rows[0].Name != "context" {
		t.Fatal("typing after Esc did not open the menu again")
	}

	h.c.click("slash:context").Click()
	h.frame()
	if h.c.Text() != "/context " || h.c.slash.open() {
		t.Fatalf("click: %q, open %v", h.c.Text(), h.c.slash.open())
	}

	h.c.SetText("/co rest of it")
	h.c.ed.SetCaret(3, 3)
	h.frame()
	h.press(key.NameReturn, 0)
	if h.c.Text() != "/compact rest of it" {
		t.Fatalf("a pick before more text: %q", h.c.Text())
	}

	h.c.SetText("")
	h.typ("look at /co")
	if h.c.slash.open() {
		t.Fatal("a / inside the draft opened the menu")
	}
}

// With no row to show the menu says why in one muted line: an older
// daemon, a list not in yet, or nothing matching. The line takes no key
// but Esc, so Enter still sends a message that starts with a /.
func TestSlashMenuNotes(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct {
		old  bool
		cmds []session.Command
		text string
		note string
	}{
		{true, nil, "/", oldNote},
		{false, nil, "/", noListNote},
		{false, testCommands, "/tmp/x", noMatchNote},
	} {
		h.old, h.snap.Commands = c.old, c.cmds
		h.c.SetText("")
		h.typ(c.text)
		if h.c.slash.open() || h.c.slash.note != c.note {
			t.Fatalf("%q, old %v, %d commands: note %q, open %v", c.text, c.old, len(c.cmds), h.c.slash.note, h.c.slash.open())
		}
	}
	if m := h.press(key.NameReturn, 0); len(m) != 1 || m[0].(proto.ChatSend).Text != "/tmp/x" {
		t.Fatalf("Enter under a note sent %+v", m)
	}
	h.typ("/zz")
	h.press(key.NameEscape, 0)
	if h.c.slash.shown() {
		t.Fatal("Esc left the note")
	}
	if h.frame(); h.c.slash.shown() {
		t.Fatal("the note came back on the text Esc closed it on")
	}
}

// The highlight stays on its command when the chat's list is read again
// and moves rows; a new query starts again at the first row.
func TestSlashMenuKeepsHighlight(t *testing.T) {
	h := newHarness(t)
	h.snap.Commands = testCommands
	h.typ("/co")
	h.press(key.NameDownArrow, 0)
	if got := h.c.slash.rows[h.c.slash.sel].Name; got != "config" {
		t.Fatalf("Down highlights %q", got)
	}
	h.snap.Commands = append([]session.Command{{Name: "cost", Description: "Show the cost"}}, testCommands...)
	h.frame()
	if got := h.c.slash.rows[h.c.slash.sel].Name; got != "config" {
		t.Fatalf("after the list was read again, %q is highlighted", got)
	}
	h.typ("n")
	if got := h.c.slash.rows[h.c.slash.sel].Name; got != "config" || h.c.slash.sel != 0 {
		t.Fatalf("a new query highlights %q at %d, want the first row", got, h.c.slash.sel)
	}
}
