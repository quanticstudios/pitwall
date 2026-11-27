package app

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

type railBackend struct {
	*FakeBackend
	snap session.Snapshot
}

func (b railBackend) Chat(string) (session.Snapshot, bool) { return b.snap, true }
func (b railBackend) SessionHost(string) string            { return "" }
func (b railBackend) Hosts() []sidebar.HostInfo            { return nil }
func (b railBackend) HostOf(id string) string {
	if id == "wq" {
		return "box"
	}
	return ""
}

// railState adds an orchestrator chat tab "wo" to b, and with workers
// the mock's five workers and a merged one under it.
func railState(b railBackend, now time.Time, workers bool) {
	prov := model.ProviderClaude
	add := func(w model.Workspace) {
		w.SessionID, w.Path = "s1", "/tmp"
		b.st.Workspaces = append(b.st.Workspaces, w)
		s1 := &b.st.Sessions[slices.IndexFunc(b.st.Sessions, func(s model.Session) bool { return s.ID == "s1" })]
		s1.Order = append(s1.Order, w.ID)
	}
	add(model.Workspace{ID: "wo", Name: "orchestrator", Label: "Phone app, production pass", Chat: "co", Branch: "main"})
	b.st.Chats = []model.Chat{{ID: "co", WorkspaceID: "wo", Provider: prov, State: session.StateReady, Title: "Phone app, production pass"}}
	b.st.Activities = []model.Activity{{PaneID: "co", WorkspaceID: "wo", Provider: prov, State: model.StateWatching, UpdatedAt: now}}
	if !workers {
		return
	}
	ago := func(m int) time.Time { return now.Add(-time.Duration(m) * time.Minute) }
	for _, w := range []struct {
		id, name string
		state    model.AgentState
		at       int
		pr       model.Reason
	}{
		{"ws", "since-you-left", model.StateAwaitingInput, 12, ""},
		{"wc", "crisp-borders", model.StateCompleted, 4, model.ReasonChecksFailing},
		{"wq", "question-answer", model.StateWorking, 6, ""},
		{"wn", "phone-new-agent", model.StateCompleted, 8, model.ReasonReviewRequired},
		{"wp", "chat-spinner", model.StateCompleted, 2, model.ReasonChecksPending},
		{"wd", "phone-polish", model.StateCompleted, 41, model.ReasonMerged},
	} {
		add(model.Workspace{ID: w.id, Name: w.name, NameSet: true, Parent: "wo"})
		b.st.Activities = append(b.st.Activities, model.Activity{PaneID: w.id + "p", WorkspaceID: w.id, Provider: prov, State: w.state, UpdatedAt: ago(w.at)})
		if w.pr != "" {
			if b.st.PRs == nil {
				b.st.PRs = map[string]model.PR{}
			}
			state := model.PROpen
			if w.pr == model.ReasonMerged {
				state = model.PRMerged
			}
			b.st.PRs[w.id] = model.PR{State: state, Reason: w.pr}
		}
	}
}

// TestWorkRailShots writes headless PNGs of an orchestrator's chat at
// 1440 wide, without workers and with the mock's five, to
// $PITWALL_RAIL_SHOTS; it skips without it.
func TestWorkRailShots(t *testing.T) {
	dir := os.Getenv("PITWALL_RAIL_SHOTS")
	if dir == "" {
		t.Skip("PITWALL_RAIL_SHOTS is not set")
	}
	old := findAide
	findAide = func() []string { return nil }
	t.Cleanup(func() { findAide = old })
	now := time.Now()
	t1 := "t1"
	prov := model.ProviderClaude
	snap := session.Snapshot{SessionID: "co", Provider: prov, SessionState: session.StateReady, Content: []session.Item{
		{ID: "u1", Kind: session.KindUserPrompt, Text: "Run the phone app production pass with five workers.", TurnID: &t1, Provider: &prov},
		{ID: "a1", Kind: session.KindAssistantText, TurnID: &t1, Provider: &prov, Text: "Five workers are running. phone-polish merged as PR #566. since-you-left asks where its card should live, and crisp-borders failed CI."},
	}, Turns: []session.TurnSummary{{ID: t1, State: session.TurnCompleted}}}
	size := image.Pt(1440, 900)
	for _, c := range []struct {
		name    string
		workers bool
	}{{"workrail-before", false}, {"workrail-after", true}} {
		b := railBackend{NewFakeBackend(), snap}
		railState(b, now, c.workers)
		u := &ui{b: b, th: theme.Dark(), panes: map[string]*paneUI{}, nav: nav{keys: aide}}
		u.cfg.Font.UISize, u.cfg.Font.ChatSize = 13, 13
		st := b.State()
		u.nav.sync(&st)
		u.nav.selectWorkspace(&st, "wo", "")
		win, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Skip("no headless GPU:", err)
		}
		var r input.Router
		var ops op.Ops
		at := now
		for range 5 {
			ops.Reset()
			at = at.Add(200 * time.Millisecond)
			u.layout(gl.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: gl.Exact(size), Now: at})
			r.Frame(&ops)
		}
		if err := win.Frame(&ops); err != nil {
			t.Fatal(err)
		}
		out := image.NewRGBA(image.Rectangle{Max: size})
		if err := win.Screenshot(out); err != nil {
			t.Fatal(err)
		}
		win.Release()
		f, err := os.Create(filepath.Join(dir, c.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, out); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}
