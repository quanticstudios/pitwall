package antigravity

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
	if started != 1 || completed != 1 || model != "gemini-3.8-flash" || tr.effort != "low" {
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

// TestTranscriptTasks: agy keeps its to-do list in task.md in the
// conversation's folder. testdata/tasks.jsonl is agy 1.5.13 writing one;
// an edit then ticks the next item, and the card follows in place.
func TestTranscriptTasks(t *testing.T) {
	data, err := os.ReadFile("testdata/tasks.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTranscript()
	var plans []*session.ToolCall
	collect := func(es []session.Event) {
		for _, e := range es {
			if e.Tool != nil {
				if e.Tool.ToolName != "Plan" {
					t.Errorf("the task list's write showed as %s", e.Tool.ToolName)
				}
				plans = append(plans, e.Tool)
			}
		}
	}
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
		collect(tr.Line(line))
	}
	want := []session.Task{{Subject: "plan", Status: "completed"}, {Subject: "build", Status: "pending"}, {Subject: "test", Status: "pending"}}
	if len(plans) != 1 || !slices.Equal(plans[0].Tasks, want) {
		t.Fatalf("plans %+v", plans)
	}
	collect(tr.Line([]byte(`{"step_index":4,"type":"USER_INPUT","content":"<USER_REQUEST>\nnext\n</USER_REQUEST>"}`)))
	collect(tr.Line([]byte(`{"step_index":5,"type":"PLANNER_RESPONSE","status":"DONE","tool_calls":[{"name":"edit_file","args":{"Path":"/home/user/.gemini/antigravity-cli/brain/22222222-3333-4444-5555-666666666666/tasks.md","Target":"- [ ] build","Replacement":"- [/] build"}}]}`)))
	collect(tr.Line([]byte(`{"step_index":6,"type":"GENERIC","status":"DONE","content":"Made 1 replacement"}`)))
	if len(plans) != 2 || plans[1].Tasks[1].Status != "in_progress" || plans[1].CallID != "plan-step-4" {
		t.Fatalf("after the edit: %+v", plans)
	}
	// A task.md outside agy's conversation folders is a file of the project.
	if tr.taskList("write_file", map[string]json.RawMessage{"Path": json.RawMessage(`"/home/user/project/task.md"`), "Content": json.RawMessage(`"- [ ] x"`)}) != nil {
		t.Error("a project's task.md read as agy's list")
	}
}

// TestTranscriptTaskList: testdata/task_list.jsonl is agy 1.5.13 on a job
// it tracked in its own task.md unasked: written, then ticked through
// "[/]" to "[x]". The card shows each state, and the file's writes show
// as no tool rows of their own.
func TestTranscriptTaskList(t *testing.T) {
	data, err := os.ReadFile("testdata/task_list.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	tr := NewTranscript()
	var cards [][]session.Task
	tools := 0
	for line := range bytes.SplitSeq(bytes.TrimSpace(data), []byte("\n")) {
		for _, e := range tr.Line(line) {
			switch {
			case e.Tool != nil && e.Tool.ToolName == "Plan":
				cards = append(cards, e.Tool.Tasks)
			case e.Tool != nil:
				tools++
				if strings.Contains(string(e.Tool.Input), "task.md") {
					t.Errorf("task.md showed as a %s row", e.Tool.ToolName)
				}
			}
		}
	}
	if len(cards) < 2 || len(cards[0]) != 4 || cards[0][0].Status != "pending" {
		t.Fatalf("cards %+v", cards)
	}
	sawProgress := false
	for _, c := range cards {
		sawProgress = sawProgress || slices.ContainsFunc(c, func(t session.Task) bool { return t.Status == "in_progress" })
	}
	last := cards[len(cards)-1]
	if !sawProgress || slices.ContainsFunc(last, func(t session.Task) bool { return t.Status != "completed" }) || tools == 0 {
		t.Errorf("progress %v, last card %+v, %d other tool rows", sawProgress, last, tools)
	}
}

// TestTranscriptSubagents replays real parallel invoke_subagent transcripts:
// each call becomes an Agent card tied to its child conversation id, the turn
// stays running across intermediate waiting replies until every subagent
// reports back, and subagent transcript lines fill SubagentCalls.
func TestTranscriptSubagents(t *testing.T) {
	mainData, err := os.ReadFile("testdata/subagent_main.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	child1Data, err := os.ReadFile("testdata/subagent_child1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	child2Data, err := os.ReadFile("testdata/subagent_child2.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	mainLines := bytes.Split(bytes.TrimSpace(mainData), []byte("\n"))
	tr := NewTranscript()

	// Feed up through step 4 (two subagents launched + waiting reply).
	for _, l := range mainLines[:5] {
		tr.Line(l)
	}
	if !tr.Running() {
		t.Fatal("turn ended on waiting PLANNER_RESPONSE while subagents still run")
	}
	subs := tr.Subagents()
	if len(subs) != 2 || !subs["step-1#0"] || !subs["step-1#1"] {
		t.Fatalf("Subagents while running = %v, want both running", subs)
	}
	mainPath := filepath.FromSlash("/home/user/.gemini/antigravity-cli/brain/11111111-2222-3333-4444-555555555555/.system_generated/logs/transcript_full.jsonl")
	paths := tr.SubagentPaths(mainPath)
	want1 := filepath.FromSlash("/home/user/.gemini/antigravity-cli/brain/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/.system_generated/logs/transcript_full.jsonl")
	want2 := filepath.FromSlash("/home/user/.gemini/antigravity-cli/brain/bbbbbbbb-cccc-dddd-eeee-ffffffffffff/.system_generated/logs/transcript_full.jsonl")
	if paths["step-1#0"] != want1 || paths["step-1#1"] != want2 {
		t.Fatalf("SubagentPaths = %v", paths)
	}

	// Feed child transcripts into their cards.
	var last1, last2 *session.ToolCall
	for _, l := range bytes.Split(bytes.TrimSpace(child1Data), []byte("\n")) {
		for _, e := range tr.Subagent("step-1#0", l) {
			last1 = e.Tool
		}
	}
	for _, l := range bytes.Split(bytes.TrimSpace(child2Data), []byte("\n")) {
		for _, e := range tr.Subagent("step-1#1", l) {
			last2 = e.Tool
		}
	}
	if last1 == nil || !slices.Equal(last1.SubagentTools, []string{"Read", "send_message"}) || len(last1.SubagentCalls) != 2 || last1.SubagentCalls[0].Status != "completed" {
		t.Fatalf("child1 card = %+v", last1)
	}
	if last2 == nil || !slices.Equal(last2.SubagentTools, []string{"Read", "send_message"}) {
		t.Fatalf("child2 card = %+v", last2)
	}

	// Child 1 replies (step 5) and main notes waiting for child 2 (step 6): turn stays running.
	var c1Done *session.ToolCall
	for _, e := range tr.Line(mainLines[5]) {
		if e.Tool != nil {
			c1Done = e.Tool
		}
	}
	if c1Done == nil || c1Done.Status != "completed" || !strings.Contains(c1Done.Output, "hello world") {
		t.Fatalf("child1 completion event = %+v", c1Done)
	}
	tr.Line(mainLines[6])
	if !tr.Running() {
		t.Fatal("turn ended after first subagent while second still runs")
	}
	if subs := tr.Subagents(); subs["step-1#0"] || !subs["step-1#1"] {
		t.Fatalf("Subagents mid-turn = %v", subs)
	}

	// Child 2 replies (step 7) and main finishes with BOTH_DONE (step 8).
	tr.Line(mainLines[7])
	es := tr.Line(mainLines[8])
	if tr.Running() {
		t.Fatal("turn still running after final reply with no running subagents")
	}
	if !slices.ContainsFunc(es, func(e session.Event) bool {
		return e.Type == session.EventTurnCompleted && e.State == session.TurnCompleted
	}) {
		t.Fatalf("final events missing TurnCompleted: %+v", es)
	}

	// Interrupting a turn with a running subagent marks its card failed.
	tr2 := NewTranscript()
	for _, l := range mainLines[:4] {
		tr2.Line(l)
	}
	endEs := tr2.End(session.TurnInterrupted)
	failed := 0
	for _, e := range endEs {
		if e.Tool != nil && e.Tool.ToolName == "Agent" && e.Tool.Status == "failed" {
			failed++
		}
	}
	if failed != 2 {
		t.Fatalf("End(TurnInterrupted) failed %d subagents, want 2", failed)
	}
}
