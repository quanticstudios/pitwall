package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func (d *Daemon) phoneUpload(pane string, data []byte) (string, error) {
	d.mu.Lock()
	prov := d.paneProvider(pane)
	d.mu.Unlock()
	if prov != model.ProviderClaude && prov != model.ProviderCodex {
		return "", errors.New("this agent does not take pictures")
	}
	if d.paneExited(pane) {
		return "", errors.New("the agent exited")
	}
	r := d.pasteImage(proto.PasteImage{Pane: pane, Data: data})
	if r.Err != "" {
		return "", errors.New(r.Err)
	}
	return filepath.Base(r.Path), nil
}

func (d *Daemon) phoneSendImage(pane, text, image string) (uint64, error) {
	if image == "" || filepath.Base(image) != image || !strings.HasPrefix(image, "image-") {
		return 0, errors.New("invalid image")
	}
	d.mu.Lock()
	prov := d.paneProvider(pane)
	if prov != model.ProviderClaude && prov != model.ProviderCodex {
		d.mu.Unlock()
		return 0, errors.New("this agent does not take pictures")
	}
	var path string
	for _, p := range d.st.Panes {
		if p.ID == pane {
			path = filepath.Join(pasteDir(p.WorkspaceID), image)
			break
		}
	}
	d.mu.Unlock()
	if path == "" {
		return 0, errors.New("the pane has closed")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return 0, errors.New("the image is unavailable")
	}
	return d.phoneSendPaths(pane, text, []string{path})
}
