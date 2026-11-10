package app

import (
	"reflect"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/gitstat"
	"github.com/quanticstudios/pitwall/internal/model"
)

// TestBaseItems lists the default branch first, then the branch the
// worktree came from, the pull request's base and the other local
// branches, each once and never the tab's own; the filter and a pick
// follow.
func TestBaseItems(t *testing.T) {
	refs := gitstat.Refs{Default: "origin/main", Local: []string{"main", "feature", "stacked", "old"}, Remote: []string{"origin/main", "origin/feature"}}
	got := baseItems(refs, "feature", "feature", "stacked")
	want := []baseItem{
		{"", "origin/main", "default branch"},
		{"feature", "feature", "created from · pull request base"},
		{"old", "old", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("baseItems = %+v\nwant %+v", got, want)
	}
	if got := baseItems(refs, "", "main", "stacked")[0].note; got != "default branch · pull request base" {
		t.Errorf("a PR into the default branch: note %q", got)
	}
	if got := shownBases(want, " FEA"); !slices.Equal(got, []int{1}) {
		t.Errorf("shownBases = %v", got)
	}

	st := &model.State{Workspaces: []model.Workspace{{ID: "w"}}}
	u := &ui{}
	u.nav.reviewBase = map[string]string{"gone": "x"}
	u.review.ws, u.review.bases.open = "w", true
	u.pickBase(st, want[1])
	if u.nav.reviewBase["w"] != "feature" || u.review.picked() != "feature" || u.review.bases.open || len(u.nav.reviewBase) != 1 {
		t.Errorf("after a pick: bases %v, picked %q, open %v", u.nav.reviewBase, u.review.picked(), u.review.bases.open)
	}
	u.pickBase(st, want[0])
	if _, ok := u.nav.reviewBase["w"]; ok || u.review.picked() != "" {
		t.Errorf("the default branch kept a pick: %v", u.nav.reviewBase)
	}
}
