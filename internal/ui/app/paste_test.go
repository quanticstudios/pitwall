package app

import (
	"bytes"
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	terminalInput "github.com/quanticstudios/pitwall/internal/input"
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// pasteBackend is an agentBackend at a given proto.Level that keeps the
// PasteImages it is sent and answers them from reply.
type pasteBackend struct {
	*agentBackend
	level  int
	images []proto.PasteImage
	reply  *proto.PasteImageResult
}

func (b *pasteBackend) Link() Link { return Link{Level: b.level} }
func (b *pasteBackend) Reconnect() {}
func (b *pasteBackend) Restart()   {}
func (b *pasteBackend) Send(msg any) error {
	if m, ok := msg.(proto.PasteImage); ok {
		b.images = append(b.images, m)
		return nil
	}
	return b.agentBackend.Send(msg)
}
func (b *pasteBackend) TakePasted(pane string) (proto.PasteImageResult, bool) {
	r := b.reply
	b.reply = nil
	if r == nil || r.Pane != pane {
		return proto.PasteImageResult{}, false
	}
	return *r, true
}

// TestHostPaste: on a --host window, a paste that finds no text reads the
// image here, sends it to the daemon and pastes the path it answers with;
// a clipboard error, a daemon error or an old daemon shows a notice and
// pastes nothing.
func TestHostPaste(t *testing.T) {
	Host = "box"
	old := readClipImage
	t.Cleanup(func() { Host, readClipImage = "", old })
	png := []byte("\x89PNG\r\n\x1a\nimage")
	var clipErr error
	readClipImage = func(context.Context) ([]byte, error) { return png, clipErr }

	b := &pasteBackend{agentBackend: newAgentBackend(model.StateAwaitingInput), level: proto.Level}
	u := &ui{b: b}
	p := &paneUI{}
	var r input.Router
	frame := func() {
		var ops op.Ops
		gtx := gl.Context{Ops: &ops, Source: r.Source(), Now: time.Now()}
		u.panePaste(gtx, p, "p", image.Rect(0, 0, 800, 600), vt.Modes{BracketedPaste: true})
	}
	// wait draws frames until the clipboard read is in and taken.
	wait := func() {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
			u.pastes.mu.Lock()
			n := len(u.pastes.done)
			u.pastes.mu.Unlock()
			if n > 0 {
				break
			}
			if time.Now().After(end) {
				t.Fatal("the clipboard read never finished")
			}
		}
		frame()
	}

	u.readImage(gl.Context{}, "p", image.Rectangle{})
	wait()
	if len(b.images) != 1 || b.images[0].Pane != "p" || !bytes.Equal(b.images[0].Data, png) || !p.pasteWait {
		t.Fatalf("sent %+v, waiting %v", b.images, p.pasteWait)
	}
	b.reply = &proto.PasteImageResult{Pane: "p", Path: "/home/me/state with spaces/pitwall/paste/tab/image-1.png"}
	frame()
	if len(b.sent) != 1 || string(b.sent[0].Data) != "\x1b[200~'/home/me/state with spaces/pitwall/paste/tab/image-1.png'\x1b[201~" || p.pasteWait {
		t.Fatalf("pasted %q, waiting %v", b.sent, p.pasteWait)
	}

	u.readImage(gl.Context{}, "p", image.Rectangle{})
	wait()
	b.reply = &proto.PasteImageResult{Pane: "p", Err: "no space left on device"}
	frame()
	if u.notice != "Can't paste the image: no space left on device" || !u.noticeErr || len(b.sent) != 1 {
		t.Errorf("daemon error: notice %q, sent %d", u.notice, len(b.sent))
	}

	clipErr = errNoImage
	u.readImage(gl.Context{}, "p", image.Rectangle{})
	wait()
	if u.notice != "Can't paste the image: the clipboard holds no text or image" || len(b.images) != 2 {
		t.Errorf("clipboard error: notice %q, images sent %d", u.notice, len(b.images))
	}

	b.level = proto.Since(proto.PasteImage{}) - 1
	u.readImage(gl.Context{}, "p", image.Rectangle{})
	if u.notice != "Can't paste the image: pitwall on box is too old; update it there" || len(u.pastes.busy) != 0 {
		t.Errorf("old daemon: notice %q, reads %d", u.notice, len(u.pastes.busy))
	}
}

// TestClipImage picks the display's tool and the best image type it
// lists, reports a missing tool, an empty clipboard and an image over the
// cap, and decodes osascript's hex on macOS.
func TestClipImage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake clipboard tools are shell scripts, and Windows has no remote windows")
	}
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	tool := func(name, script string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := func(kv ...string) func(string) string {
		return func(k string) string {
			for i := 0; i < len(kv); i += 2 {
				if kv[i] == k {
					return kv[i+1]
				}
			}
			return ""
		}
	}
	ctx := context.Background()
	wayland, x11 := env("WAYLAND_DISPLAY", "wayland-1", "DISPLAY", ":1"), env("DISPLAY", ":1")

	if _, err := clipImage(ctx, "linux", wayland); err == nil || err.Error() != "pasting an image needs wl-clipboard; install it" {
		t.Errorf("no wl-paste: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin:/bin") // head, for the fakes
	tool("wl-paste", `case "$1" in
--list-types) printf 'text/html\nimage/jpeg\nimage/png\n' ;;
*) printf 'wl %s' "$*" ;;
esac`)
	tool("powershell.exe", `printf 'windows image'`)
	if b, err := clipImage(ctx, "windows", env()); err != nil || string(b) != "windows image" {
		t.Errorf("Windows clipboard: %q, %v", b, err)
	}
	tool("xclip", `case "$*" in
*TARGETS*) printf 'TARGETS\nUTF8_STRING\nimage/gif\n' ;;
*) printf 'x %s' "$*" ;;
esac`)
	if b, err := clipImage(ctx, "linux", wayland); err != nil || string(b) != "wl --no-newline --type image/png" {
		t.Errorf("wayland: %q, %v", b, err)
	}
	if b, err := clipImage(ctx, "linux", x11); err != nil || string(b) != "x -selection clipboard -t image/gif -o" {
		t.Errorf("x11: %q, %v", b, err)
	}
	if _, err := clipImage(ctx, "linux", env()); err == nil {
		t.Error("no display read a clipboard")
	}

	tool("wl-paste", `case "$1" in
--list-types) echo text/plain ;;
esac`)
	if _, err := clipImage(ctx, "linux", wayland); !errors.Is(err, errNoImage) {
		t.Errorf("text only: %v", err)
	}
	tool("wl-paste", `echo "Nothing is copied" >&2; exit 1`)
	if _, err := clipImage(ctx, "linux", wayland); !errors.Is(err, errNoImage) {
		t.Errorf("empty clipboard: %v", err)
	}
	tool("wl-paste", `case "$1" in
--list-types) echo image/png ;;
*) head -c 21000000 /dev/zero ;;
esac`)
	if _, err := clipImage(ctx, "linux", wayland); !errors.Is(err, errTooBig) || err.Error() != "the image is over 20 MiB" {
		t.Errorf("too big: %v", err)
	}

	tool("osascript", `printf '«data PNGf89504E47»\n'`)
	if b, err := clipImage(ctx, "darwin", env()); err != nil || string(b) != "\x89PNG" {
		t.Errorf("macOS: %q, %v", b, err)
	}
	tool("osascript", `echo "execution error: Can't make some data into the expected type. (-1700)" >&2; exit 1`)
	if _, err := clipImage(ctx, "darwin", env()); !errors.Is(err, errNoImage) {
		t.Errorf("macOS, no image: %v", err)
	}
}

func TestDropFiles(t *testing.T) {
	old := Host
	t.Cleanup(func() { Host = old })
	paths := []string{"/tmp/screen shot.png", "/tmp/it's.png"}
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex, model.ProviderTerminal} {
		for _, host := range []string{"", "box"} {
			Host = host
			b := newAgentBackend(model.StateAwaitingInput)
			b.st.Panes[0].Provider = provider
			p := &paneUI{rect: image.Rect(100, 50, 500, 400)}
			u := &ui{b: b, panes: map[string]*paneUI{"p": p, "other": {rect: image.Rect(0, 50, 100, 400)}}}
			u.nav.focus = map[string]string{focusKey("", ""): "other"}
			u.dropFiles(app.FileDropEvent{Position: image.Pt(120, 80), Paths: paths})
			if host != "" && model.IsAgent(provider) {
				if !slices.Equal(p.dropFiles, paths) || len(b.sent) != 0 {
					t.Fatalf("remote drop: queue %q, sent %+v", p.dropFiles, b.sent)
				}
			} else if len(b.sent) != 1 || b.sent[0].Pane != "p" || string(b.sent[0].Data) != string(terminalInput.PastePaths(paths, vt.Modes{BracketedPaste: true})) {
				t.Fatalf("drop for %s on %q: sent %+v", provider, host, b.sent)
			}
			before := len(b.sent)
			u.dropFiles(app.FileDropEvent{Position: image.Pt(10, 10), Paths: paths})
			u.offline = true
			u.dropFiles(app.FileDropEvent{Position: image.Pt(120, 80), Paths: paths})
			if len(b.sent) != before {
				t.Fatal("drop outside a pane or behind the offline dialog sent input")
			}
		}
	}
}

func TestLocalImagePaste(t *testing.T) {
	oldHost, oldRead := Host, readClipImage
	t.Cleanup(func() { Host, readClipImage = oldHost, oldRead })
	Host = ""
	readClipImage = func(context.Context) ([]byte, error) { return []byte("local image"), nil }
	b := &pasteBackend{agentBackend: newAgentBackend(model.StateAwaitingInput), level: proto.Level}
	u := &ui{b: b}
	u.readImage(gl.Context{}, "p", image.Rectangle{})
	waitPasteRead(t, u)
	var ops op.Ops
	u.panePaste(gl.Context{Ops: &ops, Now: time.Now()}, &paneUI{}, "p", image.Rectangle{}, vt.Modes{})
	if len(b.images) != 1 || string(b.images[0].Data) != "local image" {
		t.Fatalf("local paste did not use daemon save: %+v", b.images)
	}
}

func waitPasteRead(t *testing.T, u *ui) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		u.pastes.mu.Lock()
		ready := len(u.pastes.done) > 0
		u.pastes.mu.Unlock()
		if ready {
			return
		}
		if time.Now().After(end) {
			t.Fatal("image read never completed")
		}
	}
}

func TestRemoteFilePaste(t *testing.T) {
	old := Host
	Host = "box"
	t.Cleanup(func() { Host = old })
	path := filepath.Join(t.TempDir(), "screen shot.png")
	data := []byte("image bytes")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	b := &pasteBackend{agentBackend: newAgentBackend(model.StateAwaitingInput), level: proto.Level}
	p := &paneUI{rect: image.Rect(0, 0, 500, 400)}
	u := &ui{b: b, panes: map[string]*paneUI{"p": p}}
	u.dropFiles(app.FileDropEvent{Position: image.Pt(20, 20), Paths: []string{path, path}})
	frame := func() {
		var ops op.Ops
		u.panePaste(gl.Context{Ops: &ops, Now: time.Now()}, p, "p", image.Rectangle{}, vt.Modes{BracketedPaste: true})
	}
	for i := range 2 {
		frame()
		waitPasteRead(t, u)
		frame()
		if len(b.images) != i+1 || !bytes.Equal(b.images[i].Data, data) || !p.pasteWait {
			t.Fatalf("upload %d: %+v, waiting %v", i, b.images, p.pasteWait)
		}
		frame() // while waiting for the daemon, no second upload starts
		if u.pasteBusy("p") {
			t.Fatal("started another upload before receiving the saved path")
		}
		b.reply = &proto.PasteImageResult{Pane: "p", Path: "/state/tab/screen shot.png"}
		frame()
	}
	if len(b.sent) != 2 || string(b.sent[0].Data) != "\x1b[200~'/state/tab/screen shot.png'\x1b[201~" {
		t.Fatalf("remote paths: %+v", b.sent)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("source changed: %q, %v", got, err)
	}
}

func TestReadDroppedImage(t *testing.T) {
	dir := t.TempDir()
	if _, err := readDroppedImage(dir); err == nil {
		t.Fatal("read a directory as an image")
	}
	if _, err := readDroppedImage(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("read a missing file")
	}
	path := filepath.Join(dir, "large.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(proto.MaxPasteImage + 1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readDroppedImage(path); !errors.Is(err, errTooBig) {
		t.Fatalf("oversize image: %v", err)
	}
}
