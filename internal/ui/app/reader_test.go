package app

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// TestOpenReader checks a Ctrl+clicked markdown path opens in the reader
// from the pane's folder, a web link does not, and a window on a Host
// says the file is not here.
func TestOpenReader(t *testing.T) {
	defer func(h string) { Host = h }(Host)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := &ui{b: NewFakeBackend()}
	u.nav.workspace = "w"
	st := model.State{Panes: []model.Pane{{ID: "p", WorkspaceID: "w", Cwd: dir}}}
	var ops op.Ops
	gtx := gl.Context{Ops: &ops}
	r := image.Rect(0, 0, 100, 100)
	if u.openReader(gtx, &st, "p", "https://example.com/README.md", r) {
		t.Error("a web link opened in the reader")
	}
	Host = "box"
	if !u.openReader(gtx, &st, "p", "README.md", r) || u.reader.On() || !strings.Contains(u.notice, "on box") {
		t.Errorf("host: reader %v, notice %q", u.reader.On(), u.notice)
	}
	Host = ""
	u.boardOn = true
	if !u.openReader(gtx, &st, "p", "./README.md:4", r) || !u.reader.On() || u.reader.Path() != filepath.Join(dir, "README.md") || u.readerWS != "w" || u.boardOn {
		t.Errorf("local: reader %v at %q, tab %q, board %v", u.reader.On(), u.reader.Path(), u.readerWS, u.boardOn)
	}
	u.reader.Close()
	if !u.openReader(gtx, &st, "p", "missing.md", r) || u.reader.On() || !strings.Contains(u.notice, "Cannot open missing.md") {
		t.Errorf("missing: reader %v, notice %q", u.reader.On(), u.notice)
	}
}

// paneHosts is a window of two machines whose pane "far" is on box.
type paneHosts struct{ *FakeBackend }

func (paneHosts) SessionHost(string) string { return "" }
func (paneHosts) Hosts() []sidebar.HostInfo {
	return []sidebar.HostInfo{{Up: true}, {Name: "box", Up: true}}
}
func (paneHosts) HostOf(id string) string {
	if id == "far" {
		return "box"
	}
	return ""
}

// TestReaderUsesPaneHost: with a local tab shown, a markdown path in a
// peeked pane of another machine does not open the file of that path
// here, even when there is one.
func TestReaderUsesPaneHost(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	u := &ui{b: paneHosts{NewFakeBackend()}}
	u.nav.workspace, u.nav.peek = "w", "far"
	st := model.State{Panes: []model.Pane{{ID: "p", WorkspaceID: "w", Cwd: dir}, {ID: "far", WorkspaceID: "x", Cwd: dir}}}
	var ops op.Ops
	gtx := gl.Context{Ops: &ops}
	r := image.Rect(0, 0, 100, 100)
	if !u.openReader(gtx, &st, "far", "README.md", r) || u.reader.On() || !strings.Contains(u.notice, "on box") {
		t.Errorf("remote pane: reader %v, notice %q", u.reader.On(), u.notice)
	}
	defer u.reader.Close()
	if !u.openReader(gtx, &st, "p", "README.md", r) || !u.reader.On() {
		t.Errorf("local pane: reader %v, notice %q", u.reader.On(), u.notice)
	}
}

// TestReaderHidesPanes: the pane under the reader is not seen, so its
// activity stays unseen and its bell rings, and the board, the monitor
// and the review view each close the reader rather than open under it.
func TestReaderHidesPanes(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	u.winFocused = true
	md := filepath.Join(t.TempDir(), "a.md")
	if err := os.WriteFile(md, []byte("# A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lastSee := func() string {
		out := "none"
		for _, m := range u.b.(*FakeBackend).Sent() {
			if s, ok := m.(proto.SeePane); ok {
				out = s.Pane
			}
		}
		return out
	}
	keys(press("Q", 0))
	if got := lastSee(); got != u.nav.focused() {
		t.Fatalf("panes shown: last SeePane %q, focused %q", got, u.nav.focused())
	}
	open := func() {
		t.Helper()
		if err := u.reader.Open(md, ""); err != nil {
			t.Fatal(err)
		}
		u.readerWS = u.nav.workspace
	}
	open()
	defer u.reader.Close()
	keys(press("Q", 0))
	if got := lastSee(); got != "" {
		t.Errorf("reader shown: last SeePane %q, want none", got)
	}
	keys(press("M", key.ModCtrl|key.ModShift))
	if u.reader.On() || !u.boardOn {
		t.Errorf("board: reader %v, board %v", u.reader.On(), u.boardOn)
	}
	u.boardOn = false
	open()
	u.mon.on = false
	u.toggleMonitor()
	if u.reader.On() || !u.mon.on {
		t.Errorf("monitor: reader %v, monitor %v", u.reader.On(), u.mon.on)
	}
}
