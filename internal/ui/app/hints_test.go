package app

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/term"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestAssignHintLabels checks labels stay on their text while it shows,
// a new text takes a free label, and a set that grows past one letter
// starts over with prefix-free labels.
func TestAssignHintLabels(t *testing.T) {
	a := assignHintLabels([]string{"x", "y", "z"}, nil)
	if a["x"] != "a" || a["y"] != "s" || a["z"] != "d" {
		t.Fatalf("first: %v", a)
	}
	b := assignHintLabels([]string{"new", "z", "x"}, a)
	if b["z"] != "d" || b["x"] != "a" || b["new"] != "s" {
		t.Errorf("kept: %v", b)
	}
	if c := assignHintLabels([]string{"x", "y", "z"}, a); c["x"] != a["x"] || c["y"] != a["y"] || c["z"] != a["z"] {
		t.Errorf("same screen, new labels: %v", c)
	}
	var many []string
	for i := range len(term.HintAlphabet) + 5 {
		many = append(many, string(rune('A'+i)))
	}
	d := assignHintLabels(many, a)
	seen := map[string]bool{}
	for _, l := range d {
		if seen[l] {
			t.Fatalf("label %q twice: %v", l, d)
		}
		seen[l] = true
		for m := range d {
			if o := d[m]; o != l && strings.HasPrefix(o, l) {
				t.Fatalf("%q is a prefix of %q", l, o)
			}
		}
	}
}

func TestGithubURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:quanticstudios/pitwall.git":       "https://github.com/quanticstudios/pitwall",
		"https://github.com/quanticstudios/pitwall":       "https://github.com/quanticstudios/pitwall",
		"ssh://git@github.com/quanticstudios/pitwall.git": "https://github.com/quanticstudios/pitwall",
		"https://gitlab.com/quanticstudios/pitwall.git":   "",
		"git@github.com:quanticstudios/pitwall/extra.git": "",
		"https://github.com/quanticstudios/pitwall.git/":  "https://github.com/quanticstudios/pitwall",
		"quanticstudios:quanticstudios/pitwall.git":       "",
		"": "",
	} {
		if got := githubURL(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// TestHintURL checks what Shift and a label opens: a URL as it is, www.
// on https, a path that is here as a file URL without its line, a #number
// and a commit on the tab's GitHub repo, and nothing for the rest.
func TestHintURL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	st := model.State{
		Workspaces: []model.Workspace{{ID: "w"}},
		Panes:      []model.Pane{{ID: "p", WorkspaceID: "w", Cwd: dir}},
		PRs:        map[string]model.PR{"w": {Number: 9, URL: "https://github.com/o/r/pull/9"}},
	}
	u := &ui{}
	u.nav.workspace = "w"
	for _, c := range []struct{ kind, text, want string }{
		{term.HintURL, "https://example.com/a", "https://example.com/a"},
		{term.HintURL, "www.example.com", "https://www.example.com"},
		{term.HintURL, "file:///etc/hosts", ""},
		{term.HintPath, "main.go:12:3", "file://" + filepath.ToSlash(filepath.Join(dir, "main.go"))},
		{term.HintPath, filepath.Join(dir, "main.go"), "file://" + filepath.ToSlash(filepath.Join(dir, "main.go"))},
		{term.HintPath, "missing.go:1", ""},
		{term.HintNumber, "#279", "https://github.com/o/r/pull/279"},
		{term.HintHash, "e5965229", "https://github.com/o/r/commit/e5965229"},
		{term.HintIP, "10.0.0.1", ""},
		{term.HintCustom, "ENG-2962", ""},
	} {
		got := u.hintURL(&st, hintTarget{"p", term.HintMatch{Kind: c.kind, Text: c.text}})
		if got != c.want {
			t.Errorf("%s %q: %q, want %q", c.kind, c.text, got, c.want)
		}
	}
}

// TestHintsMode drives hints mode through Gio's router: the hints key
// labels the focused pane's URLs, a letter that starts no label does
// nothing, a label copies its text without reaching the pane, Shift and a
// label opens it, and Escape leaves.
func TestHintsMode(t *testing.T) {
	var opened []string
	defer func(f func(string) error) { hintOpen = f }(hintOpen)
	hintOpen = func(s string) error { opened = append(opened, s); return nil }

	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	typed := func() (s string) {
		for _, m := range b.Sent() {
			if m, ok := m.(proto.Input); ok {
				s += string(m.Data)
			}
		}
		return s
	}
	frame()
	frame()
	pane := u.nav.focused()
	open := func() {
		t.Helper()
		r.Queue(press("G", key.ModCtrl|key.ModShift))
		frame()
		frame()
		if !u.hints.on || u.hints.pane != pane {
			t.Fatalf("hints on %v on %q, want %q", u.hints.on, u.hints.pane, pane)
		}
	}
	open()
	// The fake screen's URLs, the lower one first.
	if got := u.hints.targets["a"].Text; got != "www.example.org" {
		t.Fatalf("label a: %q, targets %v", got, u.hints.targets)
	}
	if got := u.hints.targets["s"].Text; got != "https://example.com/guide" {
		t.Fatalf("label s: %q", got)
	}
	r.Queue(press("Q", 0), key.EditEvent{Text: "q"})
	frame()
	if !u.hints.on || u.hints.typed != "" {
		t.Fatalf("q starts no label: on %v typed %q", u.hints.on, u.hints.typed)
	}
	r.Queue(press("S", 0), key.EditEvent{Text: "s"})
	frame()
	frame()
	if u.hints.on || u.notice != "Copied 25 characters" {
		t.Fatalf("s: on %v, notice %q", u.hints.on, u.notice)
	}
	if s := typed(); s != "" {
		t.Fatalf("the pane got %q", s)
	}

	open()
	r.Queue(press("A", key.ModShift), key.EditEvent{Text: "A"})
	frame()
	if u.hints.on || len(opened) != 1 || opened[0] != "https://www.example.org" {
		t.Fatalf("Shift+a: on %v, opened %q", u.hints.on, opened)
	}

	open()
	r.Queue(press(key.NameEscape, 0))
	frame()
	if u.hints.on {
		t.Fatal("Escape left hints on")
	}
	frame()
	r.Queue(press("Q", 0), key.EditEvent{Text: "q"})
	frame()
	if s := typed(); s != "q" {
		t.Fatalf("after Escape the pane got %q", s)
	}
}

// TestHintsAllPanes checks hints_all labels every pane the tab shows, the
// focused one first, with the same label for the same text in each, and
// that the hints key on its own goes back to the focused pane.
func TestHintsAllPanes(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}}
	var r input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		r.Frame(&ops)
	}
	frame()
	frame()
	pane := u.nav.focused() // the fake's first tab has three panes
	r.Queue(press("G", key.ModCtrl|key.ModAlt|key.ModShift))
	frame()
	frame()
	if !u.hints.on || !u.hints.all || len(u.hints.panes) < 2 || u.hints.panes[0] != pane {
		t.Fatalf("hints_all: on %v all %v panes %v, focused %q", u.hints.on, u.hints.all, u.hints.panes, pane)
	}
	st := b.State()
	ws := findWorkspace(&st, u.nav.workspace)
	got := u.layoutHints(&st, ws.ID, panesOf(shownTab(ws).Layout))
	for _, id := range u.hints.panes {
		var labels []string
		for _, h := range got[id] {
			labels = append(labels, h.Label)
		}
		if strings.Join(labels, " ") != "a s" {
			t.Errorf("pane %s labels %q, want a s", id, labels)
		}
	}
	r.Queue(press("G", key.ModCtrl|key.ModShift))
	frame()
	frame()
	if !u.hints.on || u.hints.all || len(u.hints.panes) != 1 {
		t.Fatalf("back to one pane: on %v all %v panes %v", u.hints.on, u.hints.all, u.hints.panes)
	}
}

func TestHintsPopup(t *testing.T) {
	b, u, frame, queue := formWindow(t)
	b.mu.Lock()
	b.st = withPopup(b.st, "pop")
	b.mu.Unlock()
	frame()
	frame()
	queue(press("G", key.ModCtrl|key.ModShift))
	if !u.hints.on || u.hints.pane != "pop" || len(u.hints.targets) == 0 {
		t.Fatalf("popup hints: on %v pane %q targets %v", u.hints.on, u.hints.pane, u.hints.targets)
	}
	queue(press("G", key.ModCtrl|key.ModAlt|key.ModShift))
	if !u.hints.all || len(u.hints.panes) != 4 || u.hints.panes[0] != "pop" {
		t.Fatalf("all panes with popup: %v", u.hints.panes)
	}
	queue(press("A", 0), key.EditEvent{Text: "a"})
	if u.hints.on || u.notice != "Copied 15 characters" {
		t.Fatalf("popup copy: on %v notice %q", u.hints.on, u.notice)
	}
	if got := sentOf[proto.Input](b); len(got) != 0 {
		t.Fatalf("popup got hint keys: %+v", got)
	}
}
