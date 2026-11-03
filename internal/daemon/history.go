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

const historyEvery = 30 * time.Second

type cachedHistory struct {
	history *model.PaneHistory
	limit   int
}

type historySource struct {
	pane    Pane
	history *model.PaneHistory
	limit   int
}

// loadHistory validates saved rows before any panes or watchers start.
func (d *Daemon) loadHistory() {
	d.history = map[string]cachedHistory{}
	for i := range d.st.Panes {
		p := &d.st.Panes[i]
		h, limit := p.History, d.historyLines(*p)
		p.History = nil
		if h == nil || limit <= 0 {
			continue
		}
		text, err := store.UnpackHistory(h)
		if err == nil {
			p.History, err = store.PackHistory(h.Cols, recentHistory(text, limit), h.SavedAt)
		}
		if err != nil {
			log.Printf("pane %s: load history: %q", p.ID, err)
			continue
		}
		d.history[p.ID] = cachedHistory{p.History, limit}
	}
}

// historySources copies handles and policy under d.mu; capture uses neither map.
func (d *Daemon) historySources(s model.State) map[string]historySource {
	sources := make(map[string]historySource, len(s.Panes))
	for _, p := range s.Panes {
		sources[p.ID] = historySource{d.panes[p.ID], d.history[p.ID].history, d.historyLines(p)}
	}
	return sources
}

// captureHistory runs with saveMu held and d.mu released.
func captureHistory(s *model.State, sources map[string]historySource) {
	now := time.Now()
	for i := range s.Panes {
		p := &s.Panes[i]
		source := sources[p.ID]
		p.History = nil
		if source.limit <= 0 {
			continue
		}
		var cols int
		var text string
		at := now
		var err error
		if h, ok := source.pane.(interface{ HistoryText(int) (int, string) }); ok {
			cols, text = h.HistoryText(source.limit)
		} else if h := source.history; h != nil {
			cols, at = h.Cols, h.SavedAt
			text, err = store.UnpackHistory(h)
			text = recentHistory(text, source.limit)
		}
		if err == nil {
			p.History, err = store.PackHistory(cols, text, at)
		}
		if err != nil {
			log.Printf("pane %s: save history: %q", p.ID, err)
		}
	}
}

// cacheHistory rechecks policy and pane identity after capture. Callers hold d.mu.
func (d *Daemon) cacheHistory(s *model.State, sources map[string]historySource) {
	if d.history == nil {
		d.history = map[string]cachedHistory{}
	}
	for i := range s.Panes {
		p := &s.Panes[i]
		source := sources[p.ID]
		j := slices.IndexFunc(d.st.Panes, func(current model.Pane) bool { return current.ID == p.ID })
		if j < 0 || source.limit <= 0 || d.historyLines(d.st.Panes[j]) != source.limit || d.panes[p.ID] != source.pane {
			p.History = nil
			delete(d.history, p.ID)
			continue
		}
		d.history[p.ID] = cachedHistory{p.History, source.limit}
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
	// Adapted from tuios (MIT): internal/session/scrollback_persist.go
	banner := "\x1b[2mRestored from " + h.SavedAt.Local().Format("15:04, Jan 2") + "\x1b[0m\r\n"
	return d.historyVT(h.Cols, text, banner, newVT)
}

// carryVT keeps a resumed agent's output when it returns to a shell.
func (d *Daemon) carryVT(p model.Pane, live Pane) vt.NewFunc {
	if h, ok := live.(interface{ HistoryText(int) (int, string) }); ok && d.historyLines(p) > 0 {
		cols, text := h.HistoryText(d.historyLines(p))
		if cols > 0 && text != "" {
			return d.historyVT(cols, text, "", d.o.NewVT)
		}
	}
	return d.o.NewVT
}

func (d *Daemon) historyVT(width int, text, banner string, newVT vt.NewFunc) vt.NewFunc {
	return func(cols, rows int, reply io.Writer) vt.Emulator {
		e := newVT(width, 1, reply)
		if s, ok := e.(interface{ SetScrollback(int) }); ok && d.o.Scrollback != nil {
			s.SetScrollback(d.o.Scrollback())
		}
		e.Write([]byte(text + banner))
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
				d.Save()
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
