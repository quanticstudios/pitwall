// Command pwdemo-sidebar shows the sidebar against a fake state that covers
// every agent state, for visual checks against aide.
package main

import (
	"log"
	"os"
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
			_, events := sb.Layout(gtx, th, &st, active)
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
	case sidebar.RenameWorkspace:
		ws(ev.WorkspaceID).Name = ev.Name
	case sidebar.ArchiveWorkspace:
		ws(ev.WorkspaceID).Archived = true
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
			{ID: "p-pitwall", Name: "pitwall", Root: "/home/me/Work/pitwall", Kind: model.ProjectGit, Color: "sky"},
			{ID: "p-aide", Name: "aide", Root: "/home/me/src/web-app", Kind: model.ProjectGit, Color: "violet"},
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
		st.Workspaces = append(st.Workspaces, model.Workspace{ID: id, ProjectID: project, Name: name, Branch: branch, UpdatedAt: ago(updated)})
		if state != "" {
			st.Activities = append(st.Activities, model.Activity{PaneID: id + "-pane", WorkspaceID: id, Provider: provider, State: state, UpdatedAt: ago(updated)})
		}
	}
	add("ws-main", "p-pitwall", "main", "main", 3*time.Hour, "", "")
	add("ws-sidebar", "p-pitwall", "Theme and sidebar", "track/sidebar", 40*time.Second, model.ProviderClaude, model.StateWorking)
	add("ws-term", "p-pitwall", "Terminal renderer", "track/term", 2*time.Minute, model.ProviderCodex, model.StateConnecting)
	add("ws-daemon", "p-pitwall", "Daemon socket server", "track/daemon", 5*time.Minute, model.ProviderClaude, model.StatePendingApproval)
	add("ws-conflict", "p-pitwall", "Store resume commands", "track/store", 9*time.Minute, model.ProviderClaude, model.StateError)
	add("ws-plan", "p-pitwall", "Split tree ops", "track/layout", 22*time.Minute, model.ProviderClaude, model.StatePlanReady)
	add("ws-hotkeys", "p-aide", "Alt navigation hotkeys", "fix/alt-nav", 12*time.Minute, model.ProviderClaude, model.StateAwaitingInput)
	add("ws-release", "p-aide", "Release 1.4", "release/1.4", 26*time.Hour, model.ProviderCodex, model.StateCompleted)
	add("ws-ci", "p-aide", "CI watch", "main", 30*time.Second, model.ProviderTerminal, model.StateTerminalRunning)
	add("ws-notes", "p-notes", "notes", "", 4*24*time.Hour, "", "")
	add("ws-old", "p-aide", "Old spike", "spike/electron-41", 9*24*time.Hour, "", "")
	st.Workspaces[len(st.Workspaces)-1].Archived = true
	for i := range st.Workspaces {
		if st.Workspaces[i].ProjectID == "p-notes" {
			st.Workspaces[i].Path = "/home/me/notes"
		}
	}
	return st
}
