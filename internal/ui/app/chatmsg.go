package app

import (
	"slices"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/proto"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/panel"
)

// ChatAsker is optionally implemented by a Backend: the answers to the
// window's ChatAside questions about chat id that came since the last
// call.
type ChatAsker interface {
	ChatAsides(id string) []proto.ChatAsideResult
}

// RewindChecker is optionally implemented by a Backend: the daemon's
// answers to the window's ChatRewindCheck about chat id that came since
// the last call.
type RewindChecker interface {
	RewindChecks(id string) []proto.ChatRewindFiles
}

// ChatFinder is optionally implemented by a Backend: the newest answer
// to the window's ChatFind for chat id that came since the last call.
type ChatFinder interface {
	ChatFound(id string) (proto.ChatFindResult, bool)
}

// chatTools sets what chat id's transcript t offers on its prompts, as
// its daemon and agent take them, hands it the side questions answered
// and the rewinds checked, and what the daemon found, and reports whether
// the chat takes side questions. Below ChatFindLevel, t finds in what the
// window holds. A Claude Code pane chat rewinds through its picker and
// does not fork; a chat tab does both. A Codex or Antigravity chat tab
// rewinds from RewindFilesLevel, through a fork of its session or a
// recap; their panes have no picker pitwall drives.
func (u *ui) chatTools(st *model.State, id string, t *panel.Transcript) bool {
	c := findChat(st, id)
	claude := c != nil && c.Provider == model.ProviderClaude
	pane := paneChat(st, id)
	level := u.link().Level
	t.TurnRewind = !pane
	t.CanRewind = claude && (pane && level >= proto.PaneRewindLevel || !pane && level >= proto.ChatForkLevel) ||
		c != nil && !pane && level >= proto.RewindFilesLevel
	t.CanFork = claude && !pane && level >= proto.ChatForkLevel
	t.FilesRewind = t.CanRewind && level >= proto.RewindFilesLevel
	t.Find = nil
	if level >= proto.ChatFindLevel {
		t.Find = func(q string) { u.send(proto.ChatFind{Chat: id, Query: q}) }
	}
	if f, ok := u.b.(ChatFinder); ok {
		if r, ok := f.ChatFound(id); ok {
			t.Found(r.Query, r.Hits, r.More, r.Err)
		}
	}
	if a, ok := u.b.(ChatAsker); ok {
		for _, r := range a.ChatAsides(id) {
			t.Answered(r.ID, r.Answer, r.Err)
		}
	}
	if r, ok := u.b.(RewindChecker); ok {
		for _, c := range r.RewindChecks(id) {
			t.RewindChecked(c)
		}
	}
	return claude && level >= proto.ChatAsideLevel
}

// paneChat reports whether chat id is a pane's, not a chat tab's.
func paneChat(st *model.State, id string) bool {
	return !slices.ContainsFunc(st.Chats, func(c model.Chat) bool { return c.ID == id })
}

// asideOf is the side question composer message m asks with /btw, typed
// or picked as a skill; false for any other message.
func asideOf(m any) (string, bool) {
	s, ok := m.(proto.ChatSend)
	if !ok {
		return "", false
	}
	if s.Skill != nil && "/"+s.Skill.Name == panel.AsideCommand {
		return panel.AsideQuestion(panel.AsideCommand + " " + s.Text)
	}
	return panel.AsideQuestion(s.Text)
}

// chatActions acts on the prompts' hover buttons clicked on chat id's
// transcript, acts: Edit and Rewind put the prompt in composer cmp. A pane
// chat rewinds to the prompt itself; a chat tab, pane false, rewinds whole
// turns. It reports whether there were any.
func (u *ui) chatActions(id string, acts []panel.MessageAction, pane bool, cmp *chatin.Composer) bool {
	for _, a := range acts {
		switch a.Kind {
		case panel.ActEdit:
			cmp.SetText(a.Text)
		case panel.ActRewindCheck:
			u.send(proto.ChatRewindCheck{Chat: id, Turn: rewindTurn(a, pane)})
		case panel.ActRewind:
			turn := rewindTurn(a, pane)
			u.send(proto.ChatRewind{Chat: id, Turn: turn, KeepFiles: a.KeepFiles, Stash: a.Stash})
			cmp.AwaitRewind(turn, a.Text, a.Attachments) // a refused rewind leaves the box as it was
		case panel.ActFork:
			u.send(proto.ChatFork{Chat: id, Turn: a.Turn})
		}
	}
	return len(acts) > 0
}

// rewindTurn is how the daemon names the prompt action a rewinds to: its
// turn in a chat tab, the prompt itself in a pane chat.
func rewindTurn(a panel.MessageAction, pane bool) string {
	if pane {
		return a.Item
	}
	return a.Turn
}
