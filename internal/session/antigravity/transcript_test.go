package antigravity

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// testdata/transcript_full.jsonl is agy 1.5.13's transcript of one turn,
// with paths and the conversation id replaced: it reads a file, edits it,
// writes another, runs a command that fails, then replies.
func TestTranscript(t *testing.T) {
	data, err := os.ReadFile("testdata/transcript_full.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTranscript()
	var es []session.Event
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
		es = append(es, tr.Line(line)...)
	}
	if tr.Running() {
		t.Error("the turn still runs after the reply that made no call")
	}
	tools := map[string]*session.ToolCall{}
	var prompt, reply, model string
	var started, completed int
	for _, e := range es {
		switch {
		case e.Type == session.EventTurnStarted:
			started++
		case e.Type == session.EventTurnCompleted:
			completed++
			if e.State != session.TurnCompleted {
				t.Errorf("turn ended %q", e.State)
			}
		case e.Type == session.EventSettings:
			model = e.Model
		case e.Kind == session.KindUserPrompt:
			prompt = e.Text
		case e.Kind == session.KindAssistantText:
			reply = e.Text
		case e.Kind == session.KindToolCall:
			tools[e.Tool.ToolName] = e.Tool
		}
	}
	if started != 1 || completed != 1 || model != "gemini-3.8-flash-low" {
		t.Errorf("turns %d started, %d completed; model %q", started, completed, model)
	}
	if !strings.HasPrefix(prompt, "Do these steps in order:") || strings.Contains(prompt, "USER_REQUEST") || strings.Contains(prompt, "local time") {
		t.Errorf("prompt %q; want the user's own text", prompt)
	}
	if !strings.HasPrefix(reply, "Read [notes.txt]") {
		t.Errorf("reply %q", reply)
	}
	if r := tools["Read"]; r == nil || !strings.Contains(string(r.Input), `"file_path":"/home/user/project/notes.txt"`) || !strings.Contains(r.Output, "1: line one") || strings.Contains(r.Output, "Created At") {
		t.Errorf("read %+v", r)
	}
	if e := tools["Edit"]; e == nil || !strings.Contains(string(e.Input), `"old_string":"line two"`) || !strings.Contains(string(e.Input), `"new_string":"line 2"`) || e.Status != "completed" {
		t.Errorf("edit %+v", e)
	}
	if w := tools["Write"]; w == nil || !strings.Contains(string(w.Input), `"content":"hi\n"`) || w.Output != "Created file." {
		t.Errorf("write %+v", w)
	}
	b := tools["Bash"]
	if b == nil || b.Command != "ls nonexistent-xyz" || b.ExitCode == nil || *b.ExitCode != 2 || b.Status != "failed" ||
		b.Output != "ls: cannot access 'nonexistent-xyz': No such file or directory" {
		t.Errorf("command %+v", b)
	}

	var marks []claude.Mark
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
		marks = append(marks, MarkOf(line))
	}
	if marks[0] != claude.MarkPrompt || marks[1] != claude.MarkItem || marks[len(marks)-1] != claude.MarkItem {
		t.Errorf("marks %v", marks)
	}
}

// TestTranscriptAsk: an approval agy's terminal report names opens a
// command request; a new turn's prompt withdraws it, and a turn cut off
// shows its waiting calls as failed.
func TestTranscriptAsk(t *testing.T) {
	tr := NewTranscript()
	tr.Line([]byte(`{"step_index":0,"type":"USER_INPUT","content":"<USER_REQUEST>\nclean up\n</USER_REQUEST>"}`))
	tr.Line([]byte(`{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","tool_calls":[{"name":"run_command","args":{"CommandLine":"rm -rf ./junk"}}]}`))
	es := tr.Ask("7", "Approve Bash: rm -rf ./junk")
	if len(es) != 1 || es[0].RequestType != session.RequestCommand || es[0].Request.Command != "rm -rf ./junk" {
		t.Fatalf("ask %+v", es)
	}
	if again := tr.Ask("7", "Approve Bash: rm -rf ./junk"); again != nil {
		t.Errorf("the same request opened twice: %+v", again)
	}
	es = tr.End(session.TurnInterrupted)
	if len(es) != 3 || es[0].Type != session.EventRequestResolved || es[1].Tool == nil || es[1].Tool.Status != "failed" || es[2].State != session.TurnInterrupted {
		t.Errorf("end %+v", es)
	}
	if es := tr.Ask("8", "Trust this folder?"); len(es) != 1 || es[0].RequestType != session.RequestUnknown {
		t.Errorf("unknown ask %+v", es)
	}
}
