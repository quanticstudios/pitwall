package flow

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

func at(m, s int) time.Time { return time.Date(2026, 10, 5, 10, m, s, 0, time.UTC) }

func read(t *testing.T, provider model.Provider, path string) Feed {
	t.Helper()
	s := newSession(provider, path, false)
	s.poll()
	return s.b.feed()
}

func checkCalls(t *testing.T, what string, got []Call, want []Call) {
	t.Helper()
	for i := range got {
		got[i].Time = time.Time{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s calls:\n got %+v\nwant %+v", what, got, want)
	}
}

func TestClaude(t *testing.T) {
	f := read(t, model.ProviderClaude, "testdata/claude/session.jsonl")
	if len(f.Turns) != 2 {
		t.Fatalf("turns = %+v", f.Turns)
	}
	t0, t1 := f.Turns[0], f.Turns[1]
	if t0.Prompt != "Fix the login bug" || !t0.Start.Equal(at(0, 0)) || !t0.End.Equal(at(2, 5)) || t0.Reply != "The e2e suite passes too." || t0.Errors() != 1 {
		t.Errorf("turn 0 = %+v", t0)
	}
	checkCalls(t, "turn 0", t0.Calls, []Call{
		{Tool: "Bash", Arg: "go test ./...", Failed: true},
		{Tool: "Read", Arg: "login.go"},
		{Tool: "TodoWrite"},
		{Tool: "Agent"},
		{Tool: "Agent"},
	})
	if !reflect.DeepEqual(t0.Subagents, []int{0, 1}) {
		t.Errorf("turn 0 subagents = %v", t0.Subagents)
	}
	if t1.Prompt != "Now add a regression test" || !t1.End.IsZero() {
		t.Errorf("turn 1 = %+v", t1)
	}
	checkCalls(t, "turn 1", t1.Calls, []Call{{Tool: "Grep", Arg: "func Login", Running: true}})
	if want := []Step{{"Find the bug", StepDone}, {"Fix it", StepActive}, {"Add a test", StepPending}}; !reflect.DeepEqual(f.Plan, want) {
		t.Errorf("plan = %+v", f.Plan)
	}
	if len(f.Subagents) != 2 {
		t.Fatalf("subagents = %+v", f.Subagents)
	}
	fg, bg := f.Subagents[0], f.Subagents[1]
	if fg.ID != "tu4" || fg.Name != "Search callers" || fg.Type != "Explore" || fg.Prompt != "Find all callers of Login." ||
		!fg.Start.Equal(at(0, 14)) || !fg.End.Equal(at(0, 40)) || fg.Latest != "Found 3 callers." || fg.Failed {
		t.Errorf("foreground = %+v", fg)
	}
	checkCalls(t, "foreground", fg.Calls, []Call{{Tool: "Grep", Arg: "Login("}})
	if bg.ID != "tu5" || bg.Type != "general-purpose" || !bg.End.Equal(at(2, 0)) || bg.Latest != "All e2e tests pass." {
		t.Errorf("background = %+v", bg)
	}
	checkCalls(t, "background", bg.Calls, []Call{{Tool: "Bash", Arg: "make e2e"}, {Tool: "SubagentHandback"}})
}

// The Task tools own the plan from the first TaskCreate after a TodoWrite,
// keyed by task id, until the next TodoWrite replaces it.
func TestClaudeTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, path, `{"type":"user","timestamp":"2026-10-05T10:00:00Z","message":{"content":"Plan it"}}
{"type":"assistant","timestamp":"2026-10-05T10:00:00Z","message":{"content":[{"type":"tool_use","id":"t","name":"TodoWrite","input":{"todos":[{"content":"Old","status":"pending"}]}}]}}
{"type":"assistant","timestamp":"2026-10-05T10:00:01Z","message":{"content":[{"type":"tool_use","id":"a","name":"TaskCreate","input":{"subject":"One"}},{"type":"tool_use","id":"b","name":"TaskCreate","input":{"subject":"Two"}},{"type":"tool_use","id":"c","name":"TaskCreate","input":{"subject":"Three"}}]}}
{"type":"user","timestamp":"2026-10-05T10:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]},"toolUseResult":{"task":{"id":"7","subject":"One"}}}
{"type":"user","timestamp":"2026-10-05T10:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"b","content":"ok"}]}}
{"type":"assistant","timestamp":"2026-10-05T10:00:03Z","message":{"content":[{"type":"tool_use","id":"d","name":"TaskUpdate","input":{"taskId":"7","status":"completed"}},{"type":"tool_use","id":"e","name":"TaskUpdate","input":{"taskId":"2","status":"in_progress"}},{"type":"tool_use","id":"f","name":"TaskUpdate","input":{"taskId":3,"status":"deleted"}}]}}
`)
	f := read(t, model.ProviderClaude, path)
	if want := []Step{{"One", StepDone}, {"Two", StepActive}}; !reflect.DeepEqual(f.Plan, want) {
		t.Errorf("plan = %+v", f.Plan)
	}
	appendFile(t, path, `{"type":"assistant","timestamp":"2026-10-05T10:00:04Z","message":{"content":[{"type":"tool_use","id":"g","name":"TodoWrite","input":{"todos":[{"content":"New","status":"in_progress"}]}},{"type":"tool_use","id":"h","name":"TaskUpdate","input":{"taskId":"2","status":"completed"}}]}}
`)
	f = read(t, model.ProviderClaude, path)
	if want := []Step{{"New", StepActive}}; !reflect.DeepEqual(f.Plan, want) {
		t.Errorf("plan after TodoWrite = %+v", f.Plan)
	}
}

func TestCodex(t *testing.T) {
	f := read(t, model.ProviderCodex, "testdata/codex/2026/10/05/rollout-2026-10-05T10-00-00-parent-thread.jsonl")
	if len(f.Turns) != 3 {
		t.Fatalf("turns = %+v", f.Turns)
	}
	t0, t1, t2 := f.Turns[0], f.Turns[1], f.Turns[2]
	if t0.Prompt != "Refactor the parser" || !t0.Start.Equal(at(0, 1)) || !t0.End.Equal(at(0, 41)) || t0.Reply != "Split the parser into two files." {
		t.Errorf("turn 0 = %+v", t0)
	}
	checkCalls(t, "turn 0", t0.Calls, []Call{
		{Tool: "update_plan"},
		{Tool: "exec_command", Arg: "rg parse", Failed: true},
		{Tool: "apply_patch", Arg: "parse.go"},
		{Tool: "exec", Arg: "go test ./...", Failed: true},
		{Tool: "spawn_agent"},
		{Tool: "wait_agent"},
	})
	if t1.Prompt != "Run it again" || !t1.End.Equal(at(1, 9)) {
		t.Errorf("turn 1 = %+v", t1)
	}
	checkCalls(t, "turn 1", t1.Calls, []Call{{Tool: "shell", Arg: "make run", Failed: true}})
	if t2.Prompt != "Check the logs" || !t2.End.IsZero() {
		t.Errorf("turn 2 = %+v", t2)
	}
	checkCalls(t, "turn 2", t2.Calls, []Call{{Tool: "shell", Arg: "tail -n 5 app.log", Running: true}})
	if want := []Step{{"Read the code", StepDone}, {"Split the parser", StepActive}, {"Run tests", StepPending}}; !reflect.DeepEqual(f.Plan, want) {
		t.Errorf("plan = %+v", f.Plan)
	}
	if len(f.Subagents) != 1 {
		t.Fatalf("subagents = %+v", f.Subagents)
	}
	s := f.Subagents[0]
	if s.ID != "c5" || s.Name != "docs_check" || s.Prompt != "Check the docs mention the new files." || !s.End.Equal(at(0, 30)) || s.Latest != "Docs are fine." {
		t.Errorf("subagent = %+v", s)
	}
	checkCalls(t, "subagent", s.Calls, []Call{{Tool: "exec", Arg: "rg parse docs/"}})
}

func TestPi(t *testing.T) {
	f := read(t, model.ProviderPi, "testdata/pi/2026-10-05T10-00-00-000Z_s1.jsonl")
	if len(f.Turns) != 2 || f.Plan != nil || f.Subagents != nil {
		t.Fatalf("feed = %+v", f)
	}
	t0, t1 := f.Turns[0], f.Turns[1]
	if t0.Prompt != "List the files" || !t0.End.Equal(at(0, 6)) || t0.Reply != "There is one file." {
		t.Errorf("turn 0 = %+v", t0)
	}
	checkCalls(t, "turn 0", t0.Calls, []Call{{Tool: "bash", Arg: "ls -la"}, {Tool: "read", Arg: "README.md", Failed: true}})
	if t1.Prompt != "Edit it" || !t1.End.IsZero() {
		t.Errorf("turn 1 = %+v", t1)
	}
	checkCalls(t, "turn 1", t1.Calls, []Call{{Tool: "edit", Arg: "a.go", Running: true}})
}

func TestArg(t *testing.T) {
	for _, c := range []struct{ tool, input, want string }{
		{"Bash", `{"command":"  make\nsecond"}`, "make"},
		{"shell", `{"command":["bash","-lc","ls\nx"]}`, "ls"},
		{"Write", `{"file_path":"C:\\repo\\main.go"}`, "main.go"},
		{"Glob", `{"pattern":"**/*.go"}`, "**/*.go"},
		{"WebSearch", `{"query":"gio layout"}`, "gio layout"},
		{"WebFetch", `{"url":"https://example.com/a?b"}`, "example.com"},
		{"Bash", `{"command":"` + strings.Repeat("é", 200) + `"}`, strings.Repeat("é", 120)},
		{"mcp__x__y", `{"q":"1"}`, ""},
		{"Bash", `not json`, ""},
	} {
		b := newBuilder(model.ProviderClaude)
		b.call(time.Time{}, "", c.tool, arg(c.tool, []byte(c.input)))
		if got := b.turns[0].Calls[0].Arg; got != c.want {
			t.Errorf("%s %s: Arg = %q, want %q", c.tool, c.input, got, c.want)
		}
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
}

// Watch follows the file as it grows, holds a partial last line, tails a
// running subagent found by its meta.json, and starts over when the file
// is truncated.
func TestWatch(t *testing.T) {
	pollEvery = 5 * time.Millisecond
	defer func() { pollEvery = 500 * time.Millisecond }()
	src, err := os.ReadFile("testdata/claude/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(string(src), "\n")
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	subs := filepath.Join(dir, "s", "subagents")
	if err := os.MkdirAll(subs, 0o700); err != nil {
		t.Fatal(err)
	}
	meta, _ := os.ReadFile("testdata/claude/session/subagents/agent-fg1.meta.json")
	kid, _ := os.ReadFile("testdata/claude/session/subagents/agent-fg1.jsonl")
	kidLines := strings.SplitAfter(string(kid), "\n")
	write(t, filepath.Join(subs, "agent-fg1.meta.json"), string(meta))
	write(t, filepath.Join(subs, "agent-fg1.jsonl"), kidLines[0]+kidLines[1])

	feeds := make(chan Feed, 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	Watch(ctx, model.ProviderClaude, path, func(f Feed) { feeds <- f })
	wait := func(what string, ok func(Feed) bool) Feed {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case f := <-feeds:
				if ok(f) {
					return f
				}
			case <-deadline:
				t.Fatalf("no feed with %s", what)
			}
		}
	}
	wait("an empty feed for a missing file", func(f Feed) bool { return f.Provider == model.ProviderClaude && f.Turns == nil })

	// Up to the foreground spawn, then half of the next line.
	write(t, path, strings.Join(lines[:11], "")+lines[11][:20])
	f := wait("a running foreground subagent", func(f Feed) bool {
		return len(f.Subagents) == 1 && len(f.Subagents[0].Calls) == 1 && f.Subagents[0].Running()
	})
	if f.Subagents[0].Calls[0].Arg != "Login(" || len(f.Turns[0].Calls) != 4 {
		t.Fatalf("feed = %+v", f)
	}
	appendFile(t, filepath.Join(subs, "agent-fg1.jsonl"), kidLines[2]+kidLines[3])
	wait("the subagent's reply", func(f Feed) bool { return len(f.Subagents) == 1 && f.Subagents[0].Latest == "Found 3 callers." })

	appendFile(t, path, lines[11][20:]+strings.Join(lines[12:], ""))
	wait("the whole session", func(f Feed) bool { return len(f.Turns) == 2 && len(f.Subagents) == 2 })

	write(t, path, lines[1])
	wait("a fresh read after truncation", func(f Feed) bool { return len(f.Turns) == 1 && f.Turns[0].Calls == nil && f.Subagents == nil })

	cancel()
	time.Sleep(20 * time.Millisecond)
	appendFile(t, path, lines[3])
	time.Sleep(20 * time.Millisecond)
	select {
	case f := <-feeds:
		t.Fatalf("feed after cancel: %+v", f)
	default:
	}
}

// The first read takes the file's last 8 MiB, from the first whole line;
// each later poll reads at most 8 MiB more.
func TestReadLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.jsonl")
	filler := func(n int) string {
		var b strings.Builder
		for b.Len() < n {
			b.WriteString(`{"type":"filler","pad":"` + strings.Repeat("x", 100) + `"}` + "\n")
		}
		return b.String()
	}
	prompt := func(s string) string {
		return `{"type":"user","timestamp":"2026-10-05T10:00:00Z","message":{"content":"` + s + `"}}` + "\n"
	}
	write(t, path, prompt("too early")+filler(maxRead+1000)+prompt("kept"))
	s := newSession(model.ProviderClaude, path, false)
	s.poll()
	if f := s.b.feed(); len(f.Turns) != 1 || f.Turns[0].Prompt != "kept" {
		t.Fatalf("turns = %+v", f.Turns)
	}
	appendFile(t, path, filler(maxRead+1000)+prompt("later"))
	s.poll()
	if n := len(s.b.feed().Turns); n != 1 {
		t.Fatalf("one poll read past 8 MiB: %d turns", n)
	}
	s.poll()
	if f := s.b.feed(); len(f.Turns) != 2 || f.Turns[1].Prompt != "later" {
		t.Fatalf("turns = %+v", f.Turns)
	}
}

// A partial line past 4 MiB is dropped, and reading resumes after its newline.
func TestLongLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, path, "")
	s := newSession(model.ProviderClaude, path, false)
	for range 3 {
		appendFile(t, path, strings.Repeat("y", 2<<20))
		s.poll()
		if len(s.t.part) > maxLine {
			t.Fatalf("holding %d bytes of one line", len(s.t.part))
		}
	}
	appendFile(t, path, "end of it\n"+`{"type":"user","timestamp":"2026-10-05T10:00:00Z","message":{"content":"next"}}`+"\n")
	s.poll()
	if f := s.b.feed(); len(f.Turns) != 1 || f.Turns[0].Prompt != "next" {
		t.Fatalf("turns = %+v", f.Turns)
	}
}

// Only the last maxTurns turns are kept, with no call ids of the dropped ones.
func TestTurnLimit(t *testing.T) {
	b := newBuilder(model.ProviderPi)
	for i := range maxTurns + 10 {
		b.prompt(at(0, 0), "p"+strconv.Itoa(i))
		b.call(at(0, 0), "c"+strconv.Itoa(i), "bash", "")
	}
	f := b.feed()
	if len(f.Turns) != maxTurns || f.Turns[0].Prompt != "p10" || len(b.calls) != maxTurns {
		t.Fatalf("%d turns from %q, %d call ids", len(f.Turns), f.Turns[0].Prompt, len(b.calls))
	}
	if b.result("c0", true) || !b.result("c59", true) || !b.feed().Turns[maxTurns-1].Calls[0].Failed {
		t.Fatal("results of dropped and kept calls")
	}
}

// Garbage, wrong types and unknown entries are skipped.
func TestBadInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	write(t, path, "not json\n{}\n[1,2]\n{\"type\":\"user\",\"message\":{\"content\":5}}\n{\"type\":\"assistant\",\"message\":{\"content\":\"x\"}}\n"+
		"{\"type\":\"response_item\",\"payload\":{\"type\":\"function_call_output\",\"call_id\":\"nope\",\"output\":{\"success\":false}}}\n"+
		"{\"type\":\"message\",\"message\":{\"role\":\"toolResult\",\"toolCallId\":\"nope\"}}\n")
	for _, p := range []model.Provider{model.ProviderClaude, model.ProviderCodex, model.ProviderPi} {
		read(t, p, path)
	}
}

// A result that comes after its turn ended updates only the call; agent
// activity after an aborted turn starts a turn without a prompt.
func TestLateResults(t *testing.T) {
	for _, c := range []struct {
		provider model.Provider
		lines    string
	}{
		{model.ProviderClaude, `{"type":"user","timestamp":"2026-10-05T10:00:00Z","message":{"content":"one"}}
{"type":"assistant","timestamp":"2026-10-05T10:00:01Z","message":{"content":[{"type":"tool_use","id":"a","name":"Bash","input":{}}]}}
{"type":"system","subtype":"turn_duration","timestamp":"2026-10-05T10:00:02Z"}
{"type":"user","timestamp":"2026-10-05T10:00:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"ok"}]}}
{"type":"user","timestamp":"2026-10-05T10:01:00Z","message":{"content":"two"}}
{"type":"assistant","timestamp":"2026-10-05T10:01:01Z","message":{"content":[{"type":"tool_use","id":"b","name":"Bash","input":{}}]}}
{"type":"user","timestamp":"2026-10-05T10:01:02Z","message":{"content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}
{"type":"user","timestamp":"2026-10-05T10:01:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"b","content":"x","is_error":true}]}}
{"type":"assistant","timestamp":"2026-10-05T10:01:04Z","message":{"content":[{"type":"text","text":"later"}],"stop_reason":"end_turn"}}
`},
		{model.ProviderCodex, `{"timestamp":"2026-10-05T10:00:00Z","type":"event_msg","payload":{"type":"task_started"}}
{"timestamp":"2026-10-05T10:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"one"}]}}
{"timestamp":"2026-10-05T10:00:01Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"a","arguments":"{}"}}
{"timestamp":"2026-10-05T10:00:02Z","type":"event_msg","payload":{"type":"task_complete"}}
{"timestamp":"2026-10-05T10:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"a","output":"ok"}}
{"timestamp":"2026-10-05T10:01:00Z","type":"event_msg","payload":{"type":"task_started"}}
{"timestamp":"2026-10-05T10:01:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"two"}]}}
{"timestamp":"2026-10-05T10:01:01Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"b","arguments":"{}"}}
{"timestamp":"2026-10-05T10:01:02Z","type":"event_msg","payload":{"type":"turn_aborted"}}
{"timestamp":"2026-10-05T10:01:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"b","output":"x"}}
{"timestamp":"2026-10-05T10:01:04Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"later"}]}}
{"timestamp":"2026-10-05T10:01:04Z","type":"event_msg","payload":{"type":"task_complete"}}
`},
		{model.ProviderPi, `{"type":"message","timestamp":"2026-10-05T10:00:00Z","message":{"role":"user","content":"one"}}
{"type":"message","timestamp":"2026-10-05T10:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"a","name":"bash","arguments":{}}],"stopReason":"toolUse"}}
{"type":"message","timestamp":"2026-10-05T10:00:02Z","message":{"role":"toolResult","toolCallId":"a"}}
{"type":"message","timestamp":"2026-10-05T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stopReason":"stop"}}
{"type":"message","timestamp":"2026-10-05T10:00:03Z","message":{"role":"toolResult","toolCallId":"a"}}
{"type":"message","timestamp":"2026-10-05T10:01:00Z","message":{"role":"user","content":"two"}}
{"type":"message","timestamp":"2026-10-05T10:01:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"b","name":"bash","arguments":{}}],"stopReason":"toolUse"}}
{"type":"message","timestamp":"2026-10-05T10:01:02Z","message":{"role":"assistant","content":[],"stopReason":"aborted"}}
{"type":"message","timestamp":"2026-10-05T10:01:03Z","message":{"role":"toolResult","toolCallId":"b","isError":true}}
{"type":"message","timestamp":"2026-10-05T10:01:04Z","message":{"role":"assistant","content":[{"type":"text","text":"later"}],"stopReason":"stop"}}
`},
	} {
		path := filepath.Join(t.TempDir(), "s.jsonl")
		write(t, path, c.lines)
		f := read(t, c.provider, path)
		if len(f.Turns) != 3 {
			t.Errorf("%s: turns = %+v", c.provider, f.Turns)
			continue
		}
		one, two, three := f.Turns[0], f.Turns[1], f.Turns[2]
		if !one.End.Equal(at(0, 2)) || one.Calls[0].Running {
			t.Errorf("%s: turn one = %+v", c.provider, one)
		}
		if !two.End.Equal(at(1, 2)) || !two.Calls[0].Failed || two.Calls[0].Running {
			t.Errorf("%s: turn two = %+v", c.provider, two)
		}
		if three.Prompt != "" || three.Reply != "later" || !three.Start.Equal(at(1, 4)) {
			t.Errorf("%s: turn three = %+v", c.provider, three)
		}
	}
}
