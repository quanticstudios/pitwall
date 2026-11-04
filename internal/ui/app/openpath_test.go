package app

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
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
	if u.notice != "Opened a b.md:12:3" {
		t.Fatalf("unset opener notice: %q", u.notice)
	}
	if !slices.Equal(opened, []string{fileURL(filepath.Join(dir, "a b.md"))}) || len(b.Sent()) != 0 {
		t.Fatalf("unset: opener %q, messages %v", opened, b.Sent())
	}
	u.cfg.OpenPath = []string{"bat", "--paging=always"}
	for _, host := range []string{"", "box"} {
		Host = host
		u.hintAct(gtx, &st, path, true)
		wantPath := filepath.Join(dir, "a b.md")
		if host != "" {
			wantPath = "a b.md"
		}
		want := proto.OpenPopup{WorkspaceID: "w", Cmd: []string{"bat", "--paging=always"}, Dir: dir, Path: wantPath}
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
	if u.pal.open || len(got) != 1 || got[0].Path != filepath.Join(pn.Cwd, "b b.go") || got[0].Dir != pn.Cwd || len(sentOf[proto.Input](b)) != 0 {
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
	u.nav.keys = conventional
	u.pal.open = true
	u.paths = pathPick{pane: "p", dir: st.Panes[0].Cwd, files: []string{"a.md"}, query: "\x00"}
	u.pathPickKey(&st, press(key.NameReturn, key.ModShift))
	if !strings.Contains(u.paths.openErr, "Level 59") || u.paths.err != "" || len(u.pathRows()) != 1 {
		t.Fatalf("older daemon picker: %+v", u.paths)
	}
	u.pathPickKey(&st, press(key.NameDownArrow, 0))
	if u.paths.openErr != "" {
		t.Fatalf("error stays after a key: %q", u.paths.openErr)
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

func TestHintMissingPathCopies(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b}
	u.cfg.OpenPath = []string{"less"}
	dir := t.TempDir()
	st := model.State{Panes: []model.Pane{{ID: "p", WorkspaceID: "w", Cwd: dir}}}
	var ops op.Ops
	for _, path := range []string{"and/or", "./missing.md", "./"} {
		u.hintAct(gl.Context{Ops: &ops}, &st, hintTarget{"p", term.HintMatch{Kind: term.HintPath, Text: path}}, true)
		if len(b.Sent()) != 0 || u.notice != copiedText(path) {
			t.Fatalf("%q: notice %q, messages %+v", path, u.notice, b.Sent())
		}
	}
}

func TestPathPickLiteralPath(t *testing.T) {
	defer func(f func(string) error) { hintOpen = f }(hintOpen)
	for _, name := range []string{filepath.Join("~", "notes.md"), "v1:2"} {
		if runtime.GOOS == "windows" && name == "v1:2" {
			continue
		}
		for _, popup := range []bool{false, true} {
			dir, changed := t.TempDir(), t.TempDir()
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			// Both files exist, so suffix stripping would open the wrong one.
			if name == "v1:2" {
				if err := os.WriteFile(filepath.Join(dir, "v1"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var opened string
			hintOpen = func(s string) error { opened = s; return nil }
			b := NewFakeBackend()
			u := &ui{b: b, nav: nav{keys: conventional}}
			if popup {
				u.cfg.OpenPath = []string{"less"}
			}
			st := model.State{Panes: []model.Pane{{ID: "p", WorkspaceID: "w", Cwd: changed}}}
			u.pal.open = true
			u.paths = pathPick{pane: "p", dir: dir, files: []string{name}, query: "\x00"}
			u.pathPickKey(&st, press(key.NameReturn, key.ModShift))
			if popup {
				got := sentOf[proto.OpenPopup](b)
				if len(got) != 1 || got[0].Path != path {
					t.Fatalf("%q: popup %+v, want %q", name, got, path)
				}
			} else if opened != fileURL(path) {
				t.Fatalf("%q: opener %q, want %q", name, opened, fileURL(path))
			}
		}
	}
}
