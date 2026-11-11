package sidebar

import (
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	lay "github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/board"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// sharedState is a shared session: a local tab and a box tab whose agent
// waits on an approval, with box down since three minutes before now.
func sharedState(now time.Time) (model.State, []HostInfo) {
	st := model.State{
		Sessions: []model.Session{{ID: "ls", Name: "daily-work"}},
		Workspaces: []model.Workspace{
			{ID: "lw", SessionID: "ls", Name: "local-tab"},
			{ID: "rw", SessionID: "ls", Name: "box-tab", Tabs: []model.Tab{{ID: "rt", Layout: &lay.Node{Pane: "rp"}}}, ActiveTab: "rt", UpdatedAt: now.Add(-time.Hour)},
		},
		Panes:      []model.Pane{{ID: "rp", WorkspaceID: "rw", Provider: model.ProviderCodex}},
		Activities: []model.Activity{{PaneID: "rp", WorkspaceID: "rw", Provider: model.ProviderCodex, State: model.StatePendingApproval, Command: "make", UpdatedAt: now.Add(-time.Hour)}},
	}
	hosts := []HostInfo{
		{Up: true, Sessions: []string{"ls"}},
		{Name: "box", SSH: "me@box", Note: "cannot reach box over ssh: timed out. Trying again in 30s.", Retry: now.Add(7 * time.Second), Since: now.Add(-3 * time.Minute)},
	}
	return st, hosts
}

// labels draws f and returns the text it laid out, in order.
func labels(f func(gtx layout.Context), w int, now time.Time) []string {
	var r input.Router
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(w, 800)), Now: now}
	f(gtx)
	r.Frame(&ops)
	var out []string
	var walk func([]input.SemanticNode)
	walk = func(nodes []input.SemanticNode) {
		for _, n := range nodes {
			out = append(out, n.Desc.Label)
			walk(n.Children)
		}
	}
	walk(r.AppendSemantics(nil))
	return out
}

// A down host's row shows Offline and how old its state is, never Allow
// and Deny, and says to answer once the host is back.
func TestDownHostRow(t *testing.T) {
	now := time.Now()
	st, hosts := sharedState(now)
	s := Sidebar{Answers: true, Hosts: hosts, HostOf: func(id string) string {
		if id == "rw" || id == "rp" {
			return "box"
		}
		return ""
	}}
	got := labels(func(gtx layout.Context) { s.Layout(gtx, theme.Dark(), &st, "ls", "lw") }, 288, now)
	for _, want := range []string{"Offline", "last seen 3m ago", "Answer when box is back"} {
		if !slices.Contains(got, want) {
			t.Errorf("no %q in %q", want, got)
		}
	}
	if slices.Contains(got, "Allow") || slices.Contains(got, "Approval") {
		t.Errorf("a down host's row answers or shows its old state: %q", got)
	}
}

// In a shared session the tree's machine lines stand for box: no row of
// machines shows under the header, box's empty heading goes, and Waiting
// on you names box.
func TestSharedHostRow(t *testing.T) {
	now := time.Now()
	st, hosts := sharedState(now)
	s := Sidebar{session: "ls", Hosts: hosts, HostOf: func(id string) string {
		if id == "rw" {
			return "box"
		}
		return ""
	}}
	got := labels(func(gtx layout.Context) { s.hostList(gtx, theme.Dark(), &st) }, 800, now)
	heading := slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, "cannot reach box") })
	if heading || slices.Contains(got, "this machine") || slices.Contains(got, "offline, retry in 7s") || !slices.Contains(got, "box · Approval") {
		t.Fatalf("host list: %q", got)
	}
}

// The board's banner names the down hosts whose cards it shows.
func TestDownBanner(t *testing.T) {
	now := time.Now()
	_, hosts := sharedState(now)
	hosts = append(hosts, HostInfo{Name: "gpu"})
	b := board.Board{All: map[string]board.Card{"a": {Host: "box"}, "b": {Host: "box"}, "c": {}}}
	if bold, rest := downBanner(hosts, b); bold != "box is unreachable." || !strings.HasPrefix(rest, "Its 2 tabs show their last state") {
		t.Fatalf("%q %q", bold, rest)
	}
	one := board.Board{All: map[string]board.Card{"a": {Host: "box"}}}
	if _, rest := downBanner(hosts, one); !strings.HasPrefix(rest, "Its tab shows its last state") {
		t.Fatalf("one tab: %q", rest)
	}
	b.All["d"] = board.Card{Host: "gpu"}
	if bold, rest := downBanner(hosts, b); bold != "box and gpu are unreachable." || !strings.HasPrefix(rest, "Their 3 tabs") {
		t.Fatalf("%q %q", bold, rest)
	}
	hosts[1].Up, hosts[2].Up = true, true
	if bold, _ := downBanner(hosts, b); bold != "" {
		t.Fatalf("up hosts: %q", bold)
	}
}

// A down host's card has its host's chip, Offline, no answers and no
// allow_prompt, and the board says the host is unreachable.
func TestDownHostCard(t *testing.T) {
	now := time.Now()
	st, hosts := sharedState(now)
	s := Sidebar{Answers: true, Hosts: hosts, session: "ls"}
	s.BoardFocus()
	o := BoardOptions{Hosts: map[string]string{"rw": "box"}}
	var r input.Router
	var ops op.Ops
	var got []string
	for range 3 {
		now = now.Add(time.Second)
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Source: r.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(1200, 700)), Now: now}
		s.Board(gtx, theme.Dark(), &st, o)
		r.Frame(&ops)
	}
	var walk func([]input.SemanticNode)
	walk = func(nodes []input.SemanticNode) {
		for _, n := range nodes {
			got = append(got, n.Desc.Label)
			walk(n.Children)
		}
	}
	walk(r.AppendSemantics(nil))
	for _, want := range []string{"box", "Offline", "box is unreachable.", "Answer when box is back. The prompt may have changed meanwhile."} {
		if !slices.Contains(got, want) {
			t.Errorf("no %q in %q", want, got)
		}
	}
	if slices.Contains(got, "Allow") {
		t.Errorf("a down host's card offers Allow: %q", got)
	}
	s.bd.cur = "rw"
	s.bd.shown = map[string]bool{"rw": true}
	if ev, ok := s.BoardAnswer(true, now.Add(time.Minute)); ok {
		t.Errorf("allow_prompt answered a down host's card: %+v", ev)
	}
}

// TestAllHosts draws the phone's tree, where every tab names its machine:
// a session on laptop and box gets a line for each, in Hosts order, with
// box's latency and no line for gpu, which has no tab there; a session on
// box alone gets no line and no chip.
func TestAllHosts(t *testing.T) {
	now := time.Now()
	st := model.State{
		Sessions: []model.Session{{ID: "main", Name: "main"}, {ID: "solo", Name: "solo"}},
		Workspaces: []model.Workspace{
			{ID: "b1", SessionID: "main", Name: "box-tab"},
			{ID: "l1", SessionID: "main", Name: "laptop-tab"},
			{ID: "b2", SessionID: "solo", Name: "solo-tab"},
		},
	}
	hosts := []HostInfo{{Name: "laptop", Up: true}, {Name: "gpu", Up: true}, {Name: "box", Up: true, Latency: 38 * time.Millisecond}}
	on := map[string]string{"b1": "box", "l1": "laptop", "b2": "box"}
	s := Sidebar{TreeOnly: true, AllHosts: true, Hosts: hosts, HostOf: func(id string) string { return on[id] }}
	count := func(got []string, want string) int {
		n := 0
		for _, l := range got {
			if l == want {
				n++
			}
		}
		return n
	}
	got := labels(func(gtx layout.Context) { s.Layout(gtx, theme.Dark(), &st, "main", "") }, 288, now)
	l, b := slices.Index(got, "laptop"), slices.Index(got, "box")
	// The semantic tree repeats a label at each level; a line's name comes
	// with its health each time, a chip's alone.
	if count(got, "laptop") != count(got, "connecting") || count(got, "box") != count(got, "38 ms") || l > b || l < 0 {
		t.Fatalf("shared session %q; want a laptop line, then a box line, no chips", got)
	}
	if slices.Index(got, "laptop-tab") > b || slices.Index(got, "box-tab") < b {
		t.Fatalf("shared session %q; want each tab under its machine's line", got)
	}
	if !slices.Contains(got, "38 ms") || slices.Contains(got, "gpu") || slices.Contains(got, "This machine") {
		t.Fatalf("shared session %q; want box's latency, no gpu, no This machine", got)
	}
	got = labels(func(gtx layout.Context) { s.Layout(gtx, theme.Dark(), &st, "solo", "") }, 288, now)
	if count(got, "box") != 0 || !slices.Contains(got, "solo-tab") {
		t.Fatalf("one-machine session %q; want its tab with no line and no chip", got)
	}
}
