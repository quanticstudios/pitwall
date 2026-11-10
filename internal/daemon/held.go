package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"sync"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// stopped is a held pane that NewWith did not relaunch: no process, a
// screen with one notice, and the exit code the state records.
type stopped struct {
	mu   sync.Mutex
	vt   vt.Emulator
	done chan struct{}
	code int

	history *model.PaneHistory // source output, without generated divider and exit notice
	// parked marks a parked agent's pane (see park.go), whose next key
	// resumes it. cols and text are its output before the notice, for
	// HistoryText while history is nil.
	parked bool
	cols   int
	text   string
}

// stoppedPane marks p exited, ExitUnknown when its command was still
// running, and returns its handle. Callers hold d.mu.
func (d *Daemon) stoppedPane(p *model.Pane) Pane {
	prog := "the command"
	if len(p.Cmd) > 0 {
		prog = filepath.Base(p.Cmd[0]) // why: arguments can carry prompts or secrets
	}
	if p.Exited && !p.ExitUnknown {
		log.Printf("pane %s: restored exited %d; held", p.ID, p.ExitCode)
	} else {
		log.Printf("pane %s: restored %q, exit unknown; held, not run again", p.ID, prog)
	}
	notice := fmt.Sprintf("[pitwall] %s exited with code %d before pitwall restarted.", prog, p.ExitCode)
	if !p.Exited || p.ExitUnknown {
		p.Exited, p.ExitCode, p.ExitUnknown = true, 0, true
		if w := d.workspace(p.WorkspaceID); w != nil && w.TaskRun != nil && w.TaskRun.Pane == p.ID {
			w.TaskRun = nil
		}
		notice = fmt.Sprintf("[pitwall] pitwall restarted while %s ran, so it was not run again. Its exit code is lost.", prog)
	}
	newVT := d.o.NewVT
	if newVT == nil {
		newVT = vt.New
	}
	newVT = d.restoredVT(*p, newVT)
	s := &stopped{vt: newVT(defaultCols, defaultRows, io.Discard), done: make(chan struct{}), code: p.ExitCode, history: p.History}
	p.History = nil
	close(s.done)
	s.vt.Write([]byte(notice + "\r\n"))
	return s
}

// Save is the screen's handoff snapshot.
func (s *stopped) Save() ([]byte, error) { s.mu.Lock(); defer s.mu.Unlock(); return vt.Save(s.vt) }

func (s *stopped) Write([]byte) (int, error) { return 0, errors.New("the pane's command has exited") }
func (s *stopped) Resize(cols, rows int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vt.Resize(cols, rows)
	return nil
}
func (s *stopped) Snapshot() vt.Grid { s.mu.Lock(); defer s.mu.Unlock(); return s.vt.Snapshot() }
func (s *stopped) SnapshotAt(off int) vt.Grid {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vt.SnapshotAt(off)
}
func (s *stopped) Search(query string, limit int) ([]vt.Match, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vt.Search(query, limit)
}

func (s *stopped) SearchContext(ctx context.Context, query string, limit int) ([]vt.SearchLine, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if f, ok := s.vt.(interface {
		SearchContext(context.Context, string, int) ([]vt.SearchLine, bool)
	}); ok {
		return f.SearchContext(ctx, query, limit)
	}
	return nil, false
}
func (s *stopped) Text(sel vt.Selection) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.vt.Text(sel)
}
func (s *stopped) ScrollbackLen() int     { s.mu.Lock(); defer s.mu.Unlock(); return s.vt.ScrollbackLen() }
func (s *stopped) Modes() vt.Modes        { s.mu.Lock(); defer s.mu.Unlock(); return s.vt.Modes() }
func (s *stopped) Dirty() <-chan struct{} { return nil }
func (s *stopped) Done() <-chan struct{}  { return s.done }
func (s *stopped) ExitCode() int          { return s.code }
func (s *stopped) Cwd() string            { return "" }
func (s *stopped) Close() error           { return nil }

// Scrollback returns the pane's history and screen as plain text.
func (p *stopped) Scrollback() string {
	p.mu.Lock()
	build := vt.CaptureScrollback(p.vt)
	p.mu.Unlock()
	return build()
}

// Commands returns the emulator's OSC 133 command blocks.
func (p *stopped) Commands() ([]vt.CommandBlock, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.vt.(interface {
		Commands() ([]vt.CommandBlock, bool)
	}); ok {
		return e.Commands()
	}
	return nil, false
}

// HasPromptMarks reports whether shell integration marks remain in history.
func (p *stopped) HasPromptMarks() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.vt.(interface{ HasPromptMarks() bool }); ok {
		return e.HasPromptMarks()
	}
	return false
}

func (s *stopped) HistoryText(limit int) (int, string) {
	if s.history == nil {
		if s.text == "" {
			return 0, ""
		}
		return s.cols, recentHistory(s.text, limit)
	}
	text, err := store.UnpackHistory(s.history)
	if err != nil {
		log.Printf("held pane history: %q", err)
		return 0, ""
	}
	return s.history.Cols, recentHistory(text, limit)
}
