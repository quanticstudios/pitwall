package sidebar

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	pwlayout "github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// queueState is three tabs: an agent pane chat with two queued, a chat
// tab whose one queued message is held, and one with none.
func queueState(now time.Time) model.State {
	tab := func(id, pane string) model.Workspace {
		return model.Workspace{ID: id, Name: id, UpdatedAt: now.Add(-3 * time.Minute),
			Tabs: []model.Tab{{ID: id + "t", Layout: &pwlayout.Node{Pane: pane}}}, ActiveTab: id + "t"}
	}
	st := model.State{
		Workspaces: []model.Workspace{tab("fix-store", "p1"), {ID: "docs", Name: "docs", Chat: "c1", UpdatedAt: now.Add(-time.Minute)}, tab("idle", "p3")},
		Panes:      []model.Pane{{ID: "p1", WorkspaceID: "fix-store", Provider: model.ProviderClaude}, {ID: "p3", WorkspaceID: "idle", Provider: model.ProviderCodex}},
		Chats:      []model.Chat{{ID: "c1", WorkspaceID: "docs", Provider: model.ProviderCodex, Queued: 1, QueueHeld: "The last turn failed"}},
		Activities: []model.Activity{
			{PaneID: "p1", WorkspaceID: "fix-store", Provider: model.ProviderClaude, State: model.StateWorking, UpdatedAt: now},
			{PaneID: "p3", WorkspaceID: "idle", Provider: model.ProviderCodex, State: model.StateCompleted, UpdatedAt: now.Add(-5 * time.Minute)},
		},
		ChatQueues: map[string]model.ChatQueue{"p1": {N: 2}, "c1": {N: 1, Held: "The last turn failed"}},
	}
	return st
}

func TestQueueLabel(t *testing.T) {
	st := queueState(time.Unix(1e9, 0))
	for i, want := range []string{"2 queued", "1 held", ""} {
		if got := QueueLabel(&st, st.Workspaces[i]); got != want {
			t.Errorf("%s: %q, want %q", st.Workspaces[i].ID, got, want)
		}
	}
}

// TestQueueShot renders the sidebar with queued tabs into
// $PITWALL_QUEUE_SHOTS; it skips without it.
func TestQueueShot(t *testing.T) {
	dir := os.Getenv("PITWALL_QUEUE_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_QUEUE_SHOTS is not set")
	}
	const scale = 2
	size := image.Pt(288*scale, 260*scale)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	defer win.Release()
	now := time.Unix(1e9, 0)
	st := queueState(now)
	var s Sidebar
	s.HideSessions = true
	var r input.Router
	var ops op.Ops
	for range 3 {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Exact(size), Now: now}
		paint.Fill(gtx.Ops, theme.Dark().Sidebar)
		s.Layout(gtx, theme.Dark(), &st, "", "")
		r.Frame(&ops)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "sidebar-queued.png"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
