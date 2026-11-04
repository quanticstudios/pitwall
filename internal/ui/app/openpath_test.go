package app

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/term"
)

func TestHintOpenPath(t *testing.T) {
	defer func(f func(string) error) { hintOpen = f }(hintOpen)
	defer func(h string) { Host = h }(Host)
	var opened []string
	hintOpen = func(s string) error { opened = append(opened, s); return nil }
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a b.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	b := NewFakeBackend()
	u := &ui{b: b}
	st := model.State{Panes: []model.Pane{{ID: "p", WorkspaceID: "w", Cwd: dir}}}
	var ops op.Ops
	gtx := gl.Context{Ops: &ops}
	path := hintTarget{"p", term.HintMatch{Kind: term.HintPath, Text: "a b.md:12:3"}}
	u.hintAct(gtx, &st, path, true)
	if !slices.Equal(opened, []string{fileURL(filepath.Join(dir, "a b.md"))}) || len(b.Sent()) != 0 {
		t.Fatalf("unset: opener %q, messages %v", opened, b.Sent())
	}
	u.cfg.OpenPath = []string{"bat", "--paging=always"}
	for _, host := range []string{"", "box"} {
		Host = host
		u.hintAct(gtx, &st, path, true)
		want := proto.OpenPopup{WorkspaceID: "w", Cmd: []string{"bat", "--paging=always"}, Dir: dir, Path: "a b.md"}
		got := sentOf[proto.OpenPopup](b)
		if len(got) == 0 || !reflect.DeepEqual(got[len(got)-1], want) {
			t.Fatalf("host %q: popups %+v", host, got)
		}
	}
	u.hintAct(gtx, &st, hintTarget{"p", term.HintMatch{Kind: term.HintURL, Text: "https://example.com"}}, true)
	if !slices.Equal(opened, []string{fileURL(filepath.Join(dir, "a b.md")), "https://example.com"}) {
		t.Fatalf("opener %q", opened)
	}
}

func TestPathPickOpen(t *testing.T) {
	b := NewFakeBackend()
	st := b.State()
	u := &ui{b: b, nav: nav{keys: conventional}}
	u.nav.sync(&st)
	pn := findPane(&st, u.nav.focused())
	u.cfg.OpenPath = []string{"bat", "--paging=always"}
	u.pal.open, u.pal.sel = true, 1
	u.paths = pathPick{pane: pn.ID, dir: pn.Cwd, files: []string{"a.go", "b b.go"}, marked: []string{"a.go"}, query: "\x00"}
	u.pathPickKey(&st, press(key.NameReturn, key.ModShift))
	got := sentOf[proto.OpenPopup](b)
	if u.pal.open || len(got) != 1 || got[0].Path != "b b.go" || got[0].Dir != pn.Cwd || len(sentOf[proto.Input](b)) != 0 {
		t.Fatalf("open %v, popups %+v, messages %+v", u.pal.open, got, b.Sent())
	}
	if !slices.Contains(pathHints(1), [2]string{"Shift+Enter", "open"}) {
		t.Fatal("footer lacks open key")
	}
}

func TestOpenPathOlderDaemon(t *testing.T) {
	b := &linked{FakeBackend: NewFakeBackend(), link: Link{Level: proto.PopupPathLevel - 1}}
	u := &ui{b: b}
	u.cfg.OpenPath = []string{"bat"}
	st := model.State{Panes: []model.Pane{{ID: "p", WorkspaceID: "w", Cwd: t.TempDir()}}}
	if err := u.openPath(&st, "p", "a.md"); err == nil || len(b.Sent()) != 0 {
		t.Fatalf("older daemon: %v, messages %+v", err, b.Sent())
	}
}

func TestPathPickOpener(t *testing.T) {
	defer func(f func(string) error) { hintOpen = f }(hintOpen)
	var opened string
	hintOpen = func(s string) error { opened = s; return nil }
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "my file.md"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	b := NewFakeBackend()
	u := &ui{b: b, nav: nav{keys: conventional}}
	st := model.State{Panes: []model.Pane{{ID: "p", Cwd: dir}}}
	u.pal.open = true
	u.paths = pathPick{pane: "p", dir: dir, files: []string{"my file.md"}, query: "\x00"}
	u.pathPickKey(&st, press(key.NameReturn, key.ModShift))
	if u.pal.open || opened != fileURL(filepath.Join(dir, "my file.md")) || len(b.Sent()) != 0 {
		t.Fatalf("open %v, opener %q, messages %+v", u.pal.open, opened, b.Sent())
	}
}
