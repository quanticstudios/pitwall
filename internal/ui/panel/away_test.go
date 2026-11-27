package panel

import (
	"testing"
	"time"

	"gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
)

// TestAwayShown: the card shows until dismissed, a newer return's card
// shows again, and a recap Claude Code wrote since the user left takes
// its place.
func TestAwayShown(t *testing.T) {
	left := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	s := &session.Snapshot{Content: []session.Item{{ID: "a", Kind: session.KindAssistantText, Text: "hi", CreatedAt: session.Stamp(left.Add(time.Minute))}}}
	tr := &Transcript{Away: &model.Away{Chat: "c", Since: left, At: left.Add(time.Hour), Item: "a"}}
	if tr.awayShown(s) == nil {
		t.Fatal("no card")
	}
	if tr.AwayDismissed() {
		t.Fatal("dismissed before anything")
	}
	tr.hideAway()
	if tr.awayShown(s) != nil || !tr.AwayDismissed() || tr.AwayDismissed() {
		t.Fatal("a dismissed card shows, or the dismissal was not said once")
	}
	tr.Away = &model.Away{Chat: "c", Since: left, At: left.Add(2 * time.Hour), Item: "a"}
	if tr.awayShown(s) == nil {
		t.Fatal("the next return's card is hidden")
	}
	s.Content = append(s.Content, session.Item{ID: "r", Kind: session.KindSystem, Phase: session.PhaseRecap, Text: "Recap", CreatedAt: session.Stamp(left.Add(90 * time.Minute))})
	if tr.awayShown(s) != nil {
		t.Fatal("two cards with Claude's recap")
	}
}

// TestAwayTall: the card above the composer shows only when the first
// missed row starts above the log's view with the list at its end, or is
// not loaded yet; otherwise the marker in the log says it all.
func TestAwayTall(t *testing.T) {
	for _, tc := range []struct {
		first int
		pos   layout.Position
		want  bool
	}{
		{5, layout.Position{First: 2}, false},            // in view, below its top
		{5, layout.Position{First: 5}, false},            // the view's top row, whole
		{5, layout.Position{First: 5, Offset: 30}, true}, // its top cut off
		{5, layout.Position{First: 9}, true},             // above the view
		{-1, layout.Position{First: 0}, true},            // older than what is loaded
	} {
		if got := awayTall(tc.first, tc.pos); got != tc.want {
			t.Errorf("awayTall(%d, %+v) = %v, want %v", tc.first, tc.pos, got, tc.want)
		}
	}
}
