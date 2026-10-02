package app

import (
	"fmt"
	"image"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	gl "gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

func TestModalPressTargets(t *testing.T) {
	for _, kind := range []modalKind{modalDelete, modalSettings, modalAddProject} {
		for _, tc := range []struct {
			name string
			at   f32.Point
			open bool
		}{
			{"body", f32.Pt(184, 400), true}, // left padding of the centered 448px dialog
			{"backdrop", f32.Pt(8, 8), false},
		} {
			t.Run(fmt.Sprintf("%d/%s", kind, tc.name), func(t *testing.T) {
				u := &ui{th: theme.Dark()}
				st := model.State{Workspaces: []model.Workspace{{ID: "w", Name: "Session"}}}
				u.modal.open(kind, "w")
				var r input.Router
				var ops op.Ops
				frame := func() {
					ops.Reset()
					gtx := gl.Context{Ops: &ops, Source: r.Source(),
						Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
						Constraints: gl.Exact(image.Pt(800, 800)), Now: time.Now()}
					u.layoutModal(gtx, &st)
					r.Frame(&ops)
				}
				frame()
				r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse,
					Buttons: pointer.ButtonPrimary, Position: tc.at})
				frame()
				if open := u.modal.kind != modalNone; open != tc.open {
					t.Fatalf("dialog open = %v, want %v", open, tc.open)
				}
			})
		}
	}
}

func TestSidebarEvents(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b}
	st := b.State()
	u.nav.sync(&st)

	u.sidebarEvent(&st, sidebar.ArchiveWorkspace{WorkspaceID: "w2"})
	u.sidebarEvent(&st, sidebar.ArchiveWorkspace{WorkspaceID: "w2"}) // twice still archives
	u.sidebarEvent(&st, sidebar.RestoreWorkspace{WorkspaceID: "w2"})
	u.sidebarEvent(&st, sidebar.SetProjectAppearance{ProjectID: "g1", Icon: "code", Color: "sky"})
	u.sidebarEvent(&st, sidebar.MoveToGroup{WorkspaceIDs: []string{"w1", "w2"}, GroupID: "g2"})
	u.sidebarEvent(&st, sidebar.MoveToGroup{WorkspaceIDs: []string{"w4"}})
	u.sidebarEvent(&st, sidebar.NewGroup{WorkspaceIDs: []string{"w2", "w3"}})
	u.sidebarEvent(&st, sidebar.RenameGroup{GroupID: "g1", Name: "bots"})
	u.sidebarEvent(&st, sidebar.Ungroup{GroupID: "g1"})
	u.sidebarEvent(&st, sidebar.NewSession{})
	u.sidebarEvent(&st, sidebar.NewSession{GroupID: "g2"})
	u.sidebarEvent(&st, sidebar.NewSession{GroupID: "g1"})
	u.sidebarEvent(&st, sidebar.NewWorktreeSession{GroupID: "g2"})
	u.sidebarEvent(&st, sidebar.DeleteWorkspace{WorkspaceID: "w3"})
	if u.modal.kind != modalDelete || u.modal.ws != "w3" {
		t.Fatalf("delete did not open the dialog: %+v", u.modal.kind)
	}
	u.modal.removeBranch = true
	u.confirmModal()
	want := []any{
		proto.ArchiveWorkspace{WorkspaceID: "w2", Archived: true},
		proto.ArchiveWorkspace{WorkspaceID: "w2", Archived: true},
		proto.ArchiveWorkspace{WorkspaceID: "w2", Archived: false},
		proto.SetProjectAppearance{ProjectID: "g1", Icon: "code", Color: "sky"},
		proto.SetSessionGroup{WorkspaceID: "w1", GroupID: "g2"},
		proto.SetSessionGroup{WorkspaceID: "w2", GroupID: "g2"},
		proto.SetSessionGroup{WorkspaceID: "w4"},
		proto.NewGroup{Name: "New group", WorkspaceIDs: []string{"w2", "w3"}},
		proto.RenameGroup{GroupID: "g1", Name: "bots"},
		proto.DeleteGroup{GroupID: "g1"},
		proto.NewSession{Cwd: fakeHome + "/Work/pitwall", FromPane: "a"}, // the open session, where its shell is now
		proto.NewSession{Cwd: fakeHome + "/Work/aide", GroupID: "g2"},    // the group's root
		proto.NewSession{GroupID: "g1"},                                  // no root, open session elsewhere
		proto.NewWorkspace{ProjectID: "g2"},
		proto.DeleteWorkspace{WorkspaceID: "w3", RemoveBranch: true},
	}
	if got := b.Sent(); !reflect.DeepEqual(got, want) {
		t.Fatalf("sent %+v\nwant %+v", got, want)
	}
	if u.modal.kind != modalNone {
		t.Error("confirm left the dialog open")
	}

	u.sidebarEvent(&st, sidebar.OpenSettings{})
	if u.modal.kind != modalSettings {
		t.Error("settings did not open")
	}
}

func TestCompletePath(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"alpha", "alps", "beta", ".hidden"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "afile"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for in, want := range map[string]string{
		dir + "/a":   dir + "/alp",   // common prefix of alpha and alps; files skipped
		dir + "/b":   dir + "/beta/", // single match completes with a slash
		dir + "/.h":  dir + "/.hidden/",
		dir + "/zzz": dir + "/zzz", // no match leaves the text alone
	} {
		if got, _ := completePath(in); got != want {
			t.Errorf("completePath(%q) = %q, want %q", in, got, want)
		}
	}
	if got := dirMatches(dir + "/"); !reflect.DeepEqual(got, []string{"alpha", "alps", "beta"}) {
		t.Errorf("hidden or file listed: %v", got)
	}

	home, _ := os.UserHomeDir()
	if got := expandHome("~/x"); got != home+"/x" {
		t.Errorf("expandHome = %q", got)
	}
	if got, err := resolveDir(dir + "/beta/"); err != nil || got != dir+"/beta" {
		t.Errorf("resolveDir = %q, %v", got, err)
	}
	if _, err := resolveDir(dir + "/afile"); err == nil {
		t.Error("a file resolved as a folder")
	}
}
