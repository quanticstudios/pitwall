package app

import (
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
	"github.com/quanticstudios/pitwall/internal/vt"
)

type searchBackend struct {
	*FakeBackend
	results []proto.SearchAllResult
}

func (b *searchBackend) SearchResults() []proto.SearchAllResult {
	r := b.results
	b.results = nil
	return r
}

func (b *searchBackend) Found(pane string) (proto.SearchResult, uint64) {
	return proto.SearchResult{}, 500
}
func (b *searchBackend) Scroll(pane string) (int, int, uint64) { return 0, 1000, 500 }

func TestSearchAllPaletteNavigation(t *testing.T) {
	b := &searchBackend{FakeBackend: NewFakeBackend()}
	u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: conventional}}
	var router input.Router
	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := gl.Context{Ops: &ops, Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(image.Pt(1280, 800)), Now: time.Now()}
		u.layout(gtx)
		router.Frame(&ops)
	}
	pressKey := func(e key.Event) {
		router.Queue(e)
		frame()
		router.Queue(key.Event{Name: e.Name, State: key.Release, Modifiers: e.Modifiers})
		frame()
	}
	frame()
	frame()
	// Run the real palette entry, whose action is unbound in every preset.
	for _, preset := range config.Presets {
		if cs := config.Preset(preset).Global["search_all"]; len(cs) != 0 {
			t.Fatalf("search_all bound by default in %s", preset)
		}
	}
	entries := rank(paletteEntries(conventional), "search all sessions")
	if len(entries) == 0 || entries[0].action.Name != "search_all" {
		t.Fatalf("entry missing: %v", names(entries))
	}
	st := b.State()
	u.runAction(&st, entries[0].action)
	u.openRequested(&st, time.Now())
	frame()
	pressKey(press("E", key.ModShift))
	if !u.pal.search || u.all.sent != "E" {
		t.Fatalf("search query %q", u.all.sent)
	}
	old := u.all.id
	pressKey(press("R", 0))
	if u.all.id <= old || u.all.sent != "Er" {
		t.Fatalf("new keystroke did not replace query: %+v", u.all)
	}
	b.results = []proto.SearchAllResult{
		{ID: old, Matches: []proto.SessionMatch{{Workspace: "w1", Pane: "a"}}},
		{ID: u.all.id, Matches: []proto.SessionMatch{
			{Workspace: "w7", Pane: "k", Match: vt.Match{Line: 20}, Snippet: "Error in detached tab"},
			{Workspace: "w1", Pane: "a", Match: vt.Match{Line: 100}, Snippet: "Error in attached tab"},
		}, Done: true},
	}
	frame()
	if len(u.all.matches) != 2 || u.all.matches[0].Workspace != "w1" {
		t.Fatalf("stale reply or grouping: %+v", u.all.matches)
	}
	pressKey(press(key.NameDownArrow, 0))
	pressKey(press(key.NameReturn, 0))
	if u.pal.open || u.nav.workspace != "w7" || u.nav.focused() != "k" {
		t.Fatalf("did not focus detached result: %s/%s open=%v", u.nav.workspace, u.nav.focused(), u.pal.open)
	}
	sent := b.Sent()
	if !slices.ContainsFunc(sent, func(m any) bool { d, ok := m.(proto.DetachSession); return ok && d.WorkspaceID == "w7" && !d.Detached }) {
		t.Error("result did not attach detached tab")
	}
	if !slices.ContainsFunc(sent, func(m any) bool { s, ok := m.(proto.Scroll); return ok && s.Pane == "k" && s.Lines > 0 }) {
		t.Error("result did not scroll to its line")
	}
	for _, m := range sent {
		if in, ok := m.(proto.Input); ok && len(in.Data) > 0 {
			t.Errorf("search key leaked to PTY: %q", in.Data)
		}
	}
}

func TestSearchAllTranscriptPreviewAndEscape(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	st := u.b.State()
	u.pal.openAt(time.Now())
	u.pal.search = true
	u.all.matches = []proto.SessionMatch{{Workspace: "w9", Pane: "o", Transcript: true, Snippet: "matching transcript text"}}
	// Use a known pane in another session rather than rely on the fake's IDs.
	for _, p := range st.Panes {
		for _, w := range st.Workspaces {
			if w.ID == p.WorkspaceID && w.SessionID != u.nav.session && !w.Detached {
				u.all.matches[0].Workspace, u.all.matches[0].Pane = w.ID, p.ID
				break
			}
		}
	}
	u.searchAllKey(&st, press(key.NameReturn, 0))
	if u.all.preview == nil || !u.pal.open || u.nav.focused() != u.all.matches[0].Pane {
		t.Fatalf("transcript not shown at destination: %+v", u.all)
	}
	keys(press(key.NameEscape, 0))
	if u.pal.open || u.all.preview != nil {
		t.Fatal("Escape did not dismiss transcript excerpt")
	}
}
