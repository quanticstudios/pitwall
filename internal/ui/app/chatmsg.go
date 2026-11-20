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

// chatTools sets what chat id's transcript t offers on its prompts, as
// its daemon and agent take them, hands it the side questions answered,
// and reports whether the chat takes side questions. A pane chat
// rewinds through Claude Code's picker and does not fork; a chat tab
// does both.
func (u *ui) chatTools(st *model.State, id string, t *panel.Transcript) bool {
	c := findChat(st, id)
	claude := c != nil && c.Provider == model.ProviderClaude
	pane := !slices.ContainsFunc(st.Chats, func(c model.Chat) bool { return c.ID == id })
	level := u.link().Level
	t.CanRewind = claude && (pane && level >= proto.PaneRewindLevel || !pane && level >= proto.ChatForkLevel)
	t.CanFork = claude && !pane && level >= proto.ChatForkLevel
	if a, ok := u.b.(ChatAsker); ok {
		for _, r := range a.ChatAsides(id) {
			t.Answered(r.ID, r.Answer, r.Err)
		}
	}
	return claude && level >= proto.ChatAsideLevel
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
// transcript t: Edit and Rewind put the prompt in composer cmp. It
// reports whether there were any.
func (u *ui) chatActions(id string, t *panel.Transcript, cmp *chatin.Composer) bool {
	acts := t.Actions()
	for _, a := range acts {
		switch a.Kind {
		case panel.ActEdit:
			cmp.SetText(a.Text)
		case panel.ActRewind:
			u.send(proto.ChatRewind{Chat: id, Turn: a.Turn, KeepFiles: true})
			cmp.SetText(a.Text)
		case panel.ActFork:
			u.send(proto.ChatFork{Chat: id, Turn: a.Turn})
		}
	}
	return len(acts) > 0
}
