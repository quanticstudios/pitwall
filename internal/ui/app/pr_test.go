package app

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// TestMergeFlow: Merge PR opens the dialog; with a failed check the first
// confirm only arms it and sends nothing, the second opens a tab running
// gh pr merge by [git] merge_method. Archive opens the delete dialog with
// the branch ticked.
func TestMergeFlow(t *testing.T) {
	old := ghInstalled
	t.Cleanup(func() { ghInstalled = old })
	ghInstalled = func() bool { return true }
	b := NewFakeBackend()
	b.st.PRs = map[string]model.PR{"w4": {Number: 12, State: model.PROpen, URL: "https://example.invalid/pull/12",
		Checks: []model.Check{{Name: "test", State: model.CheckFail}}}}
	u := &ui{b: b}
	u.cfg.MergeMethod = "rebase"
	st := b.State()
	u.nav.sync(&st)
	before := len(b.Sent())

	u.sidebarEvent(&st, sidebar.MergePR{WorkspaceID: "w4"})
	if u.modal.kind != modalMerge || u.modal.ws != "w4" {
		t.Fatalf("Merge PR did not open the dialog: %v", u.modal.kind)
	}
	u.confirmModal(&st)
	if !u.modal.armed || u.modal.kind != modalMerge || len(b.Sent()) != before {
		t.Fatalf("first confirm with a failed check: armed %v, sent %v", u.modal.armed, b.Sent()[before:])
	}
	u.confirmModal(&st)
	ws := findWorkspace(&st, "w4")
	want := proto.NewSession{Name: "Merge #12", Cwd: st.LivePath(*ws), GroupID: ws.ProjectID, SessionID: ws.SessionID, Cmd: mergeCmd(st.LivePath(*ws), 12, "rebase")}
	if sent := b.Sent()[before:]; u.modal.kind != modalNone || len(sent) != 1 || !reflect.DeepEqual(sent[0], want) {
		t.Fatalf("second confirm sent %+v, want %+v", sent, want)
	}

	b.st.PRs["w4"] = model.PR{Number: 12, State: model.PROpen, URL: "u"}
	st = b.State()
	u.prAction(&st, "w4", "merge_pr")
	u.confirmModal(&st)
	if u.modal.kind != modalNone || len(b.Sent()) != before+2 {
		t.Fatalf("passing checks merge on the first confirm: %v", b.Sent()[before:])
	}

	b.st.PRs["w4"] = model.PR{Number: 12, State: model.PRMerged}
	st = b.State()
	u.prAction(&st, "w4", "merge_pr")
	if u.modal.kind != modalNone {
		t.Fatal("a merged PR opened the merge dialog")
	}
	u.nav.workspace = "w4"
	if got := u.nav.reviewBlocked(&st, "merge_pr"); got != "merged" {
		t.Errorf("palette reason %q", got)
	}
	u.sidebarEvent(&st, sidebar.Archive{WorkspaceID: "w4"})
	if u.modal.kind != modalDelete || !u.modal.archive || !u.modal.removeBranch {
		t.Fatalf("Archive: kind %v, archive %v, branch %v", u.modal.kind, u.modal.archive, u.modal.removeBranch)
	}
	u.confirmModal(&st)
	if last := b.Sent()[len(b.Sent())-1]; last != (proto.DeleteWorkspace{WorkspaceID: "w4", RemoveBranch: true}) {
		t.Fatalf("Archive sent %+v", last)
	}
}

// TestPRScripts runs the merge and re-run commands with a stub gh: each
// prints what it runs and passes gh the PR, the method, and the latest
// failed run's id.
func TestPRScripts(t *testing.T) {
	root := t.TempDir()
	bin, log := filepath.Join(root, "bin"), filepath.Join(root, "gh.log")
	write(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho \"$@\" >> '"+log+"'\n[ \"$1 $2\" = 'run list' ] && echo 4242\nexit 0\n")
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := "PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")
	if out := run(t, mergeCmd(root, 12, "squash"), path); !strings.Contains(out, "$ gh pr merge 12 --squash") {
		t.Errorf("merge printed %q", out)
	}
	if out := run(t, rerunCmd(root, "feature"), path); !strings.Contains(out, "$ gh run rerun 4242 --failed") {
		t.Errorf("rerun printed %q", out)
	}
	calls, _ := os.ReadFile(log)
	if want := "pr merge 12 --squash\nrun list --branch feature --status failure --limit 1 --json databaseId --jq .[0].databaseId\nrun rerun 4242 --failed\n"; string(calls) != want {
		t.Errorf("gh called with\n%s\nwant\n%s", calls, want)
	}
}
