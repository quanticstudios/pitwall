package panel

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// dockInbox is an inbox of n items across tabs, here's first, which the
// dock leaves to here's own chat, and three agents at work.
func dockInbox(n int) model.Inbox {
	all := []model.InboxItem{
		{Kind: model.InboxApproval, WorkspaceID: "here", PaneID: "p0", At: 1, Tab: "orchestrator", Text: "wants to run ls"},
		{Kind: model.InboxApproval, WorkspaceID: "w1", PaneID: "p1", At: 2, Tab: "billing", Text: "wants to run go test ./...", Command: "go test ./..."},
		{Kind: model.InboxQuestion, WorkspaceID: "w2", PaneID: "p2", At: 3, Tab: "since-you-left", Text: "Where should the card live?", Answers: []string{"Marker (Recommended)", "Card"}},
		{Kind: model.InboxPlan, WorkspaceID: "w3", PaneID: "p3", At: 4, Tab: "phone-new-agent", Text: "has a plan to approve", Answers: []string{"Yes, auto-accept edits", "Yes, approve edits", "No, keep planning"}},
		{Kind: model.InboxForm, WorkspaceID: "w4", PaneID: "p4", Tab: "release", Text: "Pick the version", Form: "f1"},
		{Kind: model.InboxCI, WorkspaceID: "w5", Tab: "crisp-borders", Text: "CI failed: test (ubuntu), vet"},
		{Kind: model.InboxBlocked, WorkspaceID: "w6", PaneID: "p6", At: 5, Tab: "chat-spinner", Text: "API Error: 529 overloaded"},
	}
	return model.Inbox{Items: all[:min(n+1, len(all))], Moving: 3}
}

// dockFrame draws a chat as the app does, transcript, dock, composer, w
// by h px at scale 1.
func dockFrame(t *testing.T, th *theme.Theme, s *session.Snapshot, chatLayout string, in model.Inbox, open bool, w, h int) (*image.RGBA, int) {
	t.Helper()
	win, err := headless.NewWindow(w, h)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	tr := Transcript{Dir: "/repo", ChatLayout: chatLayout, ToolCalls: ToolsAll, Width: 760}
	var cmp chatin.Composer
	dock := Dock{Open: open}
	var ops op.Ops
	now := time.Date(2026, 11, 21, 14, 4, 0, 0, time.UTC)
	dh := 0
	for range 3 {
		ops.Reset()
		now = now.Add(16 * time.Millisecond)
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(w, h)), Now: now}
		env := chatin.Env{Chat: "c", Snap: s, Width: 760, Gutter: LedgerGutter(s.Provider), Ledger: chatLayout == LayoutLedger, Key: tr.Key, Answer: tr.Answer, Branch: "inbox-dock"}
		layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return tr.Layout(gtx, th, s, false) }),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				full := gtx.Constraints.Max.X
				cw := max(0, min(full-2*gtx.Dp(theme.SpaceXL), gtx.Dp(env.Width)))
				x := (full - cw) / 2
				if env.Ledger {
					x, cw, _ = chatin.LedgerColumn(gtx, full, gtx.Dp(env.Gutter))
				}
				d := dock.Layout(gtx, th, in, "here", x, cw)
				dh = d.Size.Y
				return d
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions { return cmp.Layout(gtx, th, env) }),
		)
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	return img, dh
}

// TestDockHeights checks that the dock draws nothing while nothing waits
// and nothing moves, one line folded or with nothing to answer, and rows
// up to four, then four and a half as the list scrolls.
func TestDockHeights(t *testing.T) {
	th := theme.Dark()
	s := ledgerSnapshot()
	s.Running, s.SessionState, s.PendingApprovals = false, session.StateReady, nil
	height := func(in model.Inbox, open bool) int {
		_, h := dockFrame(t, th, s, LayoutColumn, in, open, 1000, 700)
		return h
	}
	none := height(model.Inbox{Items: dockInbox(0).Items}, true) // only here's own item
	line := height(dockInbox(0), true)
	folded := height(dockInbox(6), false)
	two := height(dockInbox(2), true)
	six := height(dockInbox(6), true)
	if none != 0 || line == 0 || folded != line || two <= line || six <= two || six != height(dockInbox(5), true) || six <= height(dockInbox(4), true) {
		t.Fatalf("heights: none %d, line %d, folded %d, two %d, six %d", none, line, folded, two, six)
	}
}

// TestDockParts checks the header's counts and each kind's buttons.
func TestDockParts(t *testing.T) {
	items := DockItems(dockInbox(6), "here")
	if len(items) != 6 || items[0].WorkspaceID == "here" {
		t.Fatalf("items %+v", items)
	}
	if got := model.InboxHead(items, 3); !slices.Equal(got, []string{"4 for you", "2 blocked", "3 moving"}) {
		t.Fatalf("head %v", got)
	}
	want := [][]string{{DockDeny, DockAllow}, {"Marker (Recommended)", "Card"}, {"Yes, auto-accept edits", "Yes, approve edits", "No, keep planning"}, {""}, {""}, {""}}
	for i, it := range items {
		if got := dockAnswers(it); !slices.Equal(got, want[i]) {
			t.Errorf("%s: answers %q, want %q", it.Kind, got, want[i])
		}
	}
	long := model.InboxItem{Kind: model.InboxQuestion, Answers: []string{"a", "b", "c", "d"}}
	if got := dockAnswers(long); !slices.Equal(got, []string{""}) {
		t.Errorf("four answers: %q, want Open", got)
	}
}

// TestDockShots renders the dock under a chat at 1440 px, dark: nothing
// waiting, two items, six, folded, and Ledger with six, to
// PITWALL_DOCK_SHOTS for the PR.
func TestDockShots(t *testing.T) {
	dir := os.Getenv("PITWALL_DOCK_SHOTS")
	if dir == "" {
		t.Skip("set PITWALL_DOCK_SHOTS to render the dock's PNGs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	th := theme.Dark()
	s := ledgerSnapshot()
	s.Running, s.SessionState, s.PendingApprovals = false, session.StateReady, nil
	for _, c := range []struct {
		name, layout string
		in           model.Inbox
		open         bool
	}{
		{"dock-none.png", LayoutColumn, model.Inbox{}, true},
		{"dock-empty.png", LayoutColumn, dockInbox(0), true},
		{"dock-2.png", LayoutColumn, dockInbox(2), true},
		{"dock-6.png", LayoutColumn, dockInbox(6), true},
		{"dock-6-folded.png", LayoutColumn, dockInbox(6), false},
		{"dock-6-ledger.png", LayoutLedger, dockInbox(6), true},
	} {
		img, _ := dockFrame(t, th, s, c.layout, c.in, c.open, 1440, 900)
		f, err := os.Create(filepath.Join(dir, c.name))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
