package panel

import (
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func views(in *Input) string {
	var out []string
	for _, t := range tabsFor(in) {
		out = append(out, string(t.view)+":"+t.count)
	}
	return strings.Join(out, " ")
}

// TestTabs: Subagents and Plan show when the feed has them and always
// for Claude; pi and Codex without them get neither; a shell gets Flow and
// Changes only.
func TestTabs(t *testing.T) {
	now := time.Now()
	full := &flow.Feed{
		Plan:      []flow.Step{{State: flow.StepDone}, {State: flow.StepActive}, {}},
		Subagents: []flow.Subagent{{ID: "a", Start: now}, {ID: "b", Start: now, End: now}},
	}
	for _, c := range []struct {
		name string
		in   Input
		want string
	}{
		{"shell", Input{Pane: &model.Pane{}, Git: true, Files: make([]gitstat.FileStat, 2)}, "flow: changes:2"},
		{"claude, no transcript", Input{Pane: &model.Pane{Provider: model.ProviderClaude}}, "flow: subagents: plan: changes: timeline:"},
		{"claude", Input{Pane: &model.Pane{Provider: model.ProviderClaude}, Feed: full}, "flow: subagents:2 plan:1/3 changes: timeline:3"},
		{"codex, empty", Input{Pane: &model.Pane{Provider: model.ProviderCodex}, Feed: &flow.Feed{}}, "flow: changes: timeline:"},
		{"codex, plan", Input{Pane: &model.Pane{Provider: model.ProviderCodex}, Feed: &flow.Feed{Plan: []flow.Step{}}}, "flow: plan: changes: timeline:"},
		{"pi", Input{Activity: &model.Activity{Provider: model.ProviderPi}, Feed: &flow.Feed{}}, "flow: changes: timeline:"},
	} {
		if got := views(&c.in); got != c.want {
			t.Errorf("%s: tabs %q, want %q", c.name, got, c.want)
		}
	}
	in := Input{Pane: &model.Pane{Provider: model.ProviderClaude}, Feed: full}
	if !tabsFor(&in)[1].live {
		t.Error("Subagents has no live dot while one runs")
	}
}

// TestPlanCounts counts done steps only.
func TestPlanCounts(t *testing.T) {
	done, n := planCounts([]flow.Step{{State: flow.StepDone}, {State: flow.StepActive}, {State: flow.StepDone}, {}})
	if done != 2 || n != 4 {
		t.Fatalf("planCounts = %d of %d, want 2 of 4", done, n)
	}
}

// TestTimeline: prompts, calls, subagent starts and ends, finished turns
// and a pending approval merge in time order; a run of calls to one tool
// folds into a line; a failed or running call stays on its own.
func TestTimeline(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	in := Input{
		Decide: "jev",
		Feed: &flow.Feed{
			Turns: []flow.Turn{
				{Prompt: "first", Start: at(0), End: at(10), Calls: []flow.Call{{Time: at(1), Tool: "Read", Arg: "a/x.go"}}},
				{Prompt: "second", Start: at(20), Calls: []flow.Call{
					{Time: at(21), Tool: "Read", Arg: "a/b.go"}, {Time: at(22), Tool: "Read", Arg: "c.go"},
					{Time: at(23), Tool: "Read", Arg: "d.go"}, {Time: at(24), Tool: "Read", Arg: "e.go"},
					{Time: at(30), Tool: "Bash", Arg: "go test", Failed: true}, {Time: at(31), Tool: "Bash", Arg: "go vet"},
					{Time: at(32), Tool: "Bash", Arg: "go build", Running: true}, {Time: at(33), Tool: "Bash", Arg: "go doc"},
				}},
			},
			Subagents: []flow.Subagent{{Name: "recon", Type: "Explore", Start: at(25), End: at(40)}},
		},
		Activity: &model.Activity{State: model.StatePendingApproval, Detail: "rm -rf build", UpdatedAt: at(45), Advice: "allow", AdviceP: 0.71, Urgency: "soon"},
	}
	var got []string
	for _, e := range timeline(&in) {
		s := e.text
		if e.note != "" {
			s += " | " + e.note
		}
		got = append(got, s)
	}
	want := []string{
		"You: first", "Read a/x.go", "Finished in 10s",
		"You: second", "Read b.go, c.go and 2 more", "Spawned Explore: recon",
		"Bash go test failed", "Bash go vet", "Bash go build", "Bash go doc", "recon finished in 15s",
		"Approval for rm -rf build | Jev: allow 71%, triage soon",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("timeline:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestGraph: a working turn with a running subagent and a pending
// approval draws Prompt, Agent, Subagents, the gate and a pending
// Outcome; a finished one draws a completed Outcome.
func TestGraph(t *testing.T) {
	now := time.Now()
	in := Input{
		Now:      now,
		Decide:   "jev",
		Pane:     &model.Pane{Provider: model.ProviderClaude},
		Activity: &model.Activity{State: model.StatePendingApproval, Detail: "go test", Advice: "allow", AdviceP: 0.99},
		Feed: &flow.Feed{
			Turns:     []flow.Turn{{Prompt: "p", Start: now.Add(-time.Minute), Subagents: []int{0}}},
			Subagents: []flow.Subagent{{Name: "s", Start: now.Add(-time.Second)}},
		},
	}
	var got []string
	for _, n := range graphFor(&in) {
		got = append(got, n.kind+"/"+n.edge)
	}
	if want := []string{"Prompt/", "Agent/prompt", "Subagents/spawn", "Jev gate/return", "Outcome/then"}; !slices.Equal(got, want) {
		t.Fatalf("nodes %v, want %v", got, want)
	}
	in.Activity = &model.Activity{State: model.StateCompleted}
	in.Feed.Subagents[0].End = now
	g := graphFor(&in)
	if o := g[len(g)-1]; o.state != nodeCompleted || o.title != "Done" || g[2].state != nodeCompleted {
		t.Fatalf("finished turn: outcome %+v, subagents %+v", o, g[2])
	}
}

// TestLayoutViews draws every view of a full feed, and the empty states,
// without panicking, and asks for frames only while something is live.
func TestLayoutViews(t *testing.T) {
	th := theme.Dark()
	now := time.Now()
	full := Input{
		Pane:     &model.Pane{ID: "p", Provider: model.ProviderClaude, Transcript: "/t"},
		Activity: &model.Activity{State: model.StateWorking},
		Feed: &flow.Feed{
			Turns: []flow.Turn{{Prompt: "old", Start: now.Add(-time.Hour), End: now.Add(-50 * time.Minute)},
				{Prompt: "new", Start: now.Add(-time.Minute), Subagents: []int{0}, Calls: []flow.Call{{Time: now, Tool: "Bash", Arg: "make", Running: true}}}},
			Plan:      []flow.Step{{Text: "a", State: flow.StepDone}, {Text: "b", State: flow.StepActive}, {Text: "c"}},
			Subagents: []flow.Subagent{{ID: "s", Name: "s", Prompt: strings.Repeat("long prompt ", 200), Start: now, Calls: []flow.Call{{Time: now, Tool: "Read", Arg: "x", Running: true}}}},
		},
		Git: true, Base: "main", Files: []gitstat.FileStat{{Path: "a/b.go", Add: 1, Status: 'M'}, {Path: "new.txt", Add: 3, Status: '?'}},
		Now: now,
	}
	empty := []Input{{}, {Pane: &model.Pane{}}, {Pane: &model.Pane{Provider: model.ProviderPi}}, {Pane: &model.Pane{Provider: model.ProviderCodex}, Feed: &flow.Feed{}}}
	var ops op.Ops
	frame := func(p *Panel, in Input, w int) {
		ops.Reset()
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Constraints: layout.Exact(image.Pt(w, 800)), Now: now}
		p.Layout(gtx, th, in)
	}
	for _, v := range []View{ViewFlow, ViewSubagents, ViewPlan, ViewChanges, ViewTimeline} {
		for _, detail := range []int{-1, 0} {
			var p Panel
			p.SetView(v, detail)
			frame(&p, full, 432)
			frame(&p, full, 320)
			if p.view != v {
				t.Fatalf("view %s fell back to %s", v, p.view)
			}
			if !p.live && v != ViewChanges {
				t.Errorf("view %s (detail %d) with a running call is not live", v, detail)
			}
			for _, in := range empty {
				frame(&p, in, 432)
			}
		}
	}
	var p Panel
	p.SetView(ViewChanges, -1)
	frame(&p, Input{Pane: &model.Pane{}, Git: true}, 432)
	if p.live || p.tick {
		t.Error("a still panel asks for frames")
	}
}
