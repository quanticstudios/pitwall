package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Keep broad queries bounded while still searching every pane. The palette
// tells the user to narrow the query when a source has more matches.
const searchPerPane = 100

type searchPane struct {
	meta model.Pane
	pane Pane
}

// searchAll snapshots handles, not history, under the daemon lock. Detached
// tabs remain in st.Panes, so they are searched exactly like attached tabs.
// Each scrollback completes before transcript IO starts. No output, query,
// filename or JSON error enters the daemon log.
func (d *Daemon) searchAll(ctx context.Context, q proto.SearchAll, emit func(proto.SearchAllResult)) {
	if q.Query == "" || len(q.Query) > 1024 {
		emit(proto.SearchAllResult{ID: q.ID, Done: true})
		return
	}
	d.mu.Lock()
	panes := make([]searchPane, 0, len(d.st.Panes))
	for _, p := range d.st.Panes {
		panes = append(panes, searchPane{p, d.panes[p.ID]})
	}
	d.mu.Unlock()
	more := false
	total := 0
	send := func(matches []proto.SessionMatch) {
		if len(matches) > 0 && ctx.Err() == nil {
			emit(proto.SearchAllResult{ID: q.ID, Matches: matches})
		}
	}
	for _, p := range panes {
		if ctx.Err() != nil {
			return
		}
		matches, extra := searchHistory(ctx, p.pane, q.Query, min(searchPerPane, maxMatches-total))
		more = more || extra
		batch := make([]proto.SessionMatch, 0, len(matches))
		now := time.Now()
		for _, m := range matches {
			if ctx.Err() != nil {
				return
			}
			batch = append(batch, proto.SessionMatch{Workspace: p.meta.WorkspaceID, Pane: p.meta.ID, Match: m.Match, Snippet: strings.TrimSpace(m.Text), Time: now})
		}
		total += len(batch)
		send(batch)
	}
	for _, p := range panes {
		if ctx.Err() != nil {
			return
		}
		var batch []proto.SessionMatch
		n := 0
		flow.Search(ctx, p.meta.Provider, p.meta.Transcript, q.Query, func(m flow.TextMatch) bool {
			if n >= searchPerPane || total >= maxMatches {
				more = true
				return false
			}
			batch = append(batch, proto.SessionMatch{Workspace: p.meta.WorkspaceID, Pane: p.meta.ID, Match: vt.Match{Line: m.Record}, Snippet: m.Snippet, Time: m.Time, Transcript: true})
			n++
			total++
			if len(batch) == 32 {
				send(batch)
				batch = nil
			}
			return ctx.Err() == nil
		})
		send(batch)
	}
	if ctx.Err() == nil {
		emit(proto.SearchAllResult{ID: q.ID, Done: true, More: more})
	}
}

func searchHistory(ctx context.Context, p Pane, query string, limit int) ([]vt.SearchLine, bool) {
	if s, ok := p.(interface {
		SearchContext(context.Context, string, int) ([]vt.SearchLine, bool)
	}); ok {
		return s.SearchContext(ctx, query, limit)
	}
	s, ok := p.(interface {
		Search(string, int) ([]vt.Match, bool)
		Text(vt.Selection) string
	})
	if !ok || ctx.Err() != nil {
		return nil, false
	}
	matches, more := s.Search(query, limit)
	var out []vt.SearchLine
	for _, m := range matches {
		if ctx.Err() != nil {
			return nil, false
		}
		text := s.Text(vt.Selection{A: vt.Pos{Line: m.Line, Col: max(0, m.Col-20)}, B: vt.Pos{Line: m.Line, Col: m.Col + max(m.Cols, 100)}})
		out = append(out, vt.SearchLine{Match: m, Text: text})
	}
	return out, more
}
