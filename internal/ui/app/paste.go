package app

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/input"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Paster is optionally implemented by a Backend: the reply to the pane's
// last proto.PasteImage, which the call takes.
type Paster interface {
	TakePasted(pane string) (proto.PasteImageResult, bool)
}

// hostPastes are the window's image pastes. The clipboard is read
// off the UI goroutine, and each read waits in done for its pane's next
// frame.
type hostPastes struct {
	mu   sync.Mutex
	busy map[string]bool      // pane, while its clipboard read runs
	done map[string]pasteRead // pane: a finished read
}

type pasteRead struct {
	data []byte
	err  error
}

// pasteTimeout bounds one clipboard read, which waits on the program that
// owns the clipboard.
const pasteTimeout = 10 * time.Second

// readClipImage reads the image on this machine's clipboard; tests
// replace it.
var readClipImage = func(ctx context.Context) ([]byte, error) {
	return clipImage(ctx, runtime.GOOS, os.Getenv)
}

// panePaste pastes an image into pane id's agent, when its view
// asks: it reads the image on this machine's clipboard, sends it to the
// daemon, which saves it on the host, and pastes the path it answers with
// as text, which Claude Code and Codex attach. A failure shows a notice
// under the pane at r and sends nothing.
func (u *ui) panePaste(gtx gl.Context, p *paneUI, id string, r image.Rectangle, m vt.Modes) {
	if !p.pasteWait && !u.pasteBusy(id) {
		if p.view.ImageWanted() {
			u.readImage(gtx, id, r)
		} else if len(p.dropFiles) > 0 {
			path := p.dropFiles[0]
			p.dropFiles = p.dropFiles[1:]
			u.startPaste(gtx, id, r, func(context.Context) ([]byte, error) {
				return readDroppedImage(path)
			})
		}
	}
	s := &u.pastes
	s.mu.Lock()
	rd, ok := s.done[id]
	delete(s.done, id)
	s.mu.Unlock()
	switch {
	case !ok:
	case rd.err != nil:
		u.showError(gtx, "Can't paste the image: "+rd.err.Error(), r)
	case !u.send(proto.PasteImage{Pane: id, Data: rd.data}):
		u.showError(gtx, "Can't paste the image: the connection to the background service is down", r)
	default:
		p.pasteWait = true
	}
	if pr, ok := u.b.(Paster); ok && p.pasteWait {
		if res, ok := pr.TakePasted(id); ok {
			p.pasteWait = false
			if len(p.dropFiles) > 0 {
				gtx.Execute(op.InvalidateCmd{})
			}
			if res.Err != "" {
				u.showError(gtx, "Can't paste the image: "+res.Err, r)
			} else {
				u.send(proto.Input{Pane: id, Data: input.PastePaths([]string{res.Path}, m)})
			}
		}
	}
}

// readImage starts reading the clipboard's image for pane id, unless a
// read for it runs or the daemon is too old to take the image.
func (u *ui) readImage(gtx gl.Context, id string, r image.Rectangle) {
	u.startPaste(gtx, id, r, readClipImage)
}

func (u *ui) pasteBusy(id string) bool {
	u.pastes.mu.Lock()
	defer u.pastes.mu.Unlock()
	_, ready := u.pastes.done[id]
	return u.pastes.busy[id] || ready
}

func (u *ui) startPaste(gtx gl.Context, id string, r image.Rectangle, read func(context.Context) ([]byte, error)) {
	if u.link().Level < proto.Since(proto.PasteImage{}) {
		message := "Can't paste the image: pitwall's background service is too old; restart pitwall"
		if Host != "" {
			message = "Can't paste the image: pitwall on " + Host + " is too old; update it there"
		}
		u.showError(gtx, message, r)
		return
	}
	s := &u.pastes
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.busy[id] {
		return
	}
	if s.busy == nil {
		s.busy, s.done = map[string]bool{}, map[string]pasteRead{}
	}
	s.busy[id] = true
	invalidate := u.invalidate
	if invalidate == nil {
		invalidate = func() {}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), pasteTimeout)
		defer cancel()
		b, err := read(ctx)
		s.mu.Lock()
		delete(s.busy, id)
		s.done[id] = pasteRead{b, err}
		s.mu.Unlock()
		invalidate()
	}()
}

// imageTypes are the clipboard types an image paste takes, best first:
// the ones Claude Code and Codex attach.
var imageTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}

var (
	errNoImage = errors.New("the clipboard holds no text or image")
	errTooBig  = fmt.Errorf("the image is over %d MiB", proto.MaxPasteImage>>20)
)

// clipImage reads the image on the clipboard of goos's display with its
// own tool, since Gio reads only text: wl-paste on Wayland, xclip on X11,
// osascript on macOS. It prefers Wayland when both run, as Gio does.
func clipImage(ctx context.Context, goos string, getenv func(string) string) ([]byte, error) {
	switch {
	case goos == "darwin":
		return macImage(ctx)
	case goos == "windows":
		// Use a memory stream: clipboard image bytes never go to a local
		// temporary file, even when the daemon is on another machine.
		return clipRun(ctx, proto.MaxPasteImage, "powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-Command", `Add-Type -AssemblyName System.Windows.Forms; $image = [System.Windows.Forms.Clipboard]::GetImage(); if ($null -eq $image) { exit 1 }; $stream = New-Object System.IO.MemoryStream; try { $image.Save($stream, [System.Drawing.Imaging.ImageFormat]::Png); $data = $stream.ToArray(); [Console]::OpenStandardOutput().Write($data, 0, $data.Length) } finally { $stream.Dispose(); $image.Dispose() }`)
	case getenv("WAYLAND_DISPLAY") != "":
		return pickImage(ctx, "wl-paste", "wl-clipboard", []string{"--list-types"},
			func(t string) []string { return []string{"--no-newline", "--type", t} })
	case getenv("DISPLAY") != "":
		return pickImage(ctx, "xclip", "xclip", []string{"-selection", "clipboard", "-t", "TARGETS", "-o"},
			func(t string) []string { return []string{"-selection", "clipboard", "-t", t, "-o"} })
	}
	return nil, errors.New("no display to read a clipboard from")
}

// pickImage lists the clipboard's types with tool and list, then reads the
// best of imageTypes with tool and get. pkg is the package with tool.
func pickImage(ctx context.Context, tool, pkg string, list []string, get func(string) []string) ([]byte, error) {
	types, err := clipRun(ctx, 64<<10, tool, list...)
	if errors.Is(err, exec.ErrNotFound) {
		return nil, fmt.Errorf("pasting an image needs %s; install it", pkg)
	}
	if err != nil {
		return nil, errNoImage // an empty clipboard fails the listing
	}
	have := strings.Fields(string(types))
	for _, t := range imageTypes {
		if slices.Contains(have, t) {
			return clipRun(ctx, proto.MaxPasteImage, tool, get(t)...)
		}
	}
	return nil, errNoImage
}

// macImage reads the clipboard as PNG, which macOS converts other image
// types to. osascript prints it as «data PNGf<hex>».
func macImage(ctx context.Context) ([]byte, error) {
	out, err := clipRun(ctx, 2*proto.MaxPasteImage+64, "osascript", "-e", "the clipboard as «class PNGf»")
	if errors.Is(err, errTooBig) {
		return nil, err
	}
	h, pre := strings.CutPrefix(strings.TrimSpace(string(out)), "«data PNGf")
	h, suf := strings.CutSuffix(h, "»")
	b, herr := hex.DecodeString(h)
	if err != nil || !pre || !suf || herr != nil {
		return nil, errNoImage // osascript fails on a clipboard with no image
	}
	if len(b) > proto.MaxPasteImage {
		return nil, errTooBig
	}
	return b, nil
}

// clipRun runs name and returns its output, or errTooBig past limit bytes.
func clipRun(ctx context.Context, limit int, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(out, int64(limit)+1))
	if len(b) > limit {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, errTooBig
	}
	if werr := cmd.Wait(); err == nil {
		err = werr
	}
	return b, err
}

// dropFiles targets the pane under the drop, even if another pane has
// keyboard focus. Remote agents need the local image uploaded first.
func (u *ui) dropFiles(e app.FileDropEvent) {
	if u.modal.kind != modalNone || u.sw.open || u.pal.open || u.welcome.on || u.offline {
		return
	}
	st := u.b.State()
	for id, p := range u.panes {
		if !e.Position.In(p.rect) || findPane(&st, id) == nil {
			continue
		}
		pn := findPane(&st, id)
		if Host != "" && model.IsAgent(pn.Provider) {
			p.dropFiles = append(p.dropFiles, e.Paths...)
		} else {
			_, m, _ := u.b.Frame(id)
			if data := input.PastePaths(e.Paths, m); len(data) > 0 {
				u.send(proto.Input{Pane: id, Data: data})
			}
		}
		return
	}
}

func readDroppedImage(path string) ([]byte, error) {
	// Check before opening, so a directory, device or FIFO is never read.
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("the dropped image is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, errors.New("the dropped image is not a regular file")
	}
	if fi.Size() > proto.MaxPasteImage {
		return nil, errTooBig
	}
	b, err := io.ReadAll(io.LimitReader(f, proto.MaxPasteImage+1))
	if len(b) > proto.MaxPasteImage {
		return nil, errTooBig
	}
	return b, err
}
