package app

import (
	"image"
	"strings"

	gl "gioui.org/layout"
	"gioui.org/op"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Add to chat: a terminal pane's selection goes into an agent chat's
// composer as a terminal excerpt chip, T3 Code's terminal context. Its
// key is add_to_chat and the pane's right-click menu has it.

// chatAdds are terminal excerpts waiting for the command that printed
// them, and the chat the window last showed, for a tab with none.
type chatAdds struct {
	waiting []chatAdd
	last    string // the chat whose composer last had the keys
	id      uint64 // the last proto.Commands id asked
}

// chatAdd is one excerpt whose command the daemon was asked for.
type chatAdd struct {
	chat, pane string
	n          int    // its number in the chat's draft
	line       uint64 // where the selection starts
	id         uint64
}

// chatAddBase keeps Add to chat's proto.Commands ids apart from the
// command browser's, which count up from 1.
const chatAddBase = 1 << 62

// chatFor is the chat a selection in pane goes to: the pane's own when it
// shows as a chat, else the first pane of its tab that does, else the
// chat whose composer last had the keys; "" for none.
func (u *ui) chatFor(st *model.State, pane string) string {
	p := findPane(st, pane)
	if p == nil {
		return ""
	}
	ws := st.Workspace(p.WorkspaceID)
	if ws != nil && model.PaneChat(*p) && u.paneChatShown(st, ws, p.ID) {
		return p.ID
	}
	for i := range st.Panes {
		q := &st.Panes[i]
		if q.WorkspaceID == p.WorkspaceID && q.ID != p.ID && ws != nil && model.PaneChat(*q) && u.paneChatShown(st, ws, q.ID) {
			return q.ID
		}
	}
	c := u.adds.last
	if c == "" || findChat(st, c) == nil {
		return ""
	}
	if q := findPane(st, c); q != nil && !u.paneChatShown(st, st.Workspace(q.WorkspaceID), c) {
		return "" // a pane chat flipped to its TUI since: its composer is out of sight
	}
	return c
}

// toChat puts text, selection sel of pane, in its chat's composer, and
// asks the daemon what command printed it to name the chip; it says
// where the text went under the pane at r.
func (u *ui) toChat(gtx gl.Context, st *model.State, pane string, sel vt.Selection, text string, r image.Rectangle) {
	chat := u.chatFor(st, pane)
	if chat == "" {
		u.showError(gtx, "No agent chat to add this to: start an agent in this tab", r)
		return
	}
	label := "" // a shell's comes from its prompt marks; an agent's TUI has none
	if p := findPane(st, pane); p != nil && !model.IsAgent(p.Provider) {
		label = strings.Join(p.Cmd, " ")
	}
	cmp := u.composer(st, chat)
	n := cmp.AddTerminal(text, label)
	if n == 0 {
		u.showNotice(gtx, "Added to the chat as a text file", r)
	} else {
		u.showNotice(gtx, "Added to the chat", r)
	}
	gtx.Execute(op.InvalidateCmd{})
	if n == 0 || label != "" || u.link().Level < proto.CommandsLevel {
		return
	}
	u.adds.id++
	id := chatAddBase + u.adds.id
	a, _ := sel.Ordered()
	if u.send(proto.Commands{Pane: pane, ID: id}) {
		u.adds.waiting = append(u.adds.waiting, chatAdd{chat: chat, pane: pane, n: n, line: a.Line, id: id})
	}
}

// takeCommands names the excerpts the command blocks in r cover, and
// reports whether r answered Add to chat.
func (u *ui) takeCommands(st *model.State, r proto.CommandResult) bool {
	for i, a := range u.adds.waiting {
		if a.id != r.ID || a.pane != r.Pane {
			continue
		}
		u.adds.waiting = append(u.adds.waiting[:i], u.adds.waiting[i+1:]...)
		if c := commandAt(r.Blocks, a.line); c != "" && findChat(st, a.chat) != nil {
			u.composer(st, a.chat).LabelTerminal(a.n, c)
		}
		return true
	}
	return false
}

// commandAt is the command whose block holds line: the last one whose
// prompt is at or above it, "" when none is.
func commandAt(blocks []vt.CommandBlock, line uint64) string {
	c := ""
	for _, b := range blocks {
		if b.Prompt.Line > line {
			break
		}
		c = strings.TrimSpace(firstLineOf(b.Command))
	}
	return c
}

func firstLineOf(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

// canAddToChat reports whether the pane menu offers Add to chat for pane:
// it has a selection and a chat to take it.
func (u *ui) canAddToChat(st *model.State, pane string) bool {
	p := u.panes[pane]
	return p != nil && p.view.Selected() && u.chatFor(st, pane) != ""
}
