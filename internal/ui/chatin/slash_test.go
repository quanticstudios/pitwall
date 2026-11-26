package chatin

import (
	"slices"
	"strings"
	"testing"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/antigravity"
	"github.com/quanticstudios/pitwall/internal/session/claude"
	"github.com/quanticstudios/pitwall/internal/session/codex"
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

// The ranking: the name or an alias that is the query, then one that
// starts with it, a word that does, one that has it inside, the letters
// in order, then the description; within each, the more used first, then
// the list's order. Case does not count.
func TestSlashMatches(t *testing.T) {
	cmds := append(slices.Clone(testCommands),
		session.Command{Name: "usage", Description: "Show session cost", Aliases: []string{"cost", "stats"}},
		session.Command{Name: "review", Description: "Review the branch"})
	for _, c := range []struct {
		q    string
		used map[string]int
		want []string
	}{
		{"co", nil, []string{"compact", "config", "context", "Code-Review", "usage", "decompose"}},
		{"CON", nil, []string{"config", "context", "compact"}}, // compact's description has "con"
		{"", nil, names(cmds)},
		{"mod", nil, []string{"model"}},
		{"cost", nil, []string{"usage"}},                // an alias that is the query
		{"rev", nil, []string{"review", "Code-Review"}}, // a word of the name
		{"cmpt", nil, []string{"compact"}},              // the letters in order
		{"summarizing", nil, []string{"compact"}},       // the description
		{"co", map[string]int{"context": 3, "config": 1}, []string{"context", "config", "compact", "Code-Review", "usage", "decompose"}},
		{"", map[string]int{"model": 2}, append([]string{"model"}, slices.DeleteFunc(names(cmds), func(n string) bool { return n == "model" })...)},
		{"zz", nil, nil},
	} {
		if got := names(SlashMatches(cmds, c.q, c.used)); !slices.Equal(got, c.want) {
			t.Errorf("SlashMatches(%q, %v) = %q, want %q", c.q, c.used, got, c.want)
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

	h.c.slash.click("context").Click()
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
		t.Fatal("built-ins opened the menu inside the draft")
	}
}

var midCommands = append(slices.Clone(testCommands),
	session.Command{Name: "ux-review", Description: "Review the UI", Source: "user"},
	session.Command{Name: "ship", Description: "Ship it", Source: "project"},
	session.Command{Name: "pdf", Description: "Read a PDF", Source: "plugin"},
	session.Command{Name: "term", Source: "user", Terminal: true})

// Mid-prompt only skills and custom commands are listed.
func TestMidCommands(t *testing.T) {
	if got := names(MidCommands(midCommands)); !slices.Equal(got, []string{"ux-review", "ship", "pdf"}) {
		t.Fatalf("MidCommands = %q", got)
	}
}

// A /word inside the draft opens the menu with skills only, and a pick
// replaces that word and nothing else, leaving the caret after it.
func TestSlashMidDraft(t *testing.T) {
	h := newHarness(t)
	h.snap.Commands = midCommands
	h.typ("please run /ux")
	if got := names(h.c.slash.rows); !slices.Equal(got, []string{"ux-review"}) {
		t.Fatalf("mid-draft rows %q", got)
	}
	if m := h.press(key.NameTab, 0); len(m) != 0 || h.c.Text() != "please run /ux-review " {
		t.Fatalf("pick at the end: sent %+v, text %q", m, h.c.Text())
	}

	h.c.SetText("fix /sh and then test")
	h.c.ed.SetCaret(7, 7)
	h.frame()
	h.press(key.NameReturn, 0)
	if got, _ := h.c.ed.Selection(); h.c.Text() != "fix /ship and then test" || got != 10 {
		t.Fatalf("pick in the middle: %q, caret %d", h.c.Text(), got)
	}

	h.c.SetText("")
	h.typ("see /tmp/x")
	if h.c.slash.open() {
		t.Fatal("a path opened the menu")
	}
	h.c.SetText("")
	h.typ("and/or")
	if h.c.slash.open() {
		t.Fatal("and/or opened the menu")
	}

	h.c.SetText("")
	h.typ("use /co")
	if h.c.slash.open() || h.c.slash.note != noMatchNote {
		t.Fatalf("a built-in's name mid-draft: open %v, note %q", h.c.slash.open(), h.c.slash.note)
	}
	h.press(key.NameEscape, 0)
	h.c.SetText("")
	h.typ("use /sh")
	h.press(key.NameEscape, 0)
	if h.c.slash.shown() {
		t.Fatal("Esc left the mid-draft menu up")
	}

	h.snap.Provider = "codex"
	h.c.SetText("")
	h.typ("use /sh")
	if h.c.slash.shown() {
		t.Fatal("codex got a mid-draft menu")
	}
	h.snap.Provider = "antigravity"
	h.c.SetText("")
	h.typ("then /shi")
	if got := names(h.c.slash.rows); !slices.Equal(got, []string{"ship"}) {
		t.Fatalf("antigravity mid-draft rows %q", got)
	}
	h.press(key.NameEscape, 0)
	h.snap.Provider = "claude"

	h.c.SetText("")
	h.c.SetText("use /ship now")
	if m := h.press(key.NameReturn, 0); len(m) != 1 || m[0].(proto.ChatSend).Text != "use /ship now" {
		t.Fatalf("a mid-draft /skill sent %+v", m)
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
		{false, testCommands, "/zzz", noMatchNote},
		{false, testCommands, "/tmp/x", ""}, // a path: no menu at all
	} {
		h.old, h.snap.Commands = c.old, c.cmds
		h.c.SetText("")
		h.typ(c.text)
		if h.c.slash.open() || h.c.slash.note != c.note {
			t.Fatalf("%q, old %v, %d commands: note %q, open %v", c.text, c.old, len(c.cmds), h.c.slash.note, h.c.slash.open())
		}
	}
	if m := h.press(key.NameReturn, 0); len(m) != 1 || m[0].(proto.ChatSend).Text != "/tmp/x" {
		t.Fatalf("Enter on a path sent %+v", m)
	}
	h.typ("/zzz")
	if m := h.press(key.NameReturn, 0); len(m) != 1 || m[0].(proto.ChatSend).Text != "/zzz" {
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

// The pointer moving onto a row highlights it; Down then moves on from
// there, and the pointer resting on its row does not pull it back.
func TestSlashHover(t *testing.T) {
	s := SlashMenu{rows: testCommands}
	s.hovered = "config"
	if !s.hover() || s.sel != 2 || s.picked != "config" {
		t.Fatalf("moving onto config: sel %d, picked %q", s.sel, s.picked)
	}
	s.sel = 3 // Down
	if s.hover() || s.sel != 3 {
		t.Fatalf("a resting pointer took the highlight back to %d", s.sel)
	}
	s.hovered = ""
	if s.hover() || s.sel != 3 {
		t.Fatal("the pointer leaving moved the highlight")
	}
}

// A highlighted row whose description the row cuts shows it whole above
// the menu; one that fits shows nothing more.
func TestSlashFullDescription(t *testing.T) {
	h := newHarness(t)
	long := strings.Repeat("A description much longer than any menu row could ever show. ", 4)
	h.snap.Commands = []session.Command{{Name: "short", Description: "Fits"}, {Name: "shortlong", Description: long}}
	h.typ("/short")
	if h.c.slash.full != "" {
		t.Fatalf("a description that fits showed whole: %q", h.c.slash.full)
	}
	h.press(key.NameDownArrow, 0)
	h.frame()
	if h.c.slash.full != long {
		t.Fatalf("the cut description: %q", h.c.slash.full)
	}
}

// The note at a row's right: the terminal, else where the command comes
// from; nothing for a built-in.
func TestRowNote(t *testing.T) {
	for c, want := range map[*session.Command]string{
		{Name: "config", Terminal: true, Source: "user"}: terminalNote,
		{Name: "ship", Source: "user"}:                   "Personal",
		{Name: "deploy", Source: "project"}:              "Project",
		{Name: "demo:skill", Source: "plugin"}:           "Plugin",
		{Name: "compact"}:                                "",
	} {
		if got := rowNote(*c); got != want {
			t.Errorf("rowNote(%+v) = %q, want %q", *c, got, want)
		}
	}
}

// Sending one of the chat's commands counts it, so the menu offers it
// first next time; a line that is no command counts nothing.
func TestSlashUsedOnSend(t *testing.T) {
	h := newHarness(t)
	h.snap.Commands = testCommands
	h.c.SetText("/model opus")
	h.press(key.NameReturn, 0)
	h.c.SetText("/nope")
	h.press(key.NameReturn, 0)
	if slashUsed["model"] != 1 || len(slashUsed) != 1 {
		t.Fatalf("used %v", slashUsed)
	}
	h.typ("/")
	if h.c.slash.rows[0].Name != "model" {
		t.Fatalf("the used command is not first: %q", names(h.c.slash.rows))
	}
}

// A chip's popover open over the composer keeps the / menu shut, and the
// menu comes back once the popover closes.
func TestSlashMenuUnderPopover(t *testing.T) {
	h := newHarness(t)
	h.snap.Commands = testCommands
	h.c.setMenu(h.now, true)
	h.typ("/co")
	if h.c.slash.shown() {
		t.Fatal("the / menu opened under a popover")
	}
	h.c.setMenu(h.now, false)
	h.frame()
	if !h.c.slash.open() {
		t.Fatal("the / menu stayed shut after the popover closed")
	}
}

// Two letters match names only by prefix, word or substring: no letters
// in order, no description, which would list most of the menu.
func TestSlashMatchesShortQuery(t *testing.T) {
	cmds := []session.Command{{Name: "frontend"}, {Name: "effort", Description: "from fr"}, {Name: "fork"}}
	if got := names(SlashMatches(cmds, "fr", nil)); !slices.Equal(got, []string{"frontend"}) {
		t.Fatalf("/fr lists %q", got)
	}
}

// A native command whose alias is the query comes first for each agent,
// ahead of skills the query starts or sits inside, however often those
// were used: Claude Code's /rc, Codex's /quit and /btw, agy's /settings
// and /cs.
func TestSlashMatchesAliasFirst(t *testing.T) {
	skills := []session.Command{{Name: "archify", Source: "user"}, {Name: "omarchy", Source: "user"},
		{Name: "rc-notes", Source: "user"}, {Name: "quit-smoking", Source: "user"}, {Name: "btw-log", Source: "user"},
		{Name: "settings-sync", Source: "user"}, {Name: "docs", Source: "user"}}
	used := map[string]int{"archify": 9, "rc-notes": 9, "quit-smoking": 9, "btw-log": 9, "settings-sync": 9, "docs": 9}
	for _, c := range []struct {
		agent string
		cmds  []session.Command
		q     string
		want  string
	}{
		{"claude", claude.Commands("", ""), "rc", "remote-control"},
		{"claude", claude.Commands("", ""), "bg", "background"},
		{"codex", codex.Builtins, "quit", "exit"},
		{"codex", codex.Builtins, "btw", "side"},
		{"codex", codex.Builtins, "rc", "rc"},
		{"agy", antigravity.Builtins, "settings", "config"},
		{"agy", antigravity.Builtins, "cs", "codesearch"},
	} {
		got := SlashMatches(append(slices.Clone(skills), c.cmds...), c.q, used)
		if len(got) == 0 || got[0].Name != c.want {
			t.Errorf("%s /%s lists %q first, want %s", c.agent, c.q, names(got), c.want)
		}
	}
}

// Enter on /rc picks /remote-control, not the skills /rc fuzzy-matches,
// and the next Enter sends it; a typed alias with its argument counts
// for the command it names.
func TestSlashEnterAlias(t *testing.T) {
	h := newHarness(t)
	h.snap.Commands = append([]session.Command{{Name: "archify", Source: "user"}, {Name: "omarchy", Source: "user"}}, claude.Commands("", "")...)
	slashUsed["archify"] = 5
	h.typ("/rc")
	if m := h.press(key.NameReturn, 0); len(m) != 0 || h.c.Text() != "/remote-control " {
		t.Fatalf("Enter on /rc: sent %+v, text %q", m, h.c.Text())
	}
	if m := h.press(key.NameReturn, 0); len(m) != 1 || m[0].(proto.ChatSend).Text != "/remote-control " {
		t.Fatalf("Enter after the pick sent %+v", m)
	}
	h.c.SetText("/rc phone")
	h.press(key.NameReturn, 0)
	if slashUsed["remote-control"] != 2 || slashUsed["rc"] != 0 {
		t.Fatalf("used %v", slashUsed)
	}
}

// Codex takes a skill as $name anywhere in the prompt, and reads /name
// as text past the first word: a $word opens the menu with its skills
// alone, mid-draft or first, a /word does only first, with its built-ins
// too, and a skill picked either way goes in as $name. A $ word that
// matches no skill, as $HOME, shows no menu or note. A draft with a
// $skill is sent as typed.
func TestCodexSkillMenu(t *testing.T) {
	h := newHarness(t)
	h.snap.Provider = "codex"
	h.snap.Commands = append(slices.Clone(codex.Builtins),
		session.Command{Name: "release", Description: "Cut a release", Source: "project"},
		session.Command{Name: "pages:write-page", Source: "plugin"},
		session.Command{Name: "imagegen", Source: "system"})

	h.typ("please $rel")
	if got := names(h.c.slash.rows); !slices.Equal(got, []string{"release"}) {
		t.Fatalf("mid-draft $ rows %q", got)
	}
	if m := h.press(key.NameTab, 0); len(m) != 0 || h.c.Text() != "please $release " {
		t.Fatalf("pick: sent %+v, text %q", m, h.c.Text())
	}

	h.c.SetText("")
	h.typ("$")
	if got := names(h.c.slash.rows); !slices.Equal(got, []string{"release", "pages:write-page", "imagegen"}) {
		t.Fatalf("$ alone rows %q", got)
	}
	h.press(key.NameEscape, 0)

	h.c.SetText("")
	h.typ("echo $HOME")
	if h.c.slash.shown() {
		t.Fatalf("$HOME showed the menu: note %q", h.c.slash.note)
	}

	h.c.SetText("")
	h.typ("then /rel")
	if h.c.slash.shown() {
		t.Fatal("a mid-draft /word opened Codex's menu")
	}

	h.c.SetText("")
	h.typ("/re")
	got := names(h.c.slash.rows)
	if !slices.Contains(got, "review") || !slices.Contains(got, "release") {
		t.Fatalf("first /word rows %q, want built-ins and skills", got)
	}
	h.c.slash.sel = slices.Index(got, "release")
	h.press(key.NameReturn, 0)
	if h.c.Text() != "$release " {
		t.Fatalf("a skill picked from / wrote %q", h.c.Text())
	}
	h.c.SetText("")
	h.typ("/revi")
	h.press(key.NameTab, 0)
	if h.c.Text() != "/review " {
		t.Fatalf("a built-in picked wrote %q", h.c.Text())
	}

	h.c.SetText("use $release now")
	if m := h.press(key.NameReturn, 0); len(m) != 1 || m[0].(proto.ChatSend).Text != "use $release now" || m[0].(proto.ChatSend).Skill != nil {
		t.Fatalf("a draft with a $skill sent %+v", m)
	}
}
