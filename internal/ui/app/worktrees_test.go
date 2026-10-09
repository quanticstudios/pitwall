package app

import (
	"reflect"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
)

// The New worktree tab dialog sends what the user picked, refusing a
// branch the daemon did not list; the delete dialog forces a worktree
// with changes; Clean up worktrees lists the daemon's orphans and opens
// or deletes one, a dirty one only on a second click.
func TestWorktreeDialogs(t *testing.T) {
	b := NewFakeBackend()
	u := &ui{b: b}
	st := b.State()
	u.nav.sync(&st)
	sent := func() []any {
		t.Helper()
		all := b.Sent()
		return all[len(all)-1:]
	}

	pick := func(kind model.WorktreeKind, name, ref string) {
		t.Helper()
		u.openNewWorktree(&st, "g2")
		if u.modal.kind != modalNewWorktree || !reflect.DeepEqual(sent(), []any{proto.WorktreeQuery{ProjectID: "g2"}}) {
			t.Fatalf("dialog %v, sent %+v", u.modal.kind, sent())
		}
		u.setKind(kind)
		u.modal.wt.name.SetText(name)
		u.modal.wt.ref.SetText(ref)
		u.confirmModal(&st)
	}
	for _, c := range []struct {
		kind      model.WorktreeKind
		name, ref string
		want      any // nil: refused, the dialog stays
	}{
		{model.FromNew, "dark", "origin/feat/dark-mode", proto.NewWorkspace{ProjectID: "g2", Name: "dark", From: model.WorktreeFrom{Ref: "origin/feat/dark-mode"}}},
		{model.FromNew, "", "", proto.NewWorkspace{ProjectID: "g2"}},
		{model.FromNew, "x", "origin/nope", nil},
		{model.FromBranch, "", "release/1.4", proto.NewWorkspace{ProjectID: "g2", From: model.WorktreeFrom{Kind: model.FromBranch, Ref: "release/1.4"}}},
		{model.FromBranch, "", "origin/main", nil}, // a remote one
		{model.FromRemote, "", "origin/feat/saved-carts", proto.NewWorkspace{ProjectID: "g2", From: model.WorktreeFrom{Kind: model.FromRemote, Ref: "origin/feat/saved-carts"}}},
		{model.FromRemote, "", "", nil},
		{model.FromPR, "", "#42", proto.NewWorkspace{ProjectID: "g2", From: model.WorktreeFrom{Kind: model.FromPR, PR: 42}}},
		{model.FromPR, "", "abc", nil},
	} {
		pick(c.kind, c.name, c.ref)
		if c.want == nil {
			if u.modal.kind != modalNewWorktree || u.modal.wt.err == "" || !reflect.DeepEqual(sent(), []any{proto.WorktreeQuery{ProjectID: "g2"}}) {
				t.Errorf("%v %q: not refused, sent %+v", c.kind, c.ref, sent())
			}
			continue
		}
		if !reflect.DeepEqual(sent(), []any{c.want}) || u.modal.kind != modalNone {
			t.Errorf("%v %q: sent %+v, want %+v", c.kind, c.ref, sent(), c.want)
		}
	}
	u.openNewWorktree(&st, "g2")
	u.setKind(model.FromNew)
	if got := u.modal.wt.ref.Text(); got != "origin/main" {
		t.Errorf("new branch base %q, want the default branch", got)
	}
	r := b.Worktree()
	if got := refMatches(candidates(r, model.FromRemote), "FEAT"); !slices.Equal(got, []string{"origin/feat/dark-mode", "origin/feat/saved-carts"}) {
		t.Errorf("matches %q", got)
	}
	if got := refMatches(candidates(r, model.FromBranch), "main"); got != nil {
		t.Errorf("a picked branch still lists %q", got)
	}

	u.openDelete(&st, "w4")
	if !reflect.DeepEqual(sent(), []any{proto.WorktreeQuery{WorkspaceID: "w4"}}) || u.branchNote("w4") != "Not merged into the default branch: its commits are lost." {
		t.Fatalf("delete asked %+v", sent())
	}
	u.confirmModal(&st)
	if !reflect.DeepEqual(sent(), []any{proto.DeleteWorkspace{WorkspaceID: "w4", Force: true}}) {
		t.Errorf("delete of a worktree with changes sent %+v", sent())
	}
	n := len(b.Sent())
	u.openDelete(&st, "w3") // no worktree: nothing to ask
	if len(b.Sent()) != n || u.deleteForce("w3") {
		t.Errorf("asked about a plain tab: %+v", sent())
	}

	u.openCleanup()
	orphans := b.Worktree().Orphans
	if u.modal.kind != modalCleanup || len(orphans) != 2 || !orphans[1].Dirty {
		t.Fatalf("cleanup %v %+v", u.modal.kind, orphans)
	}
	n = len(b.Sent())
	u.deleteOrphan(orphans[1])
	if len(b.Sent()) != n {
		t.Fatalf("one click deleted a worktree with changes: %+v", sent())
	}
	u.deleteOrphan(orphans[1])
	if all := b.Sent(); len(all) != n+2 || all[n] != (proto.DeleteWorktree{Root: orphans[1].Root, Path: orphans[1].Path, Force: true}) {
		t.Fatalf("second click sent %+v", all[n:])
	}
	if got := b.Worktree().Orphans; len(got) != 1 || got[0] != orphans[0] {
		t.Errorf("list after delete %+v", got)
	}
	u.openOrphan(&st, orphans[0])
	if !reflect.DeepEqual(sent(), []any{proto.NewSession{Cwd: orphans[0].Path, GroupID: "g2", SessionID: "s1"}}) || u.modal.kind != modalNone {
		t.Errorf("open sent %+v", sent())
	}
}
