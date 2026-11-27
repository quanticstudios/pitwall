//go:build !android

package mobile

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
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// shotTabs are two machines' tabs with n inbox items among them.
func shotTabs(n int) (desktop, box []remote.Tab) {
	q := &agent.Question{ID: "q", Prompt: "Where should the card live?", Options: []agent.QuestionOption{{Label: "Marker (Recommended)"}, {Label: "Card"}}}
	at := time.Date(2026, 11, 27, 9, 29, 0, 0, time.UTC).UnixNano()
	tab := func(id, name, group, state string, items ...model.InboxItem) remote.Tab {
		return remote.Tab{ID: id, Group: group, Repo: "pitwall", Item: remote.Item{Pane: "p" + id, Tab: name, Session: "main", State: state, At: at, Answer: state == "pending-approval"}, Send: true, Inbox: items}
	}
	all := []func(){}
	desktop = []remote.Tab{tab("w0", "orchestrator", "pitwall", "working")}
	box = []remote.Tab{tab("w9", "question-answer", "pitwall", "working"), tab("w8", "phone-polish", "pitwall", "completed")}
	all = append(all,
		func() {
			desktop = append(desktop, tab("w1", "billing", "pitwall", "pending-approval",
				model.InboxItem{Kind: model.InboxApproval, WorkspaceID: "w1", PaneID: "pw1", At: 5, Tab: "billing", Text: "wants to run go test ./..."}))
		},
		func() {
			t := tab("w2", "since-you-left", "pitwall", "awaiting-input",
				model.InboxItem{Kind: model.InboxQuestion, WorkspaceID: "w2", PaneID: "pw2", At: 5, Tab: "since-you-left", Text: "Where should the card live?"})
			t.Question = q
			desktop = append(desktop, t)
		},
		func() {
			box = append(box, tab("w3", "crisp-borders", "pitwall", "completed",
				model.InboxItem{Kind: model.InboxCI, WorkspaceID: "w3", Tab: "crisp-borders", Text: "CI failed: test (ubuntu), vet"}))
		},
		func() {
			desktop = append(desktop, tab("w4", "release", "pitwall", "completed",
				model.InboxItem{Kind: model.InboxForm, WorkspaceID: "w4", Tab: "release", Text: "Pick the version"}))
		},
		func() {
			box = append(box, tab("w5", "phone-new-agent", "pitwall", "plan-ready",
				model.InboxItem{Kind: model.InboxPlan, WorkspaceID: "w5", PaneID: "pw5", At: 5, Tab: "phone-new-agent", Text: "has a plan to approve"}))
		},
		func() {
			box = append(box, tab("w6", "chat-spinner", "pitwall", "error",
				model.InboxItem{Kind: model.InboxBlocked, WorkspaceID: "w6", PaneID: "pw6", At: 5, Tab: "chat-spinner", Text: "API Error: 529 overloaded"}))
		},
	)
	for _, f := range all[:n] {
		f()
	}
	return desktop, box
}

// listShot draws the list screen of an app paired with desktop and box,
// 360 by 780 dp at scale 2, dark, and writes it to path.
func listShot(t *testing.T, n int, folded bool, path string) {
	t.Helper()
	ms := []phone.Machine{{ID: "m1", Name: "desktop"}, {ID: "m2", Name: "box"}}
	a := &App{th: theme.Dark(), fleet: phone.NewFleet(ms), links: map[string]*link{"m1": {m: ms[0], online: true}, "m2": {m: ms[1], online: true}}}
	a.sb.TreeOnly, a.sb.Fill, a.sb.Touch, a.sb.ExpandAll, a.sb.Answers = true, true, true, true, true
	a.sb.AllHosts, a.sb.SeparateWorkers, a.sb.Rev = true, true, 1
	a.sb.Restartable = a.restartable
	a.updates.settings.DockFolded = folded
	d, b := shotTabs(n)
	a.fleet.Set("m1", d)
	a.fleet.Set("m2", b)
	a.refresh()
	const scale = 2
	size := image.Pt(360*scale, 780*scale)
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Skip(err)
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	now := time.Date(2026, 11, 27, 9, 41, 0, 0, time.UTC)
	for range 3 {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: now, Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale}, Constraints: layout.Exact(size)}
		fillMax(gtx, a.th.Bg)
		a.listLayout(gtx)
		r.Frame(&ops)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestInboxShots renders the list's Needs you section at 360 dp, dark:
// nothing waiting, two items, six, and six folded, to
// PITWALL_INBOX_SHOTS for the PR.
func TestInboxShots(t *testing.T) {
	dir := os.Getenv("PITWALL_INBOX_SHOTS")
	if dir == "" {
		t.Skip("set PITWALL_INBOX_SHOTS to render the phone's PNGs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		n      int
		folded bool
	}{
		{"phone-empty.png", 0, false},
		{"phone-2.png", 2, false},
		{"phone-6.png", 6, false},
		{"phone-6-folded.png", 6, true},
	} {
		listShot(t, c.n, c.folded, filepath.Join(dir, c.name))
	}
}
