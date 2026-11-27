// Package antigravity reads the transcript Google's Antigravity CLI (agy)
// writes for a conversation, for a chat that mirrors agy's TUI in a pane.
package antigravity

import (
	"cmp"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/session/claude"
)

// Transcript turns the lines of agy's
// brain/<conversation>/.system_generated/logs/transcript_full.jsonl into
// chat events. Each line is one step, written once it is done: the user's
// input, a model response with its text and tool calls, then one GENERIC
// step per call with what the call returned, in the order of the calls.
// A response with no tool call ends the turn once no subagent still runs.
// A call shows once its result is in, as agy writes nothing while it runs.
type Transcript struct {
	turn       string               // the running turn's id; "" while idle
	asked      string               // the open request's id, "" for none
	askedInput bool                 // whether asked is a PendingUserInputs question
	model      string               // the model of the last response, without its effort
	effort     string               // that response's effort
	pending    []*pending           // the calls whose results have not come, oldest first
	tasks      string               // the task list's text as agy last wrote it
	agents     map[string]*subagent // spawned subagents by parent Agent call id
	byConv     map[string]*subagent // the same subagents by their conversationId
}

// pending is a call a response made, waiting for its GENERIC step. A call
// that wrote the task list shows as the list, so its result shows nothing.
type pending struct {
	call     session.ToolCall
	list     bool
	subagent bool
}

// subagent is an invoke_subagent call whose child conversationId was reported
// in its GENERIC result. Its own transcript fills SubagentTools/SubagentCalls,
// and the parent's SYSTEM_MESSAGE from sender=<convID> completes it.
type subagent struct {
	convID  string
	call    session.ToolCall
	running bool
	pending []session.ToolCall
}

func NewTranscript() *Transcript { return &Transcript{} }

// step is the fields of a transcript line the reader uses.
type step struct {
	Index     int    `json:"step_index"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Model     string `json:"model"`
	Content   string `json:"content"`
	Thinking  string `json:"thinking"`
	Error     string `json:"error"`
	ToolCalls []struct {
		Name string                     `json:"name"`
		Args map[string]json.RawMessage `json:"args"`
	} `json:"tool_calls"`
	// Media are a USER_INPUT's pictures, files agy copied into the
	// conversation's folder.
	Media []struct {
		MimeType string `json:"mime_type"`
		URI      string `json:"uri"`
	} `json:"media"`
}

var (
	// request is the user's own text in a USER_INPUT step, which agy wraps
	// with metadata for the model.
	request = regexp.MustCompile(`(?s)<USER_REQUEST>\n?(.*?)\n?</USER_REQUEST>`)
	// stamps are the lines agy starts each tool result with.
	stamps = regexp.MustCompile(`(?m)^(?:Created|Completed) At: .*\n?`)
	// exited is how a command's result gives its exit code.
	exited = regexp.MustCompile(`^Command exited with code (-?\d+)\.\s*(?:Command output:\n)?`)
)

// Line is one transcript line's events, stamped with the step's time.
func (t *Transcript) Line(b []byte) []session.Event {
	var s step
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	id := "step-" + strconv.Itoa(s.Index)
	var out []session.Event
	switch s.Type {
	case "USER_INPUT":
		text := s.Content
		if m := request.FindStringSubmatch(text); m != nil {
			text = m[1]
		}
		var atts []session.Attachment
		for _, m := range s.Media {
			if a, ok := session.ImageFile(m.URI, m.MimeType, len(atts)+1); ok {
				atts = append(atts, a)
			}
		}
		text = strings.TrimSpace(text)
		if len(atts) > 0 && (text == "(image)" || text == "(images)") {
			text = "" // why: the daemon types this for a picture sent alone, which agy's TUI needs
		}
		if text == "" && len(atts) == 0 {
			return nil
		}
		out = t.End(session.TurnCompleted)
		t.turn = id
		out = append(out, session.Event{Type: session.EventTurnStarted, TurnID: id},
			session.Event{Type: session.EventContent, ItemID: id, Kind: session.KindUserPrompt, Text: text, Attachments: atts})
	case "PLANNER_RESPONSE":
		if mdl, eff := splitModel(s.Model); mdl != "" && (mdl != t.model || eff != t.effort) {
			t.model, t.effort = mdl, eff
			out = append(out, session.Event{Type: session.EventSettings, Model: mdl})
		}
		if th := strings.TrimSpace(s.Thinking); th != "" {
			out = append(out, session.Event{Type: session.EventContent, ItemID: id + "-thinking", Kind: session.KindReasoningText, Model: t.model, Text: th})
		}
		if strings.TrimSpace(s.Content) != "" {
			out = append(out, session.Event{Type: session.EventContent, ItemID: id, Kind: session.KindAssistantText, Model: t.model, Text: s.Content})
		}
		for i, c := range s.ToolCalls {
			plan := t.taskList(c.Name, c.Args)
			tc := toolCall(id+"#"+strconv.Itoa(i), c.Name, c.Args)
			tc.StartedAt = stamp(s.CreatedAt)
			t.pending = append(t.pending, &pending{call: tc, list: plan != nil, subagent: c.Name == "invoke_subagent"})
			if plan != nil {
				out = append(out, *plan)
			}
		}
		if len(s.ToolCalls) == 0 && s.Status == "DONE" && !t.hasRunningSubagent() {
			out = append(out, t.End(session.TurnCompleted)...)
		}
	case "GENERIC":
		if len(t.pending) == 0 {
			return nil
		}
		p := t.pending[0]
		t.pending = t.pending[1:]
		if p.list {
			return nil
		}
		c := p.call
		text := strings.TrimSpace(stamps.ReplaceAllString(s.Content, ""))
		if p.subagent && s.Status != "ERROR" && s.Error == "" {
			if convID := parseSubagentLaunch(text); convID != "" {
				c.Status = "running"
				ag := &subagent{convID: convID, call: c, running: true}
				if t.agents == nil {
					t.agents = map[string]*subagent{}
					t.byConv = map[string]*subagent{}
				}
				t.agents[c.CallID] = ag
				t.byConv[convID] = ag
				out = append(out, t.agentEvent(ag, stamp(s.CreatedAt)))
				break
			}
		}
		c.Status = "completed"
		if m := exited.FindStringSubmatch(text); m != nil {
			code, _ := strconv.Atoi(m[1])
			c.ExitCode, text = &code, strings.TrimSpace(text[len(m[0]):])
			if code != 0 {
				c.Status = "failed"
			}
		}
		if s.Status == "ERROR" || s.Error != "" {
			c.Status, text = "failed", cmp.Or(strings.TrimSpace(s.Error), text)
		}
		c.Output = strings.ReplaceAll(text, "\r\n", "\n")
		out = append(out, session.Event{Type: session.EventContent, ItemID: c.CallID, Kind: session.KindToolCall, Model: t.model, Tool: &c})
	case "SYSTEM_MESSAGE":
		if sender, body, ok := parseSystemMessage(s.Content); ok {
			at := stamp(s.CreatedAt)
			if ag := t.byConv[sender]; ag != nil {
				ag.running = false
				ag.call.Status = "completed"
				ag.call.EndedAt = at
				if body != "" {
					if ag.call.Output != "" {
						ag.call.Output += "\n\n" + body
					} else {
						ag.call.Output = body
					}
				}
				out = append(out, t.agentEvent(ag, at))
			} else if sender == "system" && strings.Contains(body, "All your subagents and background tasks have been stopped") {
				for _, ag := range t.agents {
					if ag.running {
						ag.running = false
						ag.call.Status = "failed"
						ag.call.EndedAt = at
						out = append(out, t.agentEvent(ag, at))
					}
				}
			}
		}
	case "CHECKPOINT":
		out = append(out, session.Event{Type: session.EventContent, ItemID: id, Kind: session.KindSystem, Phase: session.PhaseCompact, Text: "Conversation compacted"})
	}
	at := stamp(s.CreatedAt)
	for i := range out {
		if out[i].CreatedAt == "" {
			out[i].CreatedAt = at
		}
	}
	return out
}

// toolCall is a call as the chat's tool rows read it: agy's shell, file
// read, edit and write tools under Claude Code's names, so they draw as a
// command, a file and a diff; any other tool under its own name with its
// arguments.
func toolCall(id, name string, args map[string]json.RawMessage) session.ToolCall {
	arg := func(k string) string {
		var s string
		_ = json.Unmarshal(args[k], &s)
		return s
	}
	c := session.ToolCall{CallID: id, ToolName: name}
	var in any = args
	switch name {
	case "run_command":
		c.ToolName, c.Command = "Bash", arg("CommandLine")
		in = map[string]string{"command": c.Command, "description": arg("toolSummary")}
	case "read_file", "view_file":
		c.ToolName = "Read"
		in = map[string]string{"file_path": cmp.Or(arg("Path"), arg("AbsolutePath"))}
	case "edit_file":
		c.ToolName = "Edit"
		in = map[string]string{"file_path": arg("Path"), "old_string": arg("Target"), "new_string": arg("Replacement")}
	case "write_file":
		c.ToolName = "Write"
		in = map[string]string{"file_path": arg("Path"), "content": arg("Content")}
	case "invoke_subagent":
		c.ToolName = "Agent"
		in = map[string]string{
			"description":   arg("Role"),
			"subagent_type": arg("TypeName"),
			"prompt":        arg("Prompt"),
		}
	}
	c.Input, _ = json.Marshal(in)
	if string(c.Input) == "null" {
		c.Input = json.RawMessage("{}")
	}
	return c
}

// checkbox is a line of agy's task list: "- [ ] todo", "- [/] doing",
// "- [x] done".
var checkbox = regexp.MustCompile(`(?m)^\s*[-*]\s+\[([ xX/-])\]\s+(.+?)\s*$`)

// taskList is the turn's task list after call, when the call writes or
// edits agy's task.md, its user-facing to-do list in the conversation's
// artifact folder: one task-list call, updated in place, as Claude Code's
// todos show. nil for any other call, and for an edit of a list this
// reader has not seen whole.
func (t *Transcript) taskList(name string, args map[string]json.RawMessage) *session.Event {
	arg := func(k string) string {
		var s string
		_ = json.Unmarshal(args[k], &s)
		return s
	}
	path := strings.ReplaceAll(arg("Path"), "\\", "/")
	base := path[strings.LastIndexByte(path, '/')+1:]
	if base != "task.md" && base != "tasks.md" || !strings.Contains(path, "/brain/") {
		return nil
	}
	switch name {
	case "write_file":
		t.tasks = arg("Content")
	case "edit_file":
		if t.tasks == "" {
			return nil
		}
		t.tasks = strings.ReplaceAll(t.tasks, arg("Target"), arg("Replacement"))
	default:
		return nil
	}
	status := map[string]string{" ": "pending", "x": "completed", "X": "completed", "/": "in_progress", "-": "in_progress"}
	c := &session.ToolCall{CallID: "plan-" + t.turn, ToolName: "Plan", Status: "completed", Input: json.RawMessage("{}"), Tasks: []session.Task{}}
	for _, m := range checkbox.FindAllStringSubmatch(t.tasks, -1) {
		c.Tasks = append(c.Tasks, session.Task{Subject: m[2], Status: status[m[1]]})
	}
	return &session.Event{Type: session.EventContent, ItemID: c.CallID, Kind: session.KindToolCall, Model: t.model, Tool: c}
}

// Running reports whether a turn runs.
func (t *Transcript) Running() bool { return t.turn != "" }

// Settings are the model and effort the session last ran with.
func (t *Transcript) Settings() (model, effort string) { return t.model, t.effort }

// splitModel splits agy's id of a model at an effort, as
// "gemini-3.8-flash-low" or "claude-opus-5-5-high", into the model its
// /model command and ACP option take and the effort; an id with no effort
// suffix is the model alone.
func splitModel(id string) (model, effort string) {
	for _, e := range []string{"low", "medium", "high", "xhigh", "max"} {
		if m, ok := strings.CutSuffix(id, "-"+e); ok && m != "" {
			return m, e
		}
	}
	return id, ""
}

// End completes the running turn as state, with the calls still waiting
// for a result shown as they were asked; nothing while idle. It withdraws
// an open request first.
func (t *Transcript) End(state string) []session.Event {
	out := t.Resolve()
	if t.turn == "" {
		return out
	}
	for _, p := range t.pending {
		if p.list {
			continue
		}
		c := p.call
		c.Status = "failed"
		out = append(out, session.Event{Type: session.EventContent, ItemID: c.CallID, Kind: session.KindToolCall, Model: t.model, Tool: &c})
	}
	t.pending = nil
	for _, ag := range t.agents {
		if !ag.running {
			continue
		}
		ag.running = false
		if state == session.TurnInterrupted {
			ag.call.Status = "failed"
		} else {
			ag.call.Status = "completed"
		}
		out = append(out, t.agentEvent(ag, ""))
	}
	out = append(out, session.Event{Type: session.EventTurnCompleted, TurnID: t.turn, State: state, Point: t.turn})
	t.turn = ""
	return out
}

// Ask opens request id, a permission prompt or plan approval agy's TUI shows.
// detail is what agy's terminal report or screen says, as "Approve Bash: rm -rf
// build" or "Approve plan".
func (t *Transcript) Ask(id, detail string) []session.Event {
	if id == t.asked && !t.askedInput {
		return nil
	}
	out := t.Resolve()
	t.asked, t.askedInput = id, false
	e := session.Event{Type: session.EventRequestOpened, RequestID: id, RequestType: session.RequestUnknown, Detail: detail}
	switch {
	case detail == "Approve plan" || strings.HasPrefix(detail, "Approve plan"):
		e.RequestType = session.RequestPlan
		e.Request = &session.ToolRequest{ToolName: "ExitPlanMode"}
	default:
		if tool, target, ok := strings.Cut(strings.TrimPrefix(detail, "Approve "), ": "); ok && tool == "Bash" {
			e.RequestType = session.RequestCommand
			e.Request = &session.ToolRequest{ToolName: tool, Command: target}
			e.Request.Input, _ = json.Marshal(map[string]string{"command": target})
		}
	}
	return append(out, e)
}

// AskInput opens request id as a user-input question card in the chat.
func (t *Transcript) AskInput(id, detail string, qs []session.Question) []session.Event {
	if id == t.asked && t.askedInput {
		return nil
	}
	out := t.Resolve()
	t.asked, t.askedInput = id, true
	return append(out, session.Event{
		Type:      session.EventInputRequested,
		RequestID: id,
		Detail:    detail,
		Questions: qs,
	})
}

// Resolve withdraws the open request, if any.
func (t *Transcript) Resolve() []session.Event {
	if t.asked == "" {
		return nil
	}
	id, input := t.asked, t.askedInput
	t.asked, t.askedInput = "", false
	if input {
		return []session.Event{{Type: session.EventInputResolved, RequestID: id}}
	}
	return []session.Event{{Type: session.EventRequestResolved, RequestID: id}}
}

// hasRunningSubagent reports whether any spawned subagent is still running.
func (t *Transcript) hasRunningSubagent() bool {
	for _, ag := range t.agents {
		if ag.running {
			return true
		}
	}
	return false
}

// agentEvent returns a KindToolCall event for ag with cloned slices so
// subsequent mutations to ag.call do not race with emitted snapshots.
func (t *Transcript) agentEvent(ag *subagent, at string) session.Event {
	c := ag.call
	c.SubagentTools = slices.Clone(c.SubagentTools)
	c.SubagentCalls = slices.Clone(c.SubagentCalls)
	return session.Event{Type: session.EventContent, ItemID: c.CallID, Kind: session.KindToolCall, Model: t.model, CreatedAt: at, Tool: &c}
}

// Subagents are the session's invoke_subagent calls by tool call id, and
// whether each still runs.
func (t *Transcript) Subagents() map[string]bool {
	if len(t.agents) == 0 {
		return nil
	}
	out := make(map[string]bool, len(t.agents))
	for id, ag := range t.agents {
		out[id] = ag.running
	}
	return out
}

// SubagentPaths maps each spawned subagent's call id to its own
// transcript_full.jsonl path beside mainPath under agy's brain directory.
func (t *Transcript) SubagentPaths(mainPath string) map[string]string {
	if len(t.agents) == 0 || mainPath == "" {
		return nil
	}
	logs := filepath.Dir(filepath.Clean(mainPath))
	sys := filepath.Dir(logs)
	conv := filepath.Dir(sys)
	brain := filepath.Dir(conv)
	if filepath.Base(logs) != "logs" || filepath.Base(sys) != ".system_generated" || filepath.Base(brain) != "brain" {
		return nil
	}
	out := make(map[string]string, len(t.agents))
	for id, ag := range t.agents {
		if ag.convID == "" || ag.convID == "." || ag.convID == ".." || strings.ContainsAny(ag.convID, `/\`) {
			continue
		}
		out[id] = filepath.Join(brain, ag.convID, ".system_generated", "logs", "transcript_full.jsonl")
	}
	return out
}

// Subagent folds one line of the subagent's own transcript into the parent
// Agent card for call.
func (t *Transcript) Subagent(call string, b []byte) []session.Event {
	ag := t.agents[call]
	if ag == nil {
		return nil
	}
	var s step
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	at := stamp(s.CreatedAt)
	id := "step-" + strconv.Itoa(s.Index)
	switch s.Type {
	case "PLANNER_RESPONSE":
		if len(s.ToolCalls) == 0 {
			return nil
		}
		for i, c := range s.ToolCalls {
			tc := toolCall(id+"#"+strconv.Itoa(i), c.Name, c.Args)
			tc.Status = "running"
			tc.StartedAt = at
			ag.pending = append(ag.pending, tc)
			ag.call.SubagentTools = append(ag.call.SubagentTools, tc.ToolName)
			ag.call.SubagentCalls = append(ag.call.SubagentCalls, tc)
		}
		ag.call.SubagentCalls = session.KeepSubagentCalls(ag.call.SubagentCalls)
		return []session.Event{t.agentEvent(ag, at)}
	case "GENERIC":
		if len(ag.pending) == 0 {
			return nil
		}
		tc := ag.pending[0]
		ag.pending = ag.pending[1:]
		status := "completed"
		text := strings.TrimSpace(stamps.ReplaceAllString(s.Content, ""))
		if m := exited.FindStringSubmatch(text); m != nil {
			if code, _ := strconv.Atoi(m[1]); code != 0 {
				status = "failed"
			}
		}
		if s.Status == "ERROR" || s.Error != "" {
			status = "failed"
		}
		for i := range ag.call.SubagentCalls {
			if ag.call.SubagentCalls[i].CallID == tc.CallID {
				ag.call.SubagentCalls[i].Status = status
				ag.call.SubagentCalls[i].EndedAt = at
				return []session.Event{t.agentEvent(ag, at)}
			}
		}
	}
	return nil
}

// parseSubagentLaunch extracts the child conversationId from an
// invoke_subagent GENERIC result ("Created the following subagent:\n{...}").
func parseSubagentLaunch(text string) string {
	_, rest, ok := strings.Cut(text, "Created the following subagent:")
	if !ok {
		return ""
	}
	start := strings.IndexByte(rest, '{')
	if start < 0 {
		return ""
	}
	var res struct {
		ConversationID string `json:"conversationId"`
	}
	if json.NewDecoder(strings.NewReader(rest[start:])).Decode(&res) != nil {
		return ""
	}
	if res.ConversationID == "" || res.ConversationID == "." || res.ConversationID == ".." || strings.ContainsAny(res.ConversationID, `/\`) {
		return ""
	}
	return res.ConversationID
}

var sysMsgRe = regexp.MustCompile(`(?s)<SYSTEM_MESSAGE>\s*\[Message\]\s+[^\n]*?\bsender=(\S+)[^\n]*?\bcontent=(.*?)\s*</SYSTEM_MESSAGE>`)

// parseSystemMessage extracts the sender conversation id and body from an
// agy SYSTEM_MESSAGE step's content.
func parseSystemMessage(content string) (sender, body string, ok bool) {
	m := sysMsgRe.FindStringSubmatch(content)
	if m == nil {
		return "", "", false
	}
	return m[1], strings.TrimSpace(m[2]), true
}

// MarkOf is transcript line b's mark for a reader paging through it: a
// turn starts at the user's input.
func MarkOf(b []byte) claude.Mark {
	var s struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(b, &s) != nil {
		return claude.MarkNone
	}
	switch s.Type {
	case "USER_INPUT":
		return claude.MarkPrompt
	case "CHECKPOINT":
		return claude.MarkCompact
	case "PLANNER_RESPONSE", "GENERIC":
		return claude.MarkItem
	}
	return claude.MarkNone
}

// now is the clock stamp uses for a line without a time; tests set it.
var now = time.Now

// stamp is a step's created_at as session.Stamp writes it, now when it
// has none.
func stamp(ts string) string {
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		at = now()
	}
	return session.Stamp(at)
}
