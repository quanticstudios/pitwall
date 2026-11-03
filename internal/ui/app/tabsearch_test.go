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
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// hitIDs names rows: a tab's id, a closed tab's id after "closed:", or
// "all" for the Search all sessions row.
func hitIDs(hs []tabHit) []string {
	var out []string
	for _, h := range hs {
		switch {
		case h.all:
			out = append(out, "all")
		case h.ws != nil:
			out = append(out, h.ws.ID)
		default:
			out = append(out, "closed:"+h.closed.ID)
		}
	}
	return out
}

// lastSent is the last message of type T the window sent.
func lastSent[T any](b *FakeBackend) (T, bool) {
	s := b.Sent()
	for i := len(s) - 1; i >= 0; i-- {
		if m, ok := s[i].(T); ok {
			return m, true
		}
	}
	var zero T
	return zero, false
}

// TestTabRows matches every field the spec names, puts tabs before
// closed tabs, the recent first with no query, and tolerates a typo.
func TestTabRows(t *testing.T) {
	now := time.Now()
	st := &model.State{
		Sessions: []model.Session{{ID: "s1", Name: "work"}, {ID: "s2", Name: "play"}},
		Projects: []model.Project{{ID: "g", SessionID: "s1", Name: "billing"}},
		Workspaces: []model.Workspace{
			{ID: "old", SessionID: "s1", Label: "Upgrade the ORM", Branch: "orm", Path: "/src/api", RepoRoot: "/src/api", UpdatedAt: now.Add(-time.Hour)},
			{ID: "new", SessionID: "s2", ProjectID: "g", Label: "Fix invoice rounding", Branch: "fix-rounding", Path: "/src/ledger", RepoRoot: "/src/ledger", UpdatedAt: now.Add(-2 * time.Hour)},
			{ID: "chat", SessionID: "s2", Chat: "c", Label: "a chat", UpdatedAt: now.Add(-3 * time.Hour)},
		},
		Panes:      []model.Pane{{ID: "p", WorkspaceID: "new", Provider: model.ProviderCodex, Prompt: "make rounding exact"}},
		Activities: []model.Activity{{WorkspaceID: "new", PaneID: "p", Provider: model.ProviderCodex, State: model.StateWorking, UpdatedAt: now}},
		PRs:        map[string]model.PR{"old": {Number: 41}},
		Chats:      []model.Chat{{ID: "c", WorkspaceID: "chat", Title: "plan the migration"}},
		Closed: []model.ClosedTab{
			{ID: "c1", Tab: model.Workspace{Label: "Add OAuth login", Branch: "oauth", Path: "/src/web"}, Pane: model.Pane{Provider: model.ProviderClaude, Prompt: "login with GitHub"},
				Group: model.Project{Name: "web-app"}, PR: 7, ClosedAt: now.Add(-time.Minute)},
			{ID: "c2", Tab: model.Workspace{Label: "Old shell", Path: "/tmp/scratch"}, ClosedAt: now.Add(-48 * time.Hour)},
		},
	}
	for _, c := range []struct {
		query string
		all   bool
		want  []string
	}{
		// The newest activity counts: "new" has a working agent now.
		{"", true, []string{"new", "old", "chat", "closed:c1", "closed:c2"}},
		{"rounding", true, []string{"new", "all"}},
		{"billing", false, []string{"new"}},      // group
		{"ledger", false, []string{"new"}},       // folder
		{"codex", false, []string{"new"}},        // agent
		{"exact", false, []string{"new"}},        // first prompt
		{"#41", false, []string{"old"}},          // pull request
		{"migration", false, []string{"chat"}},   // chat title
		{"claude", false, []string{"closed:c1"}}, // a closed tab's agent
		{"github", false, []string{"closed:c1"}}, // and its prompt
		{"#7", false, []string{"closed:c1"}},
		{"scratch", false, []string{"closed:c2"}},
		{"oatuh", false, []string{"closed:c1"}}, // two letters swapped
		{"invocie", false, []string{"new"}},
		{"upgrd orm", false, []string{"old"}}, // letters left out, as the palette's matcher allows
		{"nothing here", true, []string{"all"}},
	} {
		if got := hitIDs(tabRows(st, "", c.query, c.all)); !slices.Equal(got, c.want) {
			t.Errorf("%q: %v, want %v", c.query, got, c.want)
		}
	}
	// A whole word of the title outranks a scattered match, and a tab still
	// comes before any closed tab.
	st.Closed = append(st.Closed, model.ClosedTab{ID: "c3", Tab: model.Workspace{Label: "orm"}, ClosedAt: now})
	if got := hitIDs(tabRows(st, "", "orm", false)); !slices.Equal(got, []string{"old", "closed:c3"}) {
		t.Errorf("orm: %v", got)
	}
	// The current session's tabs come first: "chat" of s2 outranks "old" of
	// s1 from s2, though "old" is newer.
	if got := hitIDs(tabRows(st, "s2", "", false)); !slices.Equal(got[:3], []string{"new", "chat", "old"}) {
		t.Errorf("here s2: %v", got)
	}
}

func TestOneEdit(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"login", "login", true}, {"logn", "login", true}, {"loggin", "login", true}, {"lagin", "login", true},
		{"olgin", "login", true}, {"logni", "login", true}, {"lgoni", "login", false}, {"lg", "login", false}, {"xyzzy", "login", false},
	} {
		if got := oneEdit(c.a, c.b); got != c.want {
			t.Errorf("oneEdit(%q, %q) = %v", c.a, c.b, got)
		}
	}
	if typo("lgn", "login") {
		t.Error("a three-letter word matched one edit away")
	}
}

// TestTabSearchPresets binds tab_search in every preset, leaves plain
// Ctrl+K to the terminal, and lists it in the command palette.
func TestTabSearchPresets(t *testing.T) {
	for preset, want := range map[string]string{"conventional": "Ctrl+Shift+K", "aide": "Ctrl+Shift+K", "mac": "Super+K"} {
		b := config.Preset(preset)
		if got := firstChord(b.Global["tab_search"]); got != want {
			t.Errorf("%s: tab_search = %q, want %q", preset, got, want)
		}
		if a := b.Action(press("K", key.ModCtrl)); a != "" {
			t.Errorf("%s: Ctrl+K runs %q", preset, a)
		}
	}
	if es := rank(paletteEntries(conventional), "search tabs"); len(es) == 0 || es[0].action.Name != "tab_search" {
		t.Fatalf("palette: %v", names(es))
	}
}

// TestTabSearchKeys opens tab search with its key, shows a tab of another
// session, reopens a closed tab and selects it once it shows, opens one
// whose folder is gone in its repository, forgets one, and hands a query to
// Search all sessions.
func TestTabSearchKeys(t *testing.T) {
	b := NewFakeBackend()
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
	typeText := func(s string) {
		for _, r := range s {
			n := key.Name(string(r))
			if r == ' ' {
				n = key.NameSpace
			} else if r >= 'a' && r <= 'z' {
				n = key.Name(string(r - 'a' + 'A'))
			}
			pressKey(press(n, 0))
		}
	}
	open := func() {
		pressKey(press("K", key.ModCtrl|key.ModShift))
		if !u.pal.open || !u.pal.tabs {
			t.Fatalf("Ctrl+Shift+K: open %v, tabs %v", u.pal.open, u.pal.tabs)
		}
	}
	frame()
	frame()
	if u.nav.session != "s1" {
		t.Fatalf("starts on %q", u.nav.session)
	}

	open()
	typeText("rounding")
	pressKey(press(key.NameReturn, 0))
	if u.pal.open || u.nav.session != "s2" || u.nav.workspace != "w10" {
		t.Fatalf("Enter on a tab of calm-heron: open %v, session %q, tab %q", u.pal.open, u.nav.session, u.nav.workspace)
	}

	open()
	typeText("oauth")
	st := b.State()
	if rows := u.tabSearchRows(&st); len(rows) < 1 || rows[0].closed == nil || rows[0].closed.ID != "c1" {
		t.Fatalf("oauth: %v", hitIDs(rows))
	}
	pressKey(press(key.NameReturn, 0))
	if r, ok := lastSent[proto.ReopenTab](b); !ok || r.ID != "c1" || r.AtRoot {
		t.Fatalf("Enter on a closed tab sent %#v", r)
	}
	frame()
	if u.nav.session != "s1" || u.nav.workspace != "c1" {
		t.Fatalf("reopened tab not shown: session %q, tab %q", u.nav.session, u.nav.workspace)
	}

	open()
	typeText("flaky")
	pressKey(press(key.NameReturn, 0))
	if r, ok := lastSent[proto.ReopenTab](b); !ok || r.ID != "c2" || !r.AtRoot {
		t.Fatalf("Enter on a tab whose worktree is gone sent %#v", r)
	}

	open()
	typeText("acme")
	st = b.State()
	rows := u.tabSearchRows(&st)
	i := slices.IndexFunc(rows, func(h tabHit) bool { return h.closed != nil && h.closed.ID == "c3" })
	if i < 0 {
		t.Fatalf("acme: %v", hitIDs(rows))
	}
	for range i {
		pressKey(press(key.NameDownArrow, 0))
	}
	pressKey(press(key.NameDeleteForward, 0))
	if f, ok := lastSent[proto.ForgetTab](b); !ok || f.ID != "c3" || !u.pal.open {
		t.Fatalf("Delete sent %#v, open %v", f, u.pal.open)
	}
	if st := b.State(); len(st.Closed) != 0 {
		t.Fatalf("closed left: %+v", st.Closed)
	}

	st = b.State()
	rows = u.tabSearchRows(&st)
	for range rows {
		pressKey(press(key.NameDownArrow, 0))
	}
	if !rows[len(rows)-1].all {
		t.Fatalf("no last row for Search all sessions: %v", hitIDs(rows))
	}
	pressKey(press(key.NameReturn, 0))
	if !u.pal.open || u.pal.tabs || !u.pal.search || u.pal.query != "acme" {
		t.Fatalf("Search all sessions: open %v, tabs %v, search %v, query %q", u.pal.open, u.pal.tabs, u.pal.search, u.pal.query)
	}
	pressKey(press(key.NameEscape, 0))

	open()
	pressKey(press(key.NameEscape, 0))
	if u.pal.open {
		t.Fatal("Escape left tab search open")
	}
}
