package daemon

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/proto"
)

func (d *Daemon) scrollback(m proto.Scrollback) any {
	if m.Lines < 0 {
		return proto.Error{Message: "lines must be non-negative"}
	}
	text, err := d.scrollbackText(m.Pane)
	if err != nil {
		return proto.Error{Message: err.Error()}
	}
	if m.Lines > 0 {
		end := len(strings.TrimSuffix(text, "\n"))
		for range m.Lines {
			i := strings.LastIndexByte(text[:end], '\n')
			if i < 0 {
				end = -1
				break
			}
			end = i
		}
		text = text[end+1:]
	}
	// Leave room for gob's envelope below the protocol's 64 MiB frame limit.
	if len(text) > 63<<20 {
		return proto.Error{Message: "scrollback is larger than 63 MiB; use --lines to request fewer lines"}
	}
	return proto.ScrollbackResult{Pane: m.Pane, Text: text}
}

func (d *Daemon) scrollbackText(id string) (string, error) {
	d.mu.Lock()
	p := d.panes[id]
	d.mu.Unlock()
	if p == nil {
		return "", fmt.Errorf("no pane %s", id)
	}
	s, ok := p.(interface{ Scrollback() string })
	if !ok {
		return "", errors.New("pane has no scrollback export")
	}
	return s.Scrollback(), nil
}

func editorCommand(visual, editor string) ([]string, error) {
	command := strings.TrimSpace(visual)
	if command == "" {
		command = strings.TrimSpace(editor)
	}
	if command == "" {
		command = "vi"
	}
	argv, err := config.SplitShell(command)
	if err != nil {
		return nil, fmt.Errorf("editor command: %w", err)
	}
	if len(argv) == 0 || argv[0] == "" {
		return nil, errors.New("editor command is empty")
	}
	return argv, nil
}

func (d *Daemon) editScrollback(m proto.EditScrollback) error {
	argv, err := editorCommand(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if err != nil {
		return err
	}
	text, err := d.scrollbackText(m.Pane)
	if err != nil {
		return err
	}
	d.mu.Lock()
	var open proto.OpenPane
	for _, w := range d.st.Workspaces {
		for _, t := range w.Tabs {
			for _, id := range layout.Panes(t.Layout) {
				if id == m.Pane {
					open = proto.OpenPane{WorkspaceID: w.ID, TabID: t.ID, Target: id, Dir: layout.Horizontal}
				}
			}
		}
	}
	d.mu.Unlock()
	if open.Target == "" {
		return fmt.Errorf("no pane %s", m.Pane)
	}
	// Adapted from tuios (MIT): internal/app/os_selection.go
	f, err := os.CreateTemp("", "pitwall-scrollback-*.txt")
	if err != nil {
		return err
	}
	path := f.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	_, err = f.WriteString(text)
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	open.Cmd = append(argv, path)
	err = d.openPaneFile(open, path)
	return err
}

// cleanScrollback removes path when the editor's process exits. Callers hold d.mu.
func (d *Daemon) cleanScrollback(id string, p Pane, path string) {
	if d.scrollbackFiles == nil {
		d.scrollbackFiles = map[string]string{}
	}
	d.scrollbackFiles[id] = path
	go func() {
		<-p.Done()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			log.Printf("scrollback cleanup: %v", err)
		}
		d.mu.Lock()
		if d.scrollbackFiles[id] == path {
			delete(d.scrollbackFiles, id)
		}
		d.mu.Unlock()
	}()
}
