package app

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

type commandBackend struct {
	*FakeBackend
	results []proto.CommandResult
}

func (b *commandBackend) CommandResults() []proto.CommandResult {
	r := b.results
	b.results = nil
	return r
}
func (b *commandBackend) Scroll(string) (int, int, uint64) { return 0, 1000, 500 }

func TestCommandBrowserKeysAndPalette(t *testing.T) {
	for _, preset := range config.Presets {
		keys := config.Preset(preset)
		chord := keys.Global["scrollback_browser"]
		if len(chord) != 1 || keys.Action(press(chord[0].Name, chord[0].Mods)) != "scrollback_browser" {
			t.Fatalf("%s keys %v", preset, chord)
		}
		entries := rank(paletteEntries(keys), "browse command blocks")
		if len(entries) == 0 || entries[0].action.Name != "scrollback_browser" {
			t.Fatalf("%s palette %v", preset, names(entries))
		}
	}
	b := &commandBackend{FakeBackend: NewFakeBackend()}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
	var router input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		g := gl.Context{Ops: &ops, Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1000, 700)), Now: time.Now()}
		u.layout(g)
		router.Frame(&ops)
	}
	pressKey := func(e key.Event) {
		router.Queue(e)
		frame()
		router.Queue(key.Event{Name: e.Name, State: key.Release, Modifiers: e.Modifiers})
		frame()
	}
	frame()
	frame()
	pressKey(press("J", key.ModCtrl|key.ModShift))
	if !u.browser.open || u.browser.pane != u.nav.focused() {
		t.Fatal("browser did not open on focused pane")
	}
	id, pane := u.browser.id, u.browser.pane
	b.results = []proto.CommandResult{
		{ID: id - 1, Pane: pane, Integrated: false},
		{ID: id, Pane: pane, Integrated: true, Blocks: []vt.CommandBlock{{Prompt: vt.Pos{Line: 100}, Command: "true", Finished: true, StatusKnown: true}, {Prompt: vt.Pos{Line: 200}, Command: "false", Output: "failed\n", Finished: true, StatusKnown: true, ExitCode: 1}}},
	}
	frame()
	if !u.browser.have || u.browser.sel != 1 || len(u.browser.result.Blocks) != 2 {
		t.Fatalf("reply %+v", u.browser.result)
	}
	pressKey(press(key.NameUpArrow, 0))
	if u.browser.sel != 0 {
		t.Fatal("up did not select older command")
	}
	pressKey(press(key.NameUpArrow, 0))
	if u.browser.sel != 0 {
		t.Fatal("up passed oldest")
	}
	pressKey(press(key.NameDownArrow, 0))
	pressKey(press(key.NameEscape, 0))
	if u.browser.open {
		t.Fatal("escape did not close")
	}
	for _, m := range b.Sent() {
		if in, ok := m.(proto.Input); ok && len(in.Data) > 0 {
			t.Errorf("browser key reached PTY: %q", in.Data)
		}
	}

	pressKey(press("J", key.ModCtrl|key.ModShift))
	if u.browser.id <= id {
		t.Fatal("request id reused")
	}
	pressKey(press(key.NameEscape, 0))
	if u.browser.open {
		t.Fatal("escape did not close")
	}
}

func TestCommandBrowserCopyKeys(t *testing.T) {
	u := &ui{browser: commandBrowser{open: true, result: proto.CommandResult{Blocks: []vt.CommandBlock{{Command: "echo hi", Output: "hi\n"}}}}}
	var ops op.Ops
	var router input.Router
	g := gl.Context{Ops: &ops, Source: router.Source()}
	for _, tc := range []struct {
		name       key.Name
		mods       key.Modifiers
		part, want string
	}{
		{"C", 0, "command", "echo hi"}, {"Y", 0, "output", "hi\n"}, {"Y", key.ModShift, "both", "echo hi\nhi\n"},
	} {
		u.commandBrowserKey(g, press(tc.name, tc.mods))
		if u.browser.copy != tc.part || u.browser.result.Blocks[0].Copy(u.browser.copy) != tc.want {
			t.Fatalf("key %s copy %q", tc.name, u.browser.copy)
		}
		u.copyCommandBlock(g)
		router.Frame(&ops)
		if _, got, ok := router.WriteClipboard(); !ok || string(got) != tc.want {
			t.Fatalf("clipboard %q, want %q", got, tc.want)
		}
		if !strings.HasPrefix(u.browser.notice, "Copied") {
			t.Fatal("missing copy confirmation")
		}
	}
	u.browser.result.Blocks[0].Output = ""
	u.browser.copy = "output"
	u.copyCommandBlock(g)
	router.Frame(&ops)
	if _, _, ok := router.WriteClipboard(); ok {
		t.Fatal("empty output changed clipboard")
	}
	if u.browser.notice != "No output to copy" {
		t.Fatal(u.browser.notice)
	}
}

func TestCommandBrowserEmptyAndOldDaemon(t *testing.T) {
	b := &linked{FakeBackend: NewFakeBackend(), link: Link{Level: proto.CommandsLevel - 1}}
	u := &ui{b: b}
	st := b.State()
	u.nav.sync(&st)
	u.openCommandBrowser()
	if !strings.Contains(u.commandBrowserStatus(), "newer background service") {
		t.Fatal(u.commandBrowserStatus())
	}
	for _, m := range b.Sent() {
		if _, ok := m.(proto.Commands); ok {
			t.Fatal("sent new message to old daemon")
		}
	}
	u.b = NewFakeBackend()
	u.browser.have = true
	if !strings.Contains(u.commandBrowserStatus(), "shell integration") {
		t.Fatal(u.commandBrowserStatus())
	}
	u.browser.result.Integrated = true
	if !strings.Contains(u.commandBrowserStatus(), "No commands recorded") {
		t.Fatal(u.commandBrowserStatus())
	}
	if !strings.HasSuffix(shellIntegrationURL, "docs/usage.md#shell-integration") {
		t.Fatal(shellIntegrationURL)
	}
}

type unmarkedBackend struct{ *commandBackend }

func (*unmarkedBackend) PromptMarks(string) bool { return false }

func TestPromptJumpExplainsMissingIntegration(t *testing.T) {
	b := &unmarkedBackend{&commandBackend{FakeBackend: NewFakeBackend()}}
	u := &ui{b: b}
	st := b.State()
	u.nav.sync(&st)
	u.scrollPane(u.nav.focused(), 0, 1)
	if !u.browser.open {
		t.Fatal("prompt jump did not open integration help")
	}
	for _, m := range b.Sent() {
		if _, ok := m.(proto.Scroll); ok {
			t.Fatal("unmarked pane scrolled")
		}
	}
}

func TestCommandMetadata(t *testing.T) {
	for _, tc := range []struct {
		b    vt.CommandBlock
		want string
	}{
		{vt.CommandBlock{}, "running · start unknown · 0 bytes"},
		{vt.CommandBlock{Finished: true}, "exit unknown · start unknown · 0 bytes"},
		{vt.CommandBlock{Finished: true, StatusKnown: true, ExitCode: 1, Output: "界"}, "exit 1 · start unknown · 3 bytes"},
	} {
		if got := commandBlockMeta(tc.b); got != tc.want {
			t.Errorf("meta %q, want %q", got, tc.want)
		}
	}
}
