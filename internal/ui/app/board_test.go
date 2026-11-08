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
// over the board, allow_prompt answers nothing: not the board's focused
// card, and not the approval of the tab the board was opened over.
func TestBoardKeysOnlyWhileShown(t *testing.T) {
	for _, cover := range []string{"settings", "review"} {
		t.Run(cover, func(t *testing.T) {
			u, keys := keyWindow(t, conventional)
			st := u.b.State()
			u.nav.selectWorkspace(&st, "w4", "g") // its Claude waits for approval behind the board
			cs := key.ModCtrl | key.ModShift
			keys(press("M", cs), press(key.NameDownArrow, 0))
			time.Sleep(board.Arming + 50*time.Millisecond)
			if cover == "settings" {
				u.openSettings()
			} else {
				u.review.on, u.review.ws = true, u.nav.workspace
			}
			keys(press("Y", cs))
			for _, m := range u.b.(*FakeBackend).Sent() {
				if a, ok := m.(proto.Answer); ok {
					t.Fatalf("Ctrl+Shift+Y under the %s sent %+v", cover, a)
				}
			}
		})
	}
}

// TestBoardHidesPanesFromSeen: while the board shows, the focused pane
// does not count as seen, so its activity stays unseen and its bell rings;
// closing the board makes it seen again.
func TestBoardHidesPanesFromSeen(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	u.winFocused = true
	cs := key.ModCtrl | key.ModShift
	lastSee := func() (string, bool) {
		var out *proto.SeePane
		for _, m := range u.b.(*FakeBackend).Sent() {
			if s, ok := m.(proto.SeePane); ok {
				out = &s
			}
		}
		if out == nil {
			return "", false
		}
		return out.Pane, true
	}
	keys(press("Q", 0)) // a frame with the window focused
	pane, _ := lastSee()
	if pane == "" || pane != u.nav.focused() {
		t.Fatalf("panes shown: last SeePane %q, focused %q", pane, u.nav.focused())
	}
	keys(press("M", cs))
	if pane, ok := lastSee(); !ok || pane != "" {
		t.Errorf("board shown: last SeePane %q, want none", pane)
	}
	keys(press(key.NameEscape, 0))
	if pane, _ := lastSee(); pane != u.nav.focused() {
		t.Errorf("board closed: last SeePane %q, want %q", pane, u.nav.focused())
	}
}

// TestBoardAnswersSidebarHidden: a window that starts with the sidebar
// hidden still answers from the board.
func TestBoardAnswersSidebarHidden(t *testing.T) {
	u, keys := keyWindow(t, conventional)
	u.sidebar.Answers = false
	u.nav.sidebarHidden, u.sidebarShown = true, true // restored hidden: no slide
	cs := key.ModCtrl | key.ModShift
	keys(press("M", cs), press(key.NameDownArrow, 0))
	time.Sleep(board.Arming + 50*time.Millisecond)
	keys(press("Y", cs))
	for _, m := range u.b.(*FakeBackend).Sent() {
		if a, ok := m.(proto.Answer); ok && a.Allow {
			return
		}
	}
	t.Fatal("Ctrl+Shift+Y on a focused card with the sidebar hidden sent no Answer")
}
