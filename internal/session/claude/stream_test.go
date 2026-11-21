package claude

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

// reduce feeds stdout lines to a stream and reduces what it emits into a
// snapshot, as the chat loop does.
func reduce(t *testing.T, s *stream, snap session.Snapshot, lines ...string) (session.Snapshot, []session.Event) {
	t.Helper()
	var all []session.Event
	for _, l := range lines {
		for _, e := range s.line([]byte(l)) {
			e.CreatedAt = "2026-11-18T10:00:00.000Z"
			all = append(all, e)
			for _, d := range session.Reduce(snap, e) {
				d.Version, d.SessionID, d.Seq, d.Revision, d.UpdatedAt = session.DeltaVersion, snap.SessionID, snap.Revision+1, snap.Revision+1, e.CreatedAt
				n, err := session.Apply(&snap, snap.Revision, d)
				if err != nil {
					t.Fatalf("%s: %v", d.Type, err)
				}
				snap = n
			}
		}
	}
	return snap, all
}

func base() session.Snapshot {
	return session.Snapshot{SessionID: "c1", SessionState: session.StateReady, Running: true}
}

// The keys of a real system/init from the spike's captures
// (testdata/basic.stdout.jsonl), with short lists.
const initLine = `{"type":"system","subtype":"init","cwd":"/tmp/acme-api","session_id":"SESSION","tools":["Task","Bash"],"mcp_servers":[],"model":"claude-sonnet-5-5","permissionMode":"default","slash_commands":["review","doctor","color","compact","config"],"terminal_slash_commands":["doctor","color"],"apiKeySource":"none","claude_code_version":"2.1.314","output_style":"default","agents":["Explore","Plan"],"skills":["review"],"plugins":[],"capabilities":["interrupt_receipt_v1"],"uuid":"init-1"}`

func TestInitCommands(t *testing.T) {
	s := newStream("SESSION")
	s.conf = t.TempDir()
	review := filepath.Join(s.conf, "skills", "review", "SKILL.md")
	os.MkdirAll(filepath.Dir(review), 0o755)
	os.WriteFile(review, []byte("---\ndescription: Review the branch\n---\n"), 0o600)
	snap, events := reduce(t, s, base(), initLine)
	// The init's own list first, described from the config dir and the
	// built-ins; then the built-ins that open the terminal, which -p
	// leaves out.
	want := []session.Command{{Name: "review", Description: "Review the branch", Source: "user"},
		{Name: "doctor", Description: Builtin("doctor"), Terminal: true},
		{Name: "color", Description: Builtin("color"), Terminal: true}, {Name: "compact", Description: Builtin("compact")},
		{Name: "config", Description: "Open settings", Terminal: true, Aliases: []string{"settings"}}}
	if len(snap.Commands) <= len(want) || !reflect.DeepEqual(snap.Commands[:len(want)], want) {
		t.Fatalf("commands: %+v", snap.Commands)
	}
	for _, c := range snap.Commands[len(want):] {
		if !c.Terminal || c.Description == "" || c.Name == "config" || c.Name == "doctor" {
			t.Errorf("after the init's list: %+v", c)
		}
	}
	if !slices.ContainsFunc(snap.Commands, func(c session.Command) bool { return c.Name == "resume" }) {
		t.Error("no /resume, which opens the terminal")
	}
	// Each turn repeats system/init; the same list emits nothing.
	if _, events = reduce(t, s, snap, initLine); len(events) != 0 {
		t.Fatalf("repeated init: %+v", events)
	}
}

// The composer's meter needs the used context and the window: the first
// result gives both, and each later API response moves the used part.
func TestContextWindow(t *testing.T) {
	s := newStream("SESSION")
	s.turn = "turn-1"
	snap, _ := reduce(t, s, base(), initLine,
		`{"type":"assistant","message":{"id":"msg_1","model":"claude-sonnet-5-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":2,"output_tokens":4,"cache_read_input_tokens":11384,"cache_creation_input_tokens":5817}},"session_id":"SESSION","uuid":"a-1"}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"hi","session_id":"SESSION","uuid":"r-1","total_cost_usd":0.02,"modelUsage":{"claude-sonnet-5-5":{"inputTokens":2,"outputTokens":4,"cacheReadInputTokens":11384,"cacheCreationInputTokens":5817,"costUSD":0.02,"contextWindow":200000}}}`)
	if u := snap.Usage; u == nil || u.ContextTokens == nil || *u.ContextTokens != 17207 || u.MaxTokens == nil || *u.MaxTokens != 200000 {
		t.Fatalf("after the first turn: %+v", snap.Usage)
	}
	s.turn = "turn-2"
	snap, _ = reduce(t, s, snap,
		`{"type":"assistant","message":{"id":"msg_2","model":"claude-sonnet-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}],"usage":{"input_tokens":2,"output_tokens":40,"cache_read_input_tokens":17207,"cache_creation_input_tokens":100}},"session_id":"SESSION","uuid":"a-2"}`)
	if u := snap.Usage; u == nil || *u.ContextTokens != 17349 || *u.MaxTokens != 200000 || u.InputTokens != 2 || u.OutputTokens != 4 {
		t.Fatalf("during the second turn: %+v", snap.Usage)
	}
}

// TestStreamHarness: stream-json lines as Claude Code 2.1.328 printed them
// live, trimmed: task lists, a background subagent that ends after its
// turn, a background command, a hook's block, an API retry and a
// compaction.
func TestStreamHarness(t *testing.T) {
	s := newStream("s")
	var got []session.Event
	for _, l := range []string{
		`{"type":"stream_event","api_message_id":"m0","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}}`,
		`{"type":"assistant","message":{"id":"m0","content":[{"type":"thinking","thinking":"","signature":"x"}]}}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"tool_use","id":"c1","name":"TaskCreate","input":{"subject":"Read notes","activeForm":"Reading notes"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c1","content":"Task #4 created successfully: Read notes"}]}}`,
		`{"type":"assistant","message":{"id":"m2","content":[{"type":"tool_use","id":"c2","name":"TaskCreate","input":{"subject":"Report","activeForm":"Reporting"}}]}}`,
		`{"type":"assistant","message":{"id":"m3","content":[{"type":"tool_use","id":"c3","name":"TaskUpdate","input":{"taskId":"4","status":"in_progress"}}]}}`,
		`{"type":"assistant","message":{"id":"m4","content":[{"type":"tool_use","id":"ag","name":"Agent","input":{"description":"Read notes first line A","subagent_type":"general-purpose","prompt":"Read notes.txt"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"ag","content":[{"type":"text","text":"Async agent launched successfully."}]}]},"tool_use_result":{"isAsync":true,"status":"async_launched","agentId":"a1"}}`,
		`{"type":"assistant","message":{"id":"m5","content":[{"type":"tool_use","id":"bg","name":"Bash","input":{"command":"sleep 3; echo bg-done","run_in_background":true}}]}}`,
		`{"type":"system","subtype":"task_started","task_id":"bt1","tool_use_id":"bg","description":"Background sleep then print","is_backgrounded":true,"task_type":"local_bash"}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"bg","content":"Command running in background with ID: bt1."}]}}`,
		`{"type":"system","subtype":"hook_response","hook_name":"PreToolUse:Bash","hook_event":"PreToolUse","stderr":"Blocked: forbidden commands are not allowed here","exit_code":2,"outcome":"error"}`,
		`{"type":"system","subtype":"api_retry","attempt":2,"max_retries":10,"retry_delay_ms":3500,"error_status":529,"error":"overloaded_error"}`,
		// Live order: the subagent's call comes before the turn's result, its end after.
		`{"type":"assistant","parent_tool_use_id":"ag","message":{"id":"s1","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":"notes.txt"}}]}}`,
		`{"type":"result","subtype":"success","is_error":false,"modelUsage":{}}`,
		`{"type":"user","parent_tool_use_id":"ag","message":{"content":[{"type":"tool_result","tool_use_id":"r1","content":"1\talpha"}]}}`,
		`{"type":"system","subtype":"task_notification","task_id":"a1","tool_use_id":"ag","status":"completed","summary":"The first line is alpha","summary_is_report":true}`,
		`{"type":"system","subtype":"task_notification","task_id":"bt1","tool_use_id":"bg","status":"completed","summary":"Background command \"Background sleep then print\" completed (exit code 0)"}`,
		`{"type":"system","subtype":"compact_boundary","uuid":"cb","compact_metadata":{"trigger":"manual"}}`,
		`{"type":"user","message":{"content":"This session is being continued from a previous conversation that ran out of context.\n\nSummary:\n1. Did things."}}`,
		`{"type":"user","message":{"content":"<local-command-stdout>Compacted </local-command-stdout>"}}`,
	} {
		got = append(got, s.line([]byte(l))...)
	}
	var thinking int
	var tasks []session.Task
	var agent, shell *session.ToolCall
	var sys []string
	compact := ""
	for _, e := range got {
		switch {
		case e.Kind == session.KindReasoningText:
			thinking++
		case e.Tool != nil && e.Tool.Tasks != nil:
			tasks = e.Tool.Tasks
		case e.Tool != nil && e.ItemID == "ag":
			agent = e.Tool
		case e.Tool != nil && e.ItemID == "bg":
			shell = e.Tool
		case e.Kind == session.KindSystem && e.ItemID == "cb":
			compact += e.Text
		case e.Kind == session.KindSystem:
			sys = append(sys, e.Phase+": "+e.Text)
		}
	}
	if thinking != 2 {
		t.Errorf("%d thinking events, want the block's start and its end", thinking)
	}
	if len(tasks) != 2 || tasks[0].ID != "4" || tasks[0].Status != "in_progress" || tasks[0].ActiveForm != "Reading notes" || tasks[1].ID != "5" {
		t.Errorf("tasks %+v: want #4 in progress as Claude Code numbered it, #5 pending", tasks)
	}
	if agent == nil || agent.Status != "completed" || agent.Output != "The first line is alpha" ||
		len(agent.SubagentCalls) != 1 || agent.SubagentCalls[0].Status != "completed" {
		t.Errorf("agent %+v: want it ended by its notification after the turn, its Read done", agent)
	}
	want := []string{
		"hook: PreToolUse:Bash hook blocked: Blocked: forbidden commands are not allowed here",
		"warning: API error 529: overloaded_error · retrying in 4s (attempt 2 of 10)",
	}
	if !slices.Equal(sys, want) {
		t.Errorf("system items %q, want %q", sys, want)
	}
	if shell == nil || shell.Background != "completed" {
		t.Errorf("shell %+v: want its own row to say it ended", shell)
	}
	if compact != "Conversation compacted\n\n1. Did things." {
		t.Errorf("compaction %q", compact)
	}
}

// TestPartialMessages: text deltas grow their item as they stream and its
// finished line adds nothing; thinking deltas show nothing, as only the
// finished line's signature tells a summary from thinking, so that line
// adds the whole block: one reasoning item and one text item, each with
// the whole block once.
func TestPartialMessages(t *testing.T) {
	s := newStream("SESSION")
	s.turn = "turn-1"
	delta := func(i int, typ, field, text string) string {
		return `{"type":"stream_event","api_message_id":"msg_1","session_id":"SESSION","event":{"type":"content_block_delta","index":` +
			string(rune('0'+i)) + `,"delta":{"type":"` + typ + `","` + field + `":"` + text + `"}}}`
	}
	snap := base()
	var grown []string
	for _, l := range []string{
		`{"type":"stream_event","api_message_id":"msg_1","session_id":"SESSION","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}}`,
		delta(0, "thinking_delta", "thinking", "Let me "),
		delta(0, "thinking_delta", "thinking", "count."),
		delta(1, "text_delta", "text", "One, "),
		delta(1, "text_delta", "text", "two, "),
		delta(1, "text_delta", "text", "three."),
	} {
		snap, _ = reduce(t, s, snap, l)
		for _, it := range snap.Content {
			grown = append(grown, string(it.Kind)+":"+it.Text)
		}
	}
	if want := []string{"reasoning-text:", "reasoning-text:", "reasoning-text:", "reasoning-text:", "assistant-text:One, ", "reasoning-text:", "assistant-text:One, two, ", "reasoning-text:", "assistant-text:One, two, three."}; !reflect.DeepEqual(grown, want) {
		t.Fatalf("grew %q, want %q", grown, want)
	}
	snap, events := reduce(t, s, snap,
		`{"type":"assistant","message":{"id":"msg_1","model":"claude-sonnet-5-5","content":[{"type":"thinking","thinking":"Let me count."}]},"session_id":"SESSION","uuid":"a-1"}`,
		`{"type":"assistant","message":{"id":"msg_1","model":"claude-sonnet-5-5","content":[{"type":"text","text":"One, two, three."}]},"session_id":"SESSION","uuid":"a-2"}`)
	if len(events) != 1 || events[0].Text != "Let me count." {
		t.Fatalf("finished lines emitted %+v, want the thinking block once", events)
	}
	var got []string
	for _, it := range snap.Content {
		got = append(got, string(it.Kind)+":"+it.Text)
	}
	if want := []string{"reasoning-text:Let me count.", "assistant-text:One, two, three."}; !reflect.DeepEqual(got, want) {
		t.Fatalf("items %q, want %q", got, want)
	}
}

func TestHarnessMalformed(t *testing.T) {
	for _, in := range []string{"", "<command-name>", "<task-notification>unclosed", "<local-command-caveat>x</local-command-caveat>", "<system-reminder>y</system-reminder>", "plain text"} {
		if n := harness(in); n.phase != "" && n.phase != session.PhaseCommand || n.notified != "" {
			t.Errorf("harness(%q) = %+v", in, n)
		}
	}
	if n := harness("<command-name>/x</command-name>"); n.text != "/x" {
		t.Errorf("no args: %q", n.text)
	}
	if hookLine("", nil, "") != "A hook failed" || retryLine(nil, "", 0, 0, 0) != "API error · retrying" || errText([]byte(`{"error":{"message":"m"}}`)) != "m" {
		t.Error("hookLine, retryLine or errText")
	}
}

// A task notification's summary and result come XML-escaped, as Claude
// Code writes them inside its tags; the card shows the text.
func TestHarnessNotificationEntities(t *testing.T) {
	n := harness("<task-notification>\n<tool-use-id>t1</tool-use-id>\n<summary>Agent \"a &amp; b\" finished</summary>\n<result>prints `hello, &lt;name&gt;`</result>\n</task-notification>")
	if n.notified != "t1" || n.summary != `Agent "a & b" finished` || n.result != "prints `hello, <name>`" {
		t.Errorf("note %+v", n)
	}
}

// TestStreamEchoes: the output of a set_model the driver sent says
// nothing the chat loop did not, and a line a compaction replays shows
// once.
func TestStreamEchoes(t *testing.T) {
	s := newStream("s")
	s.modelEchoes = 1
	line := `{"type":"user","uuid":"u1","message":{"content":"<local-command-stdout>Set model to claude-opus-5-5</local-command-stdout>"}}`
	if got := s.line([]byte(line)); len(got) != 0 {
		t.Fatalf("echo shown: %+v", got)
	}
	typed := `{"type":"user","uuid":"u2","message":{"content":"<local-command-stdout>Set model to claude-sonnet-5-5</local-command-stdout>"}}`
	if got := s.line([]byte(typed)); len(got) != 1 || got[0].Text != "Switched to claude-sonnet-5-5" {
		t.Fatalf("typed /model: %+v", got)
	}
	if got := s.line([]byte(typed)); len(got) != 0 {
		t.Fatalf("replayed line shown again: %+v", got)
	}
}

// The CLI's init names the model it runs, picked or its default, and the
// snapshot shows it.
func TestInitModel(t *testing.T) {
	snap, _ := reduce(t, newStream("SESSION"), base(), initLine)
	if snap.Model == nil || *snap.Model != "claude-sonnet-5-5" {
		t.Fatalf("model %v: want init's claude-sonnet-5-5", snap.Model)
	}
}

// A line Steer wrote joins the running turn when the CLI starts it then;
// started after the turn's result, it runs as a turn of its own.
func TestStreamSteered(t *testing.T) {
	s := newStream("s")
	s.turn = "t1"
	s.steered["m2"] = true
	if es := s.line([]byte(`{"type":"command_lifecycle","command_uuid":"m2","state":"started"}`)); len(es) != 0 || s.turn != "t1" {
		t.Fatalf("started mid-turn: %+v, turn %q", es, s.turn)
	}
	s.steered["m3"] = true
	s.line([]byte(`{"type":"result","subtype":"success"}`))
	es := s.line([]byte(`{"type":"command_lifecycle","command_uuid":"m3","state":"started"}`))
	if len(es) != 1 || es[0].Type != session.EventTurnStarted || es[0].TurnID != "m3" || s.turn != "m3" {
		t.Fatalf("started after the result: %+v, turn %q", es, s.turn)
	}
	if es := s.line([]byte(`{"type":"command_lifecycle","command_uuid":"other","state":"started"}`)); len(es) != 0 {
		t.Fatalf("a line Send wrote: %+v", es)
	}
}

// The answer to the driver's initialize, as claude 2.1.330 gave it in a
// temp config dir: descriptions without their scope tag, which becomes
// the source, plugins' commands marked, aliases and hints kept, internal
// ones left out, and the terminal built-ins after. An answer to another
// request lists nothing.
func TestInitializeCommands(t *testing.T) {
	s := newStream("SESSION")
	s.initID = "req_1"
	answer := func(id string) string {
		return `{"type":"control_response","response":{"subtype":"success","request_id":"` + id + `","response":{"commands":[` +
			`{"name":"myskill","description":"My skill does things (user)","argumentHint":"target"},` +
			`{"name":"deploy","description":"Deploy it (project)","argumentHint":""},` +
			`{"name":"demo:demo-skill","description":"skill of demo (plugin)","argumentHint":""},` +
			`{"name":"usage","description":"Show session cost","argumentHint":"","aliases":["cost","stats"],"builtin":true},` +
			`{"name":"heapdump","description":"Dump the heap","builtin":true},` +
			`{"name":"__remote-workflow","description":"internal","builtin":true}]}}}`
	}
	if _, events := reduce(t, s, base(), answer("req_9")); len(events) != 0 {
		t.Fatalf("an answer to another request listed %+v", events)
	}
	snap, _ := reduce(t, s, base(), answer("req_1"))
	want := []session.Command{
		{Name: "myskill", Description: "My skill does things", Hint: "target", Source: "user"},
		{Name: "deploy", Description: "Deploy it", Source: "project"},
		{Name: "demo:demo-skill", Description: "skill of demo", Source: "plugin"},
		{Name: "usage", Description: "Show session cost", Aliases: []string{"cost", "stats"}},
	}
	if len(snap.Commands) <= len(want) || !slices.EqualFunc(snap.Commands[:len(want)], want, session.Command.Equal) {
		t.Fatalf("commands: %+v", snap.Commands)
	}
	for _, c := range snap.Commands[len(want):] {
		if !c.Terminal || hiddenCommand(c.Name) {
			t.Errorf("after the answer's own: %+v", c)
		}
	}
	// The first turn's init lists the same names: no new list. One that
	// differs, as after /reload-plugins, lists again, described.
	same := `{"type":"system","subtype":"init","session_id":"SESSION","slash_commands":["usage","myskill","deploy","demo:demo-skill","heapdump","__remote-workflow"]}`
	if _, events := reduce(t, s, snap, same); slices.ContainsFunc(events, func(e session.Event) bool { return e.Type == session.EventCommands }) {
		t.Fatalf("the same names listed again: %+v", events)
	}
	more := `{"type":"system","subtype":"init","session_id":"SESSION","slash_commands":["usage","myskill","deploy","demo:demo-skill","fresh"]}`
	snap, _ = reduce(t, s, snap, more)
	if i := slices.IndexFunc(snap.Commands, func(c session.Command) bool { return c.Name == "fresh" }); i < 0 || snap.Commands[0].Description != "Show session cost" {
		t.Fatalf("after a reload: %+v", snap.Commands)
	}
}
