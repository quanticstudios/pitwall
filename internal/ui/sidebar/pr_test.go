package sidebar

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/theme"
)

// TestPRState maps a pull request to what the row, card and menu show:
// its words and color, and which actions are off and why.
func TestPRState(t *testing.T) {
	th := &theme.Theme{Green: theme.Hex("#00ff00"), Purple: theme.Hex("#ff00ff"), Red: theme.Hex("#ff0000"), Muted: theme.Hex("#888888")}
	failing := []model.Check{{Name: "build", State: model.CheckPass}, {Name: "test", State: model.CheckFail}}
	ws := model.Workspace{ID: "w", Branch: "feature"}
	for _, tc := range []struct {
		pr                 model.PR
		gh                 bool
		summary            string
		color              string
		open, merge, rerun string
	}{
		{model.PR{Number: 1, State: model.PROpen, URL: "u", Review: model.ReviewApproved}, true, "#1 Open · Approved", "#00ff00", "", "", "none failed"},
		{model.PR{Number: 2, State: model.PROpen, URL: "u", Review: model.ReviewChanges, Conflicts: true, Checks: failing}, true, "#2 Open · Changes requested · Conflicts", "#00ff00", "", "", ""},
		{model.PR{Number: 3, State: model.PROpen, Draft: true, URL: "u"}, true, "#3 Draft", "#888888", "", "draft", "none failed"},
		{model.PR{Number: 4, State: model.PRMerged, URL: "u", Review: model.ReviewApproved}, true, "#4 Merged", "#ff00ff", "", "merged", "none failed"},
		{model.PR{Number: 5, State: model.PRClosed, Checks: failing}, true, "#5 Closed", "#ff0000", "no link", "closed", ""},
		{model.PR{Number: 6, State: model.PROpen, URL: "u", Checks: failing}, false, "#6 Open", "#00ff00", "", "no gh CLI", "no gh CLI"},
	} {
		st := &model.State{PRs: map[string]model.PR{"w": tc.pr}}
		if s := PRSummary(tc.pr); s != tc.summary {
			t.Errorf("#%d: summary %q, want %q", tc.pr.Number, s, tc.summary)
		}
		if c := PRColor(th, tc.pr); c != theme.Hex(tc.color) {
			t.Errorf("#%d: color %v, want %s", tc.pr.Number, c, tc.color)
		}
		open, merge, rerun := PRBlocked(st, ws, tc.gh)
		if open != tc.open || merge != tc.merge || rerun != tc.rerun {
			t.Errorf("#%d: blocked %q %q %q, want %q %q %q", tc.pr.Number, open, merge, rerun, tc.open, tc.merge, tc.rerun)
		}
	}
	if open, merge, rerun := PRBlocked(&model.State{}, ws, true); open != "no PR" || merge != "no PR" || rerun != "no PR" {
		t.Errorf("no PR: %q %q %q", open, merge, rerun)
	}
}
