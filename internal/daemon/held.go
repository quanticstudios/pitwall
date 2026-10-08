package daemon

import (
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"sync"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// stopped is a held pane that NewWith did not relaunch: no process, a
// screen with one notice, and the exit code the state records.
type stopped struct {
	mu   sync.Mutex
	vt   vt.Emulator
	done chan struct{}
	code int
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
	notice := fmt.Sprintf("[pitwall] %s exited with code %d before pitwall restarted. Its output was not saved.", prog, p.ExitCode)
	if !p.Exited || p.ExitUnknown {
		p.Exited, p.ExitCode, p.ExitUnknown = true, 0, true
		notice = fmt.Sprintf("[pitwall] pitwall restarted while %s ran, so it was not run again. Its exit code and output are lost.", prog)
	}
	newVT := d.o.NewVT
	if newVT == nil {
		newVT = vt.New
	}
	s := &stopped{vt: newVT(defaultCols, defaultRows, io.Discard), done: make(chan struct{}), code: p.ExitCode}
	close(s.done)
	s.vt.Write([]byte(notice + "\r\n"))
	return s
}

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
func (s *stopped) ScrollbackLen() int     { s.mu.Lock(); defer s.mu.Unlock(); return s.vt.ScrollbackLen() }
func (s *stopped) Modes() vt.Modes        { s.mu.Lock(); defer s.mu.Unlock(); return s.vt.Modes() }
func (s *stopped) Dirty() <-chan struct{} { return nil }
func (s *stopped) Done() <-chan struct{}  { return s.done }
func (s *stopped) ExitCode() int          { return s.code }
func (s *stopped) Cwd() string            { return "" }
func (s *stopped) Close() error           { return nil }
