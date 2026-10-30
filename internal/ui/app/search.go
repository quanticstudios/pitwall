package app

import (
	"cmp"
	"fmt"
	"slices"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// SessionSearcher is optionally implemented by a daemon backend. Batches are
// consumed once and carry request IDs, so a late reply cannot replace a query.
type SessionSearcher interface {
	SearchResults() []proto.SearchAllResult
}

type sessionSearch struct {
	id      uint64
	sent    string
	matches []proto.SessionMatch
	done    bool
	more    bool
	preview *proto.SessionMatch
	jump    *proto.SessionMatch // wait for the destination's first frame
}

func (u *ui) canSearchAll() bool { return u.link().Level >= proto.Since(proto.SearchAll{}) }

func (u *ui) stopSearchAll() {
	if u.all.sent != "" && u.canSearchAll() {
		u.all.id++
		u.send(proto.SearchAll{ID: u.all.id})
	}
	u.all.sent, u.all.matches, u.all.preview = "", nil, nil
}

func (u *ui) updateSearchAll() {
	s, p := &u.all, &u.pal
	if !p.search || !p.open || s.preview != nil || !u.canSearchAll() {
		return
	}
	if p.query != s.sent {
		s.id++
		s.sent, s.matches, s.done, s.more = p.query, nil, false, false
		u.send(proto.SearchAll{ID: s.id, Query: s.sent})
	}
	if b, ok := u.b.(SessionSearcher); ok {
		for _, r := range b.SearchResults() {
			if r.ID == s.id && s.sent != "" {
				// Keep the selection on its match as later batches join a tab.
				var selected *proto.SessionMatch
				if p.sel < len(s.matches) {
					m := s.matches[p.sel]
					selected = &m
				}
				s.matches = append(s.matches, r.Matches...)
				slices.SortStableFunc(s.matches, func(a, b proto.SessionMatch) int { return cmp.Compare(a.Workspace, b.Workspace) })
				if selected != nil {
					p.sel = slices.Index(s.matches, *selected)
				}
				s.done, s.more = r.Done, s.more || r.More
			}
		}
	}
}

func (u *ui) searchAllKey(st *model.State, e key.Event) {
	p := &u.pal
	if e.State != key.Press {
		return
	}
	if e.Name == key.NameEscape || u.nav.bind().Action(e) == "command_palette" {
		p.close()
		u.stopSearchAll()
	} else if e.Name == key.NameReturn || e.Name == key.NameEnter {
		if u.all.preview != nil {
			p.close()
			u.stopSearchAll()
		} else if p.sel < len(u.all.matches) {
			u.pickSearchAll(st, u.all.matches[p.sel])
		}
	} else if u.all.preview == nil {
		// Reuse the command palette's editing and movement keys. Empty rows
		// are enough for editing; row count bounds the selection.
		old := p.query
		p.key(make([]paletteEntry, len(u.all.matches)), e)
		if e.Modifiers == key.ModShift && len(e.Name) == 1 && len(p.query) > len(old) {
			p.query = old + string(e.Name)
		}
		u.updateSearchAll() // cancel immediately, including when erased to empty
	}
	if !p.open {
		u.nav.swallow = e.Name
	}
}

func (u *ui) pickSearchAll(st *model.State, m proto.SessionMatch) {
	w := findWorkspace(st, m.Workspace)
	if w == nil || findPane(st, m.Pane) == nil {
		return // the tab closed while results were arriving
	}
	if w.Detached {
		u.send(proto.DetachSession{WorkspaceID: w.ID, Detached: false})
		u.nav.attach[w.ID] = true
	}
	u.nav.selectWorkspace(st, w.ID, m.Pane)
	u.nav.setFocus(m.Pane)
	if m.Transcript {
		// A transcript record has no terminal coordinate. Keep its exact
		// excerpt visible in the destination tab rather than invent a line.
		u.all.preview = &m
		u.all.id++
		u.send(proto.SearchAll{ID: u.all.id})
		u.all.sent = ""
		return
	}
	u.all.jump = &m
	u.openFind()
	u.find.ed.SetText(u.all.sent)
	u.find.at, u.find.atOK, u.find.keep = m.Match, true, true
	u.pal.close()
	u.stopSearchAll()
}

// searchAllJump runs after the pane has a frame, including a previously
// detached pane whose first frame needed proto.View. Absolute line numbers
// keep navigation correct while more output arrives.
func (u *ui) searchAllJump(pane string) {
	m := u.all.jump
	if m == nil || m.Pane != pane {
		return
	}
	f, ok := u.b.(Finder)
	if !ok {
		return
	}
	g, _, have := u.b.Frame(pane)
	if !have || g.Rows == 0 {
		return
	}
	_, top := f.Found(pane)
	if s, ok := u.b.(Scroller); ok {
		off, most, _ := s.Scroll(pane)
		if d := reveal(m.Match.Line, top, off, most, g.Rows); d != 0 {
			u.send(proto.Scroll{Pane: pane, Lines: d})
		}
	}
	u.all.jump = nil
}

func (u *ui) searchStatus() string {
	s := &u.all
	switch {
	case !u.canSearchAll():
		return searchOld
	case s.preview != nil:
		return "Matching transcript excerpt"
	case u.pal.query == "":
		return "Search scrollback and agent transcripts, including detached tabs"
	case !s.done:
		return fmt.Sprintf("%d matches · searching…", len(s.matches))
	case s.more:
		return fmt.Sprintf("%d matches · narrow the query for more", len(s.matches))
	default:
		return fmt.Sprintf("%d matches", len(s.matches))
	}
}
