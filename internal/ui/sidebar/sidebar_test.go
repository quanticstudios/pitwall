package sidebar

import (
	"image"
	"math"
	"slices"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestRenameEndsWhenHiddenOrBlurred(t *testing.T) {
	for _, name := range []string{"visible session", "visible group", "collapsed", "deleted session", "deleted group", "detached", "blurred session", "blurred group"} {
		t.Run(name, func(t *testing.T) {
			st := model.State{
				Projects:   []model.Project{{ID: "g", Name: "Group", Kind: model.ProjectGroup}},
				Workspaces: []model.Workspace{{ID: "w", Name: "Session", ProjectID: "g"}},
			}
			var s Sidebar
			var r input.Router
			var ops op.Ops
			th := theme.Dark()
			frame := func() []Event {
				ops.Reset()
				gtx := layout.Context{Ops: &ops, Source: r.Source(),
					Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
					Constraints: layout.Exact(image.Pt(288, 600)), Now: time.Now()}
				_, events := s.Layout(gtx, th, &st, "w")
				r.Frame(&ops)
				return events
			}
			frame()
			group := name == "visible group" || name == "deleted group" || name == "blurred group"
			if group {
				s.startRename("", "g", "Group")
			} else {
				s.startRename("w", "", "Session")
			}
			frame()
			frame()
			if !s.Editing() || !r.Source().Focused(&s.editor) {
				t.Fatal("visible rename did not retain editor focus")
			}
			switch name {
			case "collapsed":
				s.expanded["g"] = false
			case "deleted session":
				st.Workspaces = nil
				r.Queue(key.Event{Name: key.NameReturn, State: key.Press})
			case "deleted group":
				st.Projects = nil
				r.Queue(key.Event{Name: key.NameReturn, State: key.Press})
			case "detached":
				st.Workspaces[0].Detached = true
			case "blurred session", "blurred group":
				r.Source().Execute(key.FocusCmd{})
			}
			if events := frame(); len(events) != 0 {
				t.Fatalf("cancel emitted events: %v", events)
			}
			want := name == "visible session" || name == "visible group"
			if s.Editing() != want {
				t.Fatalf("Editing() = %v, want %v", s.Editing(), want)
			}
		})
	}
}

func TestRelTime(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		-time.Minute:     "just now",
		4 * time.Second:  "just now",
		42 * time.Second: "42s ago",
		59 * time.Minute: "59m ago",
		3 * time.Hour:    "3h ago",
		50 * time.Hour:   "2d ago",
	} {
		if got := relTime(now, now.Add(-d)); got != want {
			t.Errorf("relTime(-%v) = %q, want %q", d, got, want)
		}
	}
	if got := relTime(now, time.Time{}); got != "" {
		t.Errorf("zero time = %q", got)
	}
}

func TestStateColors(t *testing.T) {
	th := &theme.Theme{Sidebar: theme.Hex("#08090c"), Red: theme.Hex("#ff6b6b"), Yellow: theme.Hex("#ffc533"),
		Blue: theme.Hex("#57c1ff"), Purple: theme.Hex("#bd93ff"), Green: theme.Hex("#59d499"),
		Primary: theme.Hex("#2997ff"), SurfaceSecondary: theme.Hex("#1c1f26")}
	for s, want := range map[model.AgentState]any{
		model.StateError: th.Red, model.StatePendingApproval: th.Yellow, model.StateAwaitingInput: th.Yellow,
		model.StateWorking: th.Blue, model.StateConnecting: th.Blue, model.StatePlanReady: th.Purple,
		model.StateCompleted: th.Green, model.StateTerminalRunning: th.Green,
	} {
		if got := stateColor(th, s); got != want {
			t.Errorf("stateColor(%s) = %v, want %v", s, got, want)
		}
	}
	working := &model.Activity{State: model.StateWorking}
	if rowBase(th, working, false, true) != th.Sidebar {
		t.Error("a working row must not take the hover fill")
	}
	if rowBase(th, nil, false, true) != th.SurfaceSecondary {
		t.Error("an idle row takes the hover fill")
	}
	if got, want := rowBase(th, nil, true, false), theme.Mix(th.Sidebar, th.Primary, 0.12); got != want {
		t.Errorf("active row = %v, want %v", got, want)
	}
}

func TestParsePathArc(t *testing.T) {
	// lucide git-branch: "M15 6a9 9 0 0 0-9 9" is a quarter circle around (15, 15).
	segs := parsePath("M15 6a9 9 0 0 0-9 9V3")
	if len(segs) != 3 || segs[1].op != 'A' || segs[2].op != 'L' {
		t.Fatalf("segs = %+v", segs)
	}
	if c := segs[1].pts[0]; c.Sub(f32.Pt(15, 15)).X > 1e-3 || c.Sub(f32.Pt(15, 15)).Y > 1e-3 {
		t.Errorf("center = %v", c)
	}
	if a := segs[1].angle; math.Abs(float64(a)+math.Pi/2) > 1e-3 {
		t.Errorf("sweep = %v, want -pi/2", a)
	}
	if end := segs[2].pts[0]; end != f32.Pt(6, 3) {
		t.Errorf("V3 ended at %v", end)
	}
}

func TestProjectIcons(t *testing.T) {
	var s Sidebar
	if len(projectIcons) != len(s.iconBtn) {
		t.Fatalf("%d icons, %d buttons", len(projectIcons), len(s.iconBtn))
	}
	for _, ic := range projectIcons {
		if len(parsePath(ic.d)) == 0 { // panics on a command the parser lacks
			t.Errorf("%s: empty path", ic.name)
		}
	}
	if projectIcon("code") == icFolder || projectIcon("no-such-icon") != icFolder || projectIcon("") != icFolder {
		t.Error("projectIcon lookup or fallback is wrong")
	}
}

func TestShortPathAndPill(t *testing.T) {
	for in, want := range map[string]string{
		"/home/me": "~", "/home/me/Work/x": "~/Work/x", "/home/meow": "/home/meow", "/tmp": "/tmp", "": "",
	} {
		if got := shortPath(in, "/home/me"); got != want {
			t.Errorf("shortPath(%q) = %q, want %q", in, got, want)
		}
	}
	run := model.Activity{Provider: model.ProviderTerminal, State: model.StateTerminalRunning, Detail: "go"}
	if PillText(run) != "go" {
		t.Errorf("running terminal pill = %q", PillText(run))
	}
	run.Detail = ""
	if PillText(run) != "Running" {
		t.Errorf("no command = %q", PillText(run))
	}
}

// TestSelection walks plain, Ctrl and Shift clicks over ungrouped w1..w3
// and w4 in an expanded group.
func TestSelection(t *testing.T) {
	st := &model.State{
		Projects: []model.Project{{ID: "g", Name: "g", Kind: model.ProjectGroup}},
		Workspaces: []model.Workspace{
			{ID: "w4", ProjectID: "g"}, {ID: "w1"}, {ID: "w2"}, {ID: "w3", ProjectID: "gone"},
		},
	}
	s := Sidebar{expanded: map[string]bool{"g": true}, selected: map[string]bool{}}
	v := &view{st: st, active: "w1", byProject: map[string][]model.Workspace{}, activity: map[string]*model.Activity{}}
	v.byProject[""] = st.Workspaces[1:]
	v.byProject["g"] = st.Workspaces[:1]
	if got := s.order(v); !slices.Equal(got, []string{"w1", "w2", "w3", "w4"}) {
		t.Fatalf("order %v", got)
	}
	picked := func() []string {
		var out []string
		for _, id := range s.order(v) {
			if s.selected[id] {
				out = append(out, id)
			}
		}
		return out
	}
	s.click(v, "w3", key.ModShortcut)
	if got := picked(); !slices.Equal(got, []string{"w1", "w3"}) {
		t.Fatalf("Ctrl+click from the open session: %v", got)
	}
	if len(s.events) != 0 {
		t.Fatalf("Ctrl+click selected a session: %v", s.events)
	}
	s.click(v, "w4", key.ModShift)
	if got := picked(); !slices.Equal(got, []string{"w3", "w4"}) {
		t.Fatalf("Shift+click from the anchor: %v", got)
	}
	if got := s.targets(v, "w4"); !slices.Equal(got, []string{"w4", "w3"}) {
		t.Fatalf("targets in state order: %v", got)
	}
	if got := s.targets(v, "w2"); !slices.Equal(got, []string{"w2"}) {
		t.Fatalf("a row outside the selection targets itself: %v", got)
	}
	s.click(v, "w2", 0)
	if len(s.selected) != 0 || len(s.events) != 1 || s.events[0] != (SelectWorkspace{WorkspaceID: "w2"}) {
		t.Fatalf("plain click: %v %v", s.selected, s.events)
	}
}
