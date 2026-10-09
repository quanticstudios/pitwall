// Command pwdemo-sidebar shows the sidebar against a fake state that covers
// every agent state, for visual checks against aide.
//
// Flags force states that are hard to reach by clicking:
//
//	-update label  show the footer's update button with this label
//	-numbers       show the goto_tab digits on the first nine rows
//	-active id     the active tab (default ws-sidebar); groups other than
//	               its own start collapsed
//	-expand-all    start every group expanded
//	-unseen        mark every needs-you activity unseen
//	-size WxH      window size in dp (default 900x860)
//	-theme name    a built-in theme (default aide-dark)
//	-ui-size n     [font] ui_size
//	-answers       show Allow and Deny on approvals
//	-pane id       the focused pane of the active tab
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"slices"
	"time"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/flow"
	pwlayout "github.com/quanticstudios/pitwall/internal/layout"
	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func main() {
	var sb sidebar.Sidebar
	flag.StringVar(&sb.Update, "update", "", "footer update button label")
	flag.StringVar(&sb.Host, "host", "", "ssh host named in the header, as pitwall --host shows it")
	numbers := flag.Bool("numbers", false, "show goto_tab digits on the first nine rows")
	active := flag.String("active", "ws-sidebar", "active tab id")
	flag.BoolVar(&sb.ExpandAll, "expand-all", false, "start every group expanded")
	unseen := flag.Bool("unseen", false, "mark every needs-you activity unseen")
	size := flag.String("size", "900x860", "window size WxH in dp")
	flag.BoolVar(&sb.ShowCost, "cost", false, "show token use in hover cards in dollars, as [usage] show_cost does")
	themeName := flag.String("theme", "aide-dark", "built-in theme")
	uiSize := flag.Float64("ui-size", 0, "[font] ui_size; 0 for the default")
	flag.BoolVar(&sb.Answers, "answers", false, "show Allow and Deny on approvals")
	flag.StringVar(&sb.Pane, "pane", "", "the focused pane of the active tab")
	flag.Parse()
	tc, ok := config.Builtin(*themeName)
	if !ok {
		log.Fatalf("-theme %q: no such built-in theme", *themeName)
	}
	th, err := theme.New(tc, config.Font{UISize: *uiSize})
	if err != nil {
		log.Print(err)
	}
	sb.Usage = fakeUsage
	var width, height int
	if _, err := fmt.Sscanf(*size, "%dx%d", &width, &height); err != nil {
		log.Fatalf("-size %q: want WxH", *size)
	}
	if *numbers {
		sb.Numbers = [9]bool{true, true, true, true, true, true, true, true, true}
	}
	st := fakeState(time.Now())
	if *unseen {
		for i := range st.Activities {
			st.Activities[i].Unseen = model.NeedsYou(st.Activities[i].State)
		}
	}
	go func() {
		w := new(app.Window)
		w.Option(app.Title("pwdemo-sidebar"), app.Size(unit.Dp(width), unit.Dp(height)))
		if err := run(w, th, &sb, st, *active); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

// fakeUsage is the token use hover cards show: a Claude session past
// half its context, a Codex one near its window, nothing for the rest.
func fakeUsage(ws string) *flow.Usage {
	switch ws {
	case "ws-sidebar":
		return &flow.Usage{Model: "claude-opus-5-5", Context: 612_000, Models: map[string]flow.Tokens{
			"claude-opus-5-5": {Input: 3_200, Output: 88_400, CacheRead: 9_120_000, CacheWrite: 640_000, CacheWrite1h: 640_000},
		}}
	case "ws-term":
		return &flow.Usage{Model: "gpt-6.1-sol", Context: 231_000, Window: 258_400, Models: map[string]flow.Tokens{
			"gpt-6.1-sol": {Input: 120_000, Output: 22_000, CacheRead: 1_800_000},
		}}
	}
	return nil
}

func run(w *app.Window, th *theme.Theme, sb *sidebar.Sidebar, st model.State, active string) error {
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			paint.FillShape(gtx.Ops, th.Bg, clip.Rect{Max: e.Size}.Op())
			_, events := sb.Layout(gtx, th, &st, "", active)
			for _, ev := range events {
				log.Printf("event %#v", ev)
				apply(&st, &active, ev)
			}
			if len(events) > 0 {
				w.Invalidate()
			}
			e.Frame(gtx.Ops)
		}
	}
}

func apply(st *model.State, active *string, ev sidebar.Event) {
	ws := func(id string) *model.Workspace {
		for i := range st.Workspaces {
			if st.Workspaces[i].ID == id {
				return &st.Workspaces[i]
			}
		}
		return &model.Workspace{}
	}
	switch ev := ev.(type) {
	case sidebar.SelectWorkspace:
		*active = ev.WorkspaceID
	case sidebar.RenameTab:
		ws(ev.WorkspaceID).Name, ws(ev.WorkspaceID).NameSet = ev.Name, ev.Name != ""
	case sidebar.DetachSession:
		ws(ev.WorkspaceID).Detached = true
	case sidebar.AttachSession:
		ws(ev.WorkspaceID).Detached = false
		*active = ev.WorkspaceID
	case sidebar.SetProjectAppearance:
		for i := range st.Projects {
			if st.Projects[i].ID == ev.ProjectID {
				st.Projects[i].Icon, st.Projects[i].Color = ev.Icon, ev.Color
			}
		}
	case sidebar.MoveToGroup:
		for _, id := range ev.WorkspaceIDs {
			ws(id).ProjectID = ev.GroupID
		}
	case sidebar.NewGroup:
		id := fmt.Sprintf("g-%d", len(st.Projects)+1)
		st.Projects = append(st.Projects, model.Project{ID: id, Name: "New group", Kind: model.ProjectGroup})
		for _, w := range ev.WorkspaceIDs {
			ws(w).ProjectID = id
		}
	case sidebar.RenameGroup:
		for i := range st.Projects {
			if st.Projects[i].ID == ev.GroupID {
				st.Projects[i].Name = ev.Name
			}
		}
	case sidebar.Ungroup:
		st.Projects = slices.DeleteFunc(st.Projects, func(p model.Project) bool { return p.ID == ev.GroupID })
		for i := range st.Workspaces {
			if st.Workspaces[i].ProjectID == ev.GroupID {
				st.Workspaces[i].ProjectID = ""
			}
		}
	case sidebar.DeleteWorkspace:
		for i := range st.Workspaces {
			if st.Workspaces[i].ID == ev.WorkspaceID {
				st.Workspaces = append(st.Workspaces[:i], st.Workspaces[i+1:]...)
				break
			}
		}
	}
}

func fakeState(now time.Time) model.State {
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	st := model.State{
		Projects: []model.Project{
			{ID: "p-api", Name: "acme-api", Root: "/home/me/src/acme-api", Kind: model.ProjectGit, Color: "sky", Icon: "server"},
			{ID: "p-web", Name: "web-app-storefront-checkout", Root: "/home/me/src/web-app", Kind: model.ProjectGit, Color: "violet", Icon: "globe"},
			{ID: "p-notes", Name: "notes", Root: "/home/me/notes", Kind: model.ProjectFolder, Color: "amber"},
		},
		Stats: map[string]model.BranchStats{
			"ws-sidebar":  {Additions: 412, Deletions: 38, MergeStatus: model.MergeClean},
			"ws-term":     {Additions: 1290, Deletions: 211},
			"ws-daemon":   {Additions: 57, Deletions: 3},
			"ws-conflict": {Additions: 18, Deletions: 44, MergeStatus: model.MergeConflicts},
			"ws-plan":     {MergeStatus: model.MergeUpToDate},
			"ws-hotkeys":  {Additions: 6, Deletions: 6},
		},
		// One PR chip per state: open with each CI rollup and review, a
		// draft, merged and closed.
		PRs: map[string]model.PR{
			"ws-sidebar": {Number: 123, State: model.PROpen, Review: model.ReviewApproved, URL: "https://github.com/acme/api/pull/123",
				Checks: []model.Check{{Name: "build", State: model.CheckPass}, {Name: "test", State: model.CheckPass}}},
			"ws-term": {Number: 131, State: model.PROpen, Review: model.ReviewChanges, URL: "https://github.com/acme/api/pull/131",
				Checks: []model.Check{{Name: "build", State: model.CheckPass}, {Name: "test (ubuntu-latest)", State: model.CheckFail}, {Name: "lint", State: model.CheckPending}}},
			"ws-daemon": {Number: 9, State: model.PROpen, Draft: true, URL: "https://github.com/acme/api/pull/9",
				Checks: []model.Check{{Name: "build", State: model.CheckPending}}},
			"ws-pi":       {Number: 118, State: model.PRMerged, URL: "https://github.com/acme/api/pull/118"},
			"ws-hotkeys":  {Number: 77, State: model.PRClosed, URL: "https://github.com/acme/web/pull/77"},
			"ws-conflict": {Number: 140, State: model.PROpen, Review: model.ReviewRequired, Conflicts: true, URL: "https://github.com/acme/api/pull/140"},
		},
	}
	add := func(id, project, name, branch string, updated time.Duration, provider model.Provider, state model.AgentState) {
		st.Workspaces = append(st.Workspaces, model.Workspace{ID: id, ProjectID: project, Name: name, Branch: branch, Path: "/home/me/src/" + name, UpdatedAt: ago(updated)})
		if state != "" {
			detail := ""
			if provider == model.ProviderTerminal {
				detail = "go"
			}
			st.Activities = append(st.Activities, model.Activity{PaneID: id + "-pane", WorkspaceID: id, Provider: provider, State: state, Detail: detail, UpdatedAt: ago(updated)})
		}
	}
	add("ws-main", "", "~", "", 3*time.Hour, "", "")
	if home, err := os.UserHomeDir(); err == nil {
		st.Workspaces[0].Path = home
	}
	// A tab with three agents and a shell, one of them asking.
	add("ws-trio", "", "Ship the billing API", "billing-api", 3*time.Minute, "", "")
	st.Workspaces[len(st.Workspaces)-1].Tabs = []model.Tab{{Layout: &pwlayout.Node{Children: []*pwlayout.Node{
		{Pane: "trio-1"}, {Pane: "trio-2"}, {Pane: "trio-sh"}, {Pane: "trio-3"}}}}}
	st.Stats["ws-trio"] = model.BranchStats{Additions: 96, Deletions: 12}
	st.Panes = append(st.Panes,
		model.Pane{ID: "trio-1", WorkspaceID: "ws-trio", Provider: model.ProviderClaude, Title: "Write the migration"},
		model.Pane{ID: "trio-2", WorkspaceID: "ws-trio", Provider: model.ProviderCodex, Title: "Review the handlers"},
		model.Pane{ID: "trio-sh", WorkspaceID: "ws-trio"},
		model.Pane{ID: "trio-3", WorkspaceID: "ws-trio", Provider: model.ProviderClaude, Prompt: "update the API docs"})
	st.Activities = append(st.Activities,
		model.Activity{PaneID: "trio-1", WorkspaceID: "ws-trio", Provider: model.ProviderClaude, State: model.StateWorking, UpdatedAt: ago(time.Minute)},
		model.Activity{PaneID: "trio-2", WorkspaceID: "ws-trio", Provider: model.ProviderCodex, State: model.StatePendingApproval,
			Detail: "Bash: go test ./internal/billing/...", UpdatedAt: ago(3 * time.Minute)},
		model.Activity{PaneID: "trio-3", WorkspaceID: "ws-trio", Provider: model.ProviderClaude, State: model.StateCompleted, UpdatedAt: ago(8 * time.Minute)})
	add("ws-sidebar", "p-api", "Add rate limiting to the public API", "rate-limit", 40*time.Second, model.ProviderClaude, model.StateWorking)
	add("ws-term", "p-api", "Paginate the orders endpoint", "orders-pagination", 2*time.Minute, model.ProviderCodex, model.StateConnecting)
	add("ws-daemon", "p-api", "Rotate the signing keys", "key-rotation", 5*time.Minute, model.ProviderClaude, model.StatePendingApproval)
	add("ws-conflict", "p-api", "Retry failed webhooks", "webhook-retry", 9*time.Minute, model.ProviderClaude, model.StateError)
	add("ws-pi", "p-api", "Port the parser", "parser-port", 7*time.Minute, model.ProviderPi, model.StateCompleted)
	add("ws-plan", "p-api", "Split the billing service", "billing-split", 22*time.Minute, model.ProviderClaude, model.StatePlanReady)
	add("ws-hotkeys", "p-web", "Fix the checkout race", "fix/checkout-race", 12*time.Minute, model.ProviderClaude, model.StateAwaitingInput)
	add("ws-release", "p-web", "Release 2.3", "release/2.3", 26*time.Hour, model.ProviderCodex, model.StateCompleted)
	add("ws-ci", "", "CI watch", "", 30*time.Second, model.ProviderTerminal, model.StateTerminalRunning)
	add("ws-notes", "p-notes", "notes", "", 4*24*time.Hour, "", "")
	add("ws-old", "p-web", "Old spike", "spike/new-router", 9*24*time.Hour, "", "")
	st.Workspaces[len(st.Workspaces)-1].Detached = true
	for i := range st.Workspaces {
		if st.Workspaces[i].ProjectID == "p-notes" {
			st.Workspaces[i].Path = "/home/me/notes"
		}
	}
	return st
}
