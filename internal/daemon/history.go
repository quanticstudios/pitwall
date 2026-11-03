package daemon

import (
	"context"
	"io"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/store"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// historyLines applies the limit to both capture and restore. Callers hold d.mu.
func (d *Daemon) historyLines(p model.Pane) int {
	if d.o.PersistScrollback == nil {
		return 0
	}
	for _, other := range d.st.Panes {
		if other.WorkspaceID == p.WorkspaceID && slices.Contains(other.Env, "PITWALL_GUARD=1") {
			return 0
		}
	}
	if slices.Contains(p.Env, "PITWALL_NO_HISTORY=1") {
		return 0
	}
	return d.o.PersistScrollback()
}

func (d *Daemon) captureHistory(s *model.State) {
	now := time.Now()
	for i := range s.Panes {
		p := &s.Panes[i]
		limit := d.historyLines(*p)
		if limit <= 0 {
			continue
		}
		if h, ok := d.panes[p.ID].(interface{ HistoryText(int) (int, string) }); ok {
			cols, text := h.HistoryText(limit)
			var err error
			p.History, err = store.PackHistory(cols, text, now)
			if err != nil {
				log.Printf("pane %s: save history: %q", p.ID, err)
			}
		} else if j := slices.IndexFunc(d.st.Panes, func(old model.Pane) bool { return old.ID == p.ID }); j >= 0 {
			// A queued agent still owns the history from the saved state.
			old := d.st.Panes[j].History
			if old != nil {
				text, err := store.UnpackHistory(old)
				if err == nil {
					p.History, err = store.PackHistory(old.Cols, recentHistory(text, limit), old.SavedAt)
				}
				if err != nil {
					log.Printf("pane %s: keep queued history: %q", p.ID, err)
				}
			}
		}
	}
}

// restoredVT writes history before the new pane starts reading its PTY.
func (d *Daemon) restoredVT(p model.Pane, newVT vt.NewFunc) vt.NewFunc {
	h := p.History
	limit := d.historyLines(p)
	if h == nil || limit <= 0 {
		return newVT
	}
	text, err := store.UnpackHistory(h)
	if err != nil {
		log.Printf("pane %s: restore history: %q", p.ID, err)
		return newVT
	}
	text = recentHistory(text, limit)
	return func(cols, rows int, reply io.Writer) vt.Emulator {
		e := newVT(h.Cols, 1, reply)
		if s, ok := e.(interface{ SetScrollback(int) }); ok && d.o.Scrollback != nil {
			s.SetScrollback(d.o.Scrollback())
		}
		e.Write([]byte(text))
		// Adapted from tuios (MIT): internal/session/scrollback_persist.go
		e.Write([]byte("\x1b[2mRestored from " + h.SavedAt.Local().Format("15:04, Jan 2") + "\x1b[0m\r\n"))
		e.Resize(cols, rows)
		return e
	}
}

// historyLoop refreshes rolling snapshots even when only terminal output changes.
func (d *Daemon) historyLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if d.o.PersistScrollback != nil && d.o.PersistScrollback() > 0 {
				d.save()
			}
		}
	}
}

func recentHistory(text string, limit int) string {
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines[max(0, len(lines)-limit):], "")
}
