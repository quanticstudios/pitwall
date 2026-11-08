package app

import (
	"time"

	"gioui.org/io/key"
	gl "gioui.org/layout"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// toggleBoard opens the board in place of the panes, or closes it.
func (u *ui) toggleBoard() {
	if u.boardOn {
		u.boardOn = false
		return
	}
	u.settings.Hide()
	u.review.hide()
	u.boardOn, u.boardWS = true, u.nav.workspace
	u.sidebar.BoardFocus()
}

// layoutBoard draws the board, or the panes again once another tab is
// shown (from the keyboard, say).
func (u *ui) layoutBoard(gtx gl.Context, st *model.State) {
	if u.nav.workspace != u.boardWS {
		u.boardOn = false
		u.layoutPanes(gtx, st)
		return
	}
	o := sidebar.BoardOptions{Hosts: u.boardHosts(), GuardDeny: u.cfg.GuardDeny, AllowKey: u.chordName("allow_prompt"), DenyKey: u.chordName("deny_prompt")}
	for _, ev := range u.sidebar.Board(gtx, u.th, st, o) {
		u.sidebarPointer(st, ev)
	}
}

// boardShown reports whether the board is what the pane area shows: on,
// and not under the settings page or the review view.
func (u *ui) boardShown() bool { return u.boardOn && !u.settings.Shown() && !u.review.on }

// boardKey sends allow_prompt and deny_prompt to the board's focused card
// while the board shows, and never to a pane behind it.
func (u *ui) boardKey(st *model.State, e key.Event, now time.Time) bool {
	if !u.boardShown() {
		return false
	}
	act := u.nav.bind().Action(e)
	if act != "allow_prompt" && act != "deny_prompt" {
		return false
	}
	if e.State == key.Press {
		if ev, ok := u.sidebar.BoardAnswer(act == "allow_prompt", now); ok {
			u.sidebarEvent(st, ev)
		}
	}
	return true
}

// boardHosts is each session's machine by session id, for a window that
// shows more than one; nil otherwise.
func (u *ui) boardHosts() map[string]string {
	h, ok := u.b.(Hoster)
	if !ok {
		return nil
	}
	var out map[string]string
	for _, hi := range h.Hosts() {
		for _, s := range hi.Sessions {
			if hi.Name != "" {
				if out == nil {
					out = map[string]string{}
				}
				out[s] = hi.Name
			}
		}
	}
	return out
}

// chordName is action's first key as the settings write it, "" when it
// has none.
func (u *ui) chordName(action string) string {
	if cs := u.nav.bind().Global[action]; len(cs) > 0 {
		return cs[0].String()
	}
	return ""
}
