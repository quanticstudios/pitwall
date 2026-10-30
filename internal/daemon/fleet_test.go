package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/agent"
	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/forge"
	"github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
)

// fleetDaemon has a parent tab "p" with pane "pa" and its worker "w",
// pane "wa", on the small lane since start, both with Claude Code at its
// prompt. s is what Options.Fleet returns.
func fleetDaemon(t *testing.T, start time.Time, s *config.Settings) (*Daemon, *fakes) {
	t.Helper()
	tab := func(id, pane string) []model.Tab {
		return []model.Tab{{ID: id + "t", Layout: &layout.Node{Pane: pane}}}
	}
	f := &fakes{statsCalls: map[string]int{}}
	f.saved = model.State{
		Sessions: []model.Session{{ID: "s", Name: "work"}},
		Workspaces: []model.Workspace{
			{ID: "p", SessionID: "s", Name: "orchestrator", Tabs: tab("p", "pa"), ActiveTab: "pt"},
			{ID: "w", SessionID: "s", Name: "fix-login", NameSet: true, Branch: "fix-login", Path: "/r", Parent: "p", Tabs: tab("w", "wa"), ActiveTab: "wt",
				Lane: model.Lane{Name: "small", Start: start}},
		},
		Panes: []model.Pane{{ID: "pa", WorkspaceID: "p"}, {ID: "wa", WorkspaceID: "w"}},
	}
	o := f.options()
	o.Derive = agent.Derive
	o.Fleet = func() config.Settings { return *s }
	d, err := NewWith(o)
	if err != nil {
		t.Fatal(err)
	}
	claudeHook(t, d, "pa", `{"hook_event_name":"SessionStart","source":"startup"}`)
	claudeHook(t, d, "wa", `{"hook_event_name":"SessionStart","source":"startup"}`)
	return d, f
}

func fleetSettings() config.Settings {
	return config.Settings{Fleet: config.FleetSettings{On: true, Nudge: "Wrap up now.", Escalate: "parent", Lanes: []config.LaneSettings{
		{Name: "small", Preset: "Quick", Timebox: 30 * time.Minute},
		{Name: "normal", Timebox: time.Hour},
		{Name: "big", Timebox: 2 * time.Hour},
	}}}
}

func snapshot(d *Daemon) model.State {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshot()
}

// quiet fails when p got more than n bytes within a moment.
func quiet(t *testing.T, p *fakePane, n int, what string) {
	t.Helper()
	time.Sleep(50 * time.Millisecond)
	if got := p.got(); len(got) != n {
		t.Fatalf("%s: typed %q", what, got[n:])
	}
}

// TestFleetTimebox: a lane worker past its timebox with no pull request
// gets one nudge, its parent one line past twice the timebox, and an open
// pull request stops both.
func TestFleetTimebox(t *testing.T) {
	t0 := time.Now()
	s := fleetSettings()
	d, f := fleetDaemon(t, t0, &s)
	worker, parent := f.paneNamed("wa"), f.paneNamed("pa")

	d.checkFleet(t0.Add(10 * time.Minute))
	quiet(t, worker, 0, "inside the timebox")
	d.checkFleet(t0.Add(31 * time.Minute))
	want := "[pitwall: message from a script] Wrap up now."
	if got := gotAfter(t, worker, 0, len(want)); !strings.HasPrefix(got, want) {
		t.Fatalf("worker got %q, want %q", got, want)
	}
	claudeHook(t, d, "wa", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	n := len(worker.got())
	d.checkFleet(t0.Add(40 * time.Minute))
	quiet(t, worker, n, "a second nudge")
	quiet(t, parent, 0, "the parent before twice the timebox")

	d.checkFleet(t0.Add(61 * time.Minute))
	if got := gotAfter(t, parent, 0, 120); !strings.Contains(got, "fix-login on the small lane is past twice its 30m timebox with no pull request open.") {
		t.Fatalf("parent got %q", got)
	}
	m := len(parent.got())
	d.checkFleet(t0.Add(90 * time.Minute))
	quiet(t, parent, m, "a second parent message")
	quiet(t, worker, n, "a nudge after the parent's")
	if l := snapshot(d).Workspaces[1].Lane; !l.Nudged || !l.Told {
		t.Fatalf("lane %+v: want Nudged and Told kept for a restart", l)
	}

	// With a pull request open, nothing goes to either.
	d2, f2 := fleetDaemon(t, t0, &s)
	d2.mu.Lock()
	d2.st.PRs["w"] = model.PR{Number: 3, State: model.PROpen}
	d2.mu.Unlock()
	d2.checkFleet(t0.Add(3 * time.Hour))
	quiet(t, f2.paneNamed("wa"), 0, "a worker with a pull request")
	quiet(t, f2.paneNamed("pa"), 0, "the parent of a worker with a pull request")

	// Fleet mode off: nothing.
	s.Fleet.On = false
	d3, f3 := fleetDaemon(t, t0, &s)
	d3.checkFleet(t0.Add(3 * time.Hour))
	quiet(t, f3.paneNamed("wa"), 0, "fleet mode off")
}

// TestFleetEscalate: once auto_send_failures has sent a lane worker's
// failures twice and CI fails on a new head, escalate = "parent" tells its
// parent once, and "respawn" replaces its agent with the big lane's preset
// in the same tab, the contract and what went wrong as its brief.
func TestFleetEscalate(t *testing.T) {
	t0 := time.Now()
	failing := []model.Check{{Name: "test", State: model.CheckFail}}
	setup := func(s *config.Settings) (*Daemon, *fakes) {
		d, f := fleetDaemon(t, t0, s)
		d.o.PR = func(context.Context, string) (*model.PR, error) {
			return &model.PR{Number: 7, State: model.PROpen, Checks: failing, Head: "h3"}, nil
		}
		d.o.AutoSendFailures = func() bool { return true }
		d.o.Head = func(context.Context, string) (string, error) { return "h3", nil }
		d.o.Failures = func(context.Context, string) ([]forge.FailedCheck, error) {
			return []forge.FailedCheck{{Name: "test", URL: "https://ci/1", Log: "boom"}}, nil
		}
		d.o.DiffStat = func(context.Context, string) (string, error) { return "login.go +4 -1", nil }
		d.mu.Lock()
		d.prs.sent[prKey("/r", "fix-login")] = []string{"h1", "h2"}
		d.mu.Unlock()
		return d, f
	}

	s := fleetSettings()
	d, f := setup(&s)
	d.pollPRs(context.Background(), t0)
	parent := f.paneNamed("pa")
	if got := gotAfter(t, parent, 0, 140); !strings.Contains(got, "fix-login: CI failed again on pull request #7 after pitwall sent it the failures twice (test).") {
		t.Fatalf("parent got %q", got)
	}
	n := len(parent.got())
	d.pollPRs(context.Background(), t0.Add(time.Hour))
	quiet(t, parent, n, "a second escalation")
	quiet(t, f.paneNamed("wa"), 0, "a third send of the failures")

	// The snapshot shows the worker's finished turn as PR open.
	claudeHook(t, d, "wa", `{"hook_event_name":"Stop","last_assistant_message":"ok"}`)
	if i := slices.IndexFunc(snapshot(d).Activities, func(a model.Activity) bool { return a.PaneID == "wa" }); i < 0 || snapshot(d).Activities[i].State != model.StatePROpen {
		t.Fatalf("snapshot activities %+v: want wa pr-open", snapshot(d).Activities)
	}

	contract := filepath.Join(t.TempDir(), "contract.md")
	os.WriteFile(contract, []byte("Commit on the branch.\n"), 0o644)
	s = fleetSettings()
	s.Fleet.Escalate, s.Fleet.Contract = "respawn", contract
	s.Fleet.Lanes[2].Preset = "Deep"
	s.Presets = []config.AgentPreset{{Name: "Deep", Agent: "deepagent", Args: []string{"--effort", "max"}, Env: []string{"A=1"}, Guard: true}}
	d, f = setup(&s)
	f.paneNamed("wa").draw("$ go test ./...", "FAIL login_test.go:12")
	d.pollPRs(context.Background(), t0)
	snap := snapshot(d)
	var panes []model.Pane
	for _, p := range snap.Panes {
		if p.WorkspaceID == "w" {
			panes = append(panes, p)
		}
	}
	if len(panes) != 1 || panes[0].ID == "wa" {
		t.Fatalf("worker tab's panes %+v: want one new pane", panes)
	}
	cmd := panes[0].Cmd
	if len(cmd) != 4 || cmd[0] != "deepagent" || cmd[1] != "--effort" {
		t.Fatalf("respawned %q", cmd)
	}
	brief := cmd[3]
	for _, w := range []string{"Commit on the branch.\n\nYou take over this branch", "pull request #7", "login.go +4 -1", "boom", "FAIL login_test.go:12"} {
		if !strings.Contains(brief, w) {
			t.Errorf("brief lacks %q:\n%s", w, brief)
		}
	}
	if !slices.Equal(panes[0].Env, []string{"A=1", "PITWALL_GUARD=1"}) {
		t.Errorf("env %q", panes[0].Env)
	}
	if l := snap.Workspaces[1].Lane; l.Name != "big" || !l.Escalated {
		t.Errorf("lane %+v: want big, escalated", l)
	}
	quiet(t, f.paneNamed("pa"), 0, "the parent of a respawned worker")
}
