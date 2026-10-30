package daemon

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"context"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// A pasted image lands in a 0600 file in a 0700 folder under the state
// directory, named for its type. Live tabs retain images older than a day.
func TestPasteImage(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	d := &Daemon{st: model.State{Panes: []model.Pane{{ID: "p", WorkspaceID: "tab", Provider: model.ProviderClaude}}}}
	dir := pasteDir("tab")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	old, fresh := filepath.Join(dir, "image-old.png"), filepath.Join(dir, "image-fresh.png")
	for _, f := range []string{old, fresh} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ago := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(old, ago, ago); err != nil {
		t.Fatal(err)
	}

	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	r := d.pasteImage(proto.PasteImage{Pane: "p", Data: png})
	if r.Err != "" || r.Pane != "p" || filepath.Dir(r.Path) != dir || !strings.HasSuffix(r.Path, ".png") {
		t.Fatalf("result %+v", r)
	}
	if b, err := os.ReadFile(r.Path); err != nil || !bytes.Equal(b, png) {
		t.Errorf("file %q, %v", b, err)
	}
	for f, want := range map[string]os.FileMode{r.Path: 0o600, dir: 0o700} {
		if runtime.GOOS == "windows" {
			break // Windows has no Unix modes, and no remote windows
		}
		if fi, err := os.Stat(f); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, %v; want %v", f, fi.Mode().Perm(), err, want)
		}
	}
	if _, err := os.Stat(old); err != nil {
		t.Errorf("a live tab lost its day-old paste: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a fresh paste went: %v", err)
	}

	if r := d.pasteImage(proto.PasteImage{Pane: "p", Data: []byte("plain text")}); r.Err == "" || r.Path != "" {
		t.Errorf("text saved as an image: %+v", r)
	}
	big := append(png, make([]byte, proto.MaxPasteImage)...)
	if r := d.pasteImage(proto.PasteImage{Pane: "p", Data: big}); r.Err != "the image is over 20 MiB" {
		t.Errorf("too big: %+v", r)
	}
}

// Closing a split keeps the tab's attachments. Closing a tab removes only
// that tab's attachments, and a late clipboard read cannot recreate them.
func TestPasteCleanup(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	f := &fakes{statsCalls: map[string]int{}}
	d := newDaemon(t, f.options())
	ctx := context.Background()
	for range 2 {
		must(t, d.handle(ctx, proto.NewSession{Cwd: t.TempDir()}))
	}
	st := d.state()
	first, second := st.Workspaces[0], st.Workspaces[1]
	must(t, d.handle(ctx, proto.OpenPane{WorkspaceID: first.ID, Target: st.Panes[0].ID}))
	d.mu.Lock()
	for i := range d.st.Panes {
		d.st.Panes[i].Provider = model.ProviderCodex
	}
	d.mu.Unlock()
	data := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	one := d.pasteImage(proto.PasteImage{Pane: st.Panes[0].ID, Data: data})
	two := d.pasteImage(proto.PasteImage{Pane: st.Panes[1].ID, Data: data})
	if one.Err != "" || two.Err != "" || one.Path == two.Path {
		t.Fatalf("pastes: %+v %+v", one, two)
	}
	must(t, d.handle(ctx, proto.ClosePane{Pane: st.Panes[0].ID}))
	if _, err := os.Stat(one.Path); err != nil {
		t.Fatalf("split close removed the tab's image: %v", err)
	}
	must(t, d.handle(ctx, proto.CloseTab{WorkspaceID: first.ID}))
	if _, err := os.Stat(pasteDir(first.ID)); !os.IsNotExist(err) {
		t.Errorf("closed tab's paste folder remains: %v", err)
	}
	if _, err := os.Stat(two.Path); err != nil {
		t.Errorf("other tab's paste removed: %v", err)
	}
	if r := d.pasteImage(proto.PasteImage{Pane: st.Panes[0].ID, Data: data}); r.Err == "" || r.Path != "" {
		t.Errorf("late paste accepted: %+v", r)
	}
	if _, err := os.Stat(pasteDir(first.ID)); !os.IsNotExist(err) {
		t.Errorf("late paste recreated the folder: %v", err)
	}
	must(t, d.handle(ctx, proto.ClosePane{Pane: st.Panes[1].ID}))
	if _, err := os.Stat(pasteDir(second.ID)); !os.IsNotExist(err) {
		t.Errorf("last pane close left its paste folder: %v", err)
	}
}

func TestPasteImageRejectsShell(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	d := &Daemon{st: model.State{Panes: []model.Pane{{ID: "p", WorkspaceID: "shell", Provider: model.ProviderTerminal}}}}
	if r := d.pasteImage(proto.PasteImage{Pane: "p", Data: []byte("image")}); r.Err == "" || r.Path != "" {
		t.Errorf("shell paste accepted: %+v", r)
	}
	if _, err := os.Stat(pasteDir("shell")); !os.IsNotExist(err) {
		t.Errorf("shell paste wrote a folder: %v", err)
	}
}
