package app

import (
	"testing"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/board"
)

// TestBoardKeys: toggle_board opens the board; allow_prompt with no card
// focused answers nothing, not even the pane behind the board; once a card
// has the focus and its ask has been up for board.Arming, it answers that
// card's prompt; Esc goes back to the panes.
func TestBoardKeys(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	cs := key.ModCtrl | key.ModShift
	answers := func() []proto.Answer {
		var out []proto.Answer
		for _, m := range u.b.(*FakeBackend).Sent() {
			if a, ok := m.(proto.Answer); ok {
				out = append(out, a)
			}
		}
		return out
	}
	st := u.b.State()
	u.nav.selectWorkspace(&st, "w4", "g") // its Claude waits for approval behind the board
	keys(press("M", cs))
	if !u.boardOn {
		t.Fatal("Ctrl+Shift+M left the board closed")
	}
	keys(press("Y", cs))
	if got := answers(); len(got) != 0 {
		t.Fatalf("Ctrl+Shift+Y with no card focused sent %+v", got)
	}
	keys(press(key.NameDownArrow, 0))
	time.Sleep(board.Arming + 50*time.Millisecond)
	keys(press("Y", cs))
	got := answers()
	st = u.b.State()
	if len(got) != 1 || !got[0].Allow {
		t.Fatalf("Ctrl+Shift+Y on a focused card sent %+v", got)
	}
	for _, a := range st.Activities {
		if a.PaneID == got[0].Pane && (a.State != model.StatePendingApproval || a.UpdatedAt.UnixNano() != got[0].At) {
			t.Fatalf("answered %+v, the pane is %v at %v", got[0], a.State, a.UpdatedAt.UnixNano())
		}
	}
	keys(press(key.NameEscape, 0))
	if u.boardOn {
		t.Fatal("Esc left the board open")
	}
}

// TestBoardKeysOnlyWhileShown: with the settings page or the review view
// over the board, allow_prompt does not answer the board's focused card.
func TestBoardKeysOnlyWhileShown(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	cs := key.ModCtrl | key.ModShift
	keys(press("M", cs), press(key.NameDownArrow, 0))
	time.Sleep(board.Arming + 50*time.Millisecond)
	keys() // a frame after the arming time
	u.toggleSettings()
	keys(press("Y", cs))
	for _, m := range u.b.(*FakeBackend).Sent() {
		if a, ok := m.(proto.Answer); ok {
			t.Fatalf("Ctrl+Shift+Y under the settings page sent %+v", a)
		}
	}
}
