package app

import (
	"context"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	pinput "github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestRankPaths needs every word to match, puts a match in the base name
// first and a shorter path on a tie, and caps the rows but counts them all.
func TestRankPaths(t *testing.T) {
	files := []string{"approve/x.go", "internal/ui/app/app.go", "internal/ui/app/apps.go", "app.go", "docs/usage.md"}
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"", files[:3]},
		{"app.go", []string{"app.go", "internal/ui/app/app.go", "internal/ui/app/apps.go"}},
		{"ui apps", []string{"internal/ui/app/apps.go"}},
		{"usage", []string{"docs/usage.md"}},
		{"zzz", nil},
	} {
		got, n := rankPaths(files, c.query, 3)
		if !slices.Equal(got, c.want) {
			t.Errorf("%q: %v, want %v", c.query, got, c.want)
		}
		if c.query == "" && n != len(files) {
			t.Errorf("no query counts %d of %d", n, len(files))
		}
	}
}

// TestListPaths lists a repository's tracked and untracked files but not
// its ignored ones, relative to a subfolder, and walks a plain folder
// past its dot folders, up to the cap.
func TestListPaths(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	write := func(p string) {
		t.Helper()
		p = filepath.Join(repo, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-C", repo}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	write("sub/tracked.go")
	write("sub/new file.go")
	write("sub/build.log")
	write(".gitignore")
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("add", "sub/tracked.go", ".gitignore")
	got, err := listPaths(context.Background(), filepath.Join(repo, "sub"))
	if want := []string{"new file.go", "tracked.go"}; err != nil || !slices.Equal(got, want) {
		t.Errorf("repo: %q, %v; want %q", got, err, want)
	}

	plain := t.TempDir()
	for _, p := range []string{"a.txt", "d/b.txt", ".cache/c.txt", ".env"} {
		os.MkdirAll(filepath.Join(plain, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(plain, p), nil, 0o644)
	}
	got, err = listPaths(context.Background(), plain)
	if want := []string{".env", "a.txt", filepath.Join("d", "b.txt")}; err != nil || !slices.Equal(got, want) {
		t.Errorf("walk: %q, %v; want %q", got, err, want)
	}
	// .cache counts as an entry: three entries are .env and a.txt.
	if got, _ := walkPaths(plain, 3); !slices.Equal(got, []string{".env", "a.txt"}) {
		t.Errorf("a cap of 3 entries listed %q", got)
	}
	if _, err := listPaths(context.Background(), filepath.Join(plain, "gone")); err == nil {
		t.Error("a missing folder listed")
	}
}

// TestPathPickKeys opens the picker on the focused pane with its key,
// marks two files with Tab and Ctrl+Space, and Enter types both paths at
// the pane's cursor, quoting the one with a space; Enter alone types the
// highlighted one; Escape types nothing.
func TestPathPickKeys(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
	var router input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		router.Frame(&ops)
	}
	pressKey := func(e key.Event) {
		router.Queue(e)
		frame()
		router.Queue(key.Event{Name: e.Name, State: key.Release, Modifiers: e.Modifiers})
		frame()
	}
	typeText := func(s string) {
		for _, r := range strings.ToUpper(s) {
			pressKey(press(key.Name(string(r)), 0))
		}
	}
	frame()
	frame()
	pane := u.nav.focused()
	if pane == "" {
		t.Fatal("no focused pane")
	}
	dir := t.TempDir()
	for _, f := range []string{"main.go", "notes.txt", "my file.md"} {
		os.WriteFile(filepath.Join(dir, f), nil, 0o644)
	}
	b.mu.Lock()
	for i := range b.st.Panes {
		b.st.Panes[i].Cwd = dir
	}
	b.mu.Unlock()
	open := func() {
		t.Helper()
		pressKey(press("I", key.ModCtrl|key.ModShift))
		if !u.pal.open || !u.pal.files || u.paths.pane != pane {
			t.Fatalf("Ctrl+Shift+I: open %v, files %v, pane %q", u.pal.open, u.pal.files, u.paths.pane)
		}
		for range 200 {
			if u.pathRows(); u.paths.files != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("the listing never finished")
	}
	inputs := func() int {
		return len(slices.DeleteFunc(b.Sent(), func(m any) bool { _, ok := m.(proto.Input); return !ok }))
	}
	_, modes, _ := b.Frame(pane)

	open()
	pressKey(press(key.NameTab, 0))             // main.go, then my file.md
	pressKey(press(key.NameSpace, key.ModCtrl)) // my file.md
	if !slices.Equal(u.paths.marked, []string{"main.go", "my file.md"}) {
		t.Fatalf("marked %q", u.paths.marked)
	}
	pressKey(press(key.NameReturn, 0))
	in, _ := lastSent[proto.Input](b)
	if want := string(pinput.TypePaths([]string{"main.go", "my file.md"}, modes)); u.pal.open || in.Pane != pane || string(in.Data) != want {
		t.Fatalf("Enter with two marked: open %v, sent %q to %q, want %q", u.pal.open, in.Data, in.Pane, want)
	}

	open()
	if len(u.paths.marked) != 0 {
		t.Fatalf("marks survived: %q", u.paths.marked)
	}
	typeText("notes")
	pressKey(press(key.NameReturn, 0))
	if in, _ := lastSent[proto.Input](b); string(in.Data) != "notes.txt" && string(in.Data) != "\x1b[200~notes.txt\x1b[201~" {
		t.Fatalf("Enter: sent %q", in.Data)
	}

	open()
	n := inputs()
	pressKey(press(key.NameEscape, 0))
	if u.pal.open || inputs() != n {
		t.Fatalf("Escape: open %v, %d inputs after %d", u.pal.open, inputs(), n)
	}
}
