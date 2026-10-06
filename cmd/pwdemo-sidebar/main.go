// Command pwdemo-sidebar shows the sidebar against a fake state that covers
// every agent state, for visual checks against aide.
package main

import (
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

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func main() {
	go func() {
		w := new(app.Window)
		w.Option(app.Title("pwdemo-sidebar"), app.Size(unit.Dp(900), unit.Dp(860)))
		if err := run(w); err != nil {
			log.Fatal(err)
		}
		os.Exit(0)
	}()
	app.Main()
}

func run(w *app.Window) error {
	th := theme.Dark()
	st := fakeState(time.Now())
	active := "ws-sidebar"
	var sb sidebar.Sidebar
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
	add("ws-main", "", "home", "", 3*time.Hour, "", "")
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
