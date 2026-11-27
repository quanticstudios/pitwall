//go:build !android

package mobile

import (
	"errors"
	"image"
	"image/png"
	"os"
	"slices"
	"strings"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/phone"
	"github.com/quanticstudios/pitwall/internal/remote"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// ListStates are the start screen's states ListShot draws: the Tabs tree,
// the Agents feed, and the feed with its first card dragged right.
var ListStates = []string{"tabs", "agents", "agents-swipe"}

// demoFleet is six agents on two machines, desktop and box, at now.
func demoFleet(now time.Time) ([]phone.Machine, map[string][]remote.Tab) {
	ago := func(m time.Duration) int64 { return now.Add(-m * time.Minute).UnixNano() }
	tab := func(id, name, agent, state, label string, at int64) remote.Tab {
		t := remote.Tab{ID: id, Branch: name}
		t.Pane, t.Tab, t.Session, t.Agent, t.State, t.Label, t.At = "p-"+id, name, "main", agent, state, label, at
		return t
	}
	billing := tab("billing", "billing", "claude", "pending-approval", "Approval", ago(2))
	billing.Detail, billing.Answer = "go test ./...", true
	arke := tab("arke", "arke", "codex", "awaiting-input", "Input", ago(14))
	arke.Detail, arke.Reply = "Four decisions before the device run", true
	sinter := tab("sinter", "sinter", "antigravity", "working", "Working", ago(0))
	sinter.Spinner = "Benchmarking v4-SFT, 413 of 1,691"
	away := tab("away", "since-you-left", "claude", "error", "Error", ago(12))
	away.Detail = "needs the card's placement"
	qa := tab("qa", "question-answer", "claude", "working", "Working", ago(1))
	qa.Spinner = "2 of 3 steps, running tests"
	polish := tab("polish", "phone-polish", "claude", "completed", "Done", ago(40))
	polish.Detail = "15 fixes, PR #566 merged"
	ms := []phone.Machine{{ID: "d", Name: "desktop", Server: "https://desktop.invalid"}, {ID: "b", Name: "box", Server: "https://box.invalid"}}
	return ms, map[string][]remote.Tab{"d": {billing, sinter, away, polish}, "b": {arke, qa}}
}

// demoFleetApp is the app paired with demoFleet's machines, both online.
func demoFleetApp(th *theme.Theme, now time.Time) *App {
	ms, tabs := demoFleet(now)
	a := &App{th: th, links: map[string]*link{}}
	a.sb.TreeOnly, a.sb.Fill, a.sb.Touch, a.sb.ExpandAll, a.sb.Answers = true, true, true, true, true
	a.sb.AllHosts, a.sb.SeparateWorkers, a.sb.Rev = true, true, 1
	a.fleet = phone.NewFleet(ms)
	for _, m := range ms {
		a.fleet.Set(m.ID, tabs[m.ID])
		a.links[m.ID] = &link{m: m, online: true}
	}
	a.refresh()
	return a
}

// ListShot draws the start screen in state at 360x760dp, scale px per dp,
// with demoFleet's agents, and writes it to path as a PNG, with no window.
func ListShot(th *theme.Theme, state, path string, scale float32) error {
	if !slices.Contains(ListStates, state) {
		return errors.New("no list state " + state + "; one of " + strings.Join(ListStates, ", "))
	}
	now := time.Date(2026, 11, 27, 9, 41, 0, 0, time.UTC)
	a := demoFleetApp(th, now)
	a.list.feed = state != "tabs"
	size := image.Pt(int(360*scale), int(760*scale))
	win, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		return err
	}
	defer win.Release()
	var r input.Router
	var ops op.Ops
	for i := range 3 {
		if i == 2 && state == "agents-swipe" {
			// why: set after the first frames, whose new handlers take a Cancel.
			c := a.triage.card(phone.NS("b", "p-arke"))
			c.drag = swipe{down: true, took: true, dx: 0.36 * float32(size.X-int(24*scale))}
		}
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Now: now,
			Metric:      unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Exact(size)}
		a.layoutFrame(gtx)
		r.Frame(&ops)
	}
	if err := win.Frame(&ops); err != nil {
		return err
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
