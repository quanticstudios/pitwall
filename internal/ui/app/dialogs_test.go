package app

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

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
		proto.NewSession{Cwd: fakeHome + "/src/web-app", GroupID: "g2"},    // the group's root
		proto.NewSession{GroupID: "g1"},                                  // no root, open session elsewhere
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
