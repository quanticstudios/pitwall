package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// pasteKeep is how long a pasted image stays in the paste folder.
const pasteKeep = 24 * time.Hour

// pasteExts are the image types an agent attaches from a path, by the
// extension Claude Code and Codex look for.
var pasteExts = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// pasteImage saves m's image in the paste folder under the state directory
// and answers with its path.
func pasteImage(m proto.PasteImage) proto.PasteImageResult {
	r := proto.PasteImageResult{Pane: m.Pane}
	path, err := savePaste(filepath.Join(config.StateDir(), "paste"), m.Data, time.Now())
	if err != nil {
		r.Err = err.Error()
	}
	r.Path = path
	return r
}

// savePaste writes data to a new file in dir, mode 0600 in a folder mode
// 0700, and returns its path. It first removes the folder's files older
// than pasteKeep.
func savePaste(dir string, data []byte, now time.Time) (string, error) {
	if len(data) > proto.MaxPasteImage {
		return "", fmt.Errorf("the image is over %d MiB", proto.MaxPasteImage>>20)
	}
	ext, ok := pasteExts[http.DetectContentType(data)]
	if !ok {
		return "", errors.New("not a PNG, JPEG, GIF or WebP image")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// why: MkdirAll leaves the mode of a folder that already exists.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if fi, err := e.Info(); err == nil && now.Sub(fi.ModTime()) > pasteKeep {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	f, err := os.CreateTemp(dir, "image-*"+ext) // mode 0600
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
