package daemon

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

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
func (d *Daemon) pasteImage(m proto.PasteImage) proto.PasteImageResult {
	r := proto.PasteImageResult{Pane: m.Pane}
	// Serialize saving with tab close, so a late upload cannot recreate a
	// folder after cleanup. Derive the folder from daemon state, never input.
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range d.st.Panes {
		if p.ID != m.Pane {
			continue
		}
		if !model.IsAgent(p.Provider) {
			r.Err = "the pane is not running an agent"
			return r
		}
		path, err := savePaste(pasteDir(p.WorkspaceID), m.Data)
		if err != nil {
			r.Err = err.Error()
		}
		r.Path = path
		return r
	}
	r.Err = "the pane has closed"
	return r
}

func pasteDir(tab string) string {
	return filepath.Join(config.StateDir(), "paste", tab)
}

// savePaste writes data to a new file in dir, mode 0600 in a folder mode
// 0700, and returns its path. The tab owns the folder until it closes.
func savePaste(dir string, data []byte) (string, error) {
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
