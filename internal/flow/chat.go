package flow

import (
	"path"
	"strings"

	managed "github.com/quanticstudios/pitwall/internal/session" // flow has a session type of its own
)

// Chat is the Feed of a managed session's snapshot, so the side panel
// shows a chat as it shows an agent pane: turns with their calls and
// reply, and the latest task list as the plan.
func Chat(s managed.Snapshot) Feed {
	f := Feed{Provider: s.Provider}
	at := map[string]int{} // turn id to its index in f.Turns
	for _, t := range s.Turns {
		at[t.ID] = len(f.Turns)
		turn := Turn{Prompt: cut(firstLine(t.RequestText), 200), Start: parseTime(t.StartedAt)}
		if t.CompletedAt != nil {
			turn.End = parseTime(*t.CompletedAt)
		}
		f.Turns = append(f.Turns, turn)
	}
	for _, it := range s.Content {
		i, ok := -1, false
		if it.TurnID != nil {
			i, ok = at[*it.TurnID]
		}
		switch {
		case it.Kind == managed.KindToolCall && it.Tool != nil:
			if it.Tool.Tasks != nil {
				f.Plan = make([]Step, 0, len(it.Tool.Tasks))
				for _, t := range it.Tool.Tasks {
					f.Plan = append(f.Plan, Step{Text: t.Subject, State: stepState(t.Status)})
				}
			}
			if ok {
				when := it.CreatedAt
				if when == "" {
					when = it.UpdatedAt
				}
				f.Turns[i].Calls = append(f.Turns[i].Calls, Call{Time: parseTime(when), Tool: it.Tool.ToolName, Arg: cut(ToolArg(*it.Tool), 120),
					Running: it.Tool.Status == "running", Failed: it.Tool.Status == "failed"})
			}
		case it.Kind == managed.KindAssistantText && ok:
			f.Turns[i].Reply = cut(it.Text, 600)
		case it.Kind == managed.KindUserPrompt && ok && f.Turns[i].Prompt == "":
			f.Turns[i].Prompt = cut(firstLine(it.Text), 200)
		}
	}
	if n := len(f.Turns); n > maxTurns {
		f.Turns = f.Turns[n-maxTurns:]
	}
	return f
}

// ToolArg is a short label for a chat's tool call: its command's first
// line, else what Call.Arg shows for its input, else its first file's base
// name.
func ToolArg(t managed.ToolCall) string {
	if t.Command != "" {
		return firstLine(t.Command)
	}
	if a := arg(t.ToolName, t.Input); a != "" {
		return a
	}
	if len(t.Files) > 0 {
		return path.Base(strings.ReplaceAll(t.Files[0].Path, `\`, "/"))
	}
	return ""
}
