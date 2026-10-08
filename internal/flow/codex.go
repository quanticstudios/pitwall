package flow

import (
	"encoding/json"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// codex reads a Codex rollout: entries {timestamp, type, payload}.
// response_item payloads are the conversation (messages, tool calls and
// their outputs, paired by call_id); event_msg payloads bound turns
// (task_started, task_complete, turn_aborted) and report finished items.
// A subagent writes its own rollout, named by its thread id.
type codex struct {
	path  string
	tasks bool   // task_started was seen: turns are bounded by task events
	model string // the model of the latest turn_context
	total Tokens // the latest token_count's running total
}

type codexEntry struct {
	Timestamp string       `json:"timestamp"`
	Type      string       `json:"type"`
	Payload   codexPayload `json:"payload"`
}

type codexPayload struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Input     string          `json:"input"`
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`
	Action    struct {
		Command []string `json:"command"`
	} `json:"action"`
	Model            string     `json:"model"`
	Info             *codexInfo `json:"info"`
	Author           string     `json:"author"`
	LastAgentMessage string     `json:"last_agent_message"`
	Item             struct {
		Type          string `json:"type"`
		ID            string `json:"id"`
		Status        string `json:"status"`
		Kind          string `json:"kind"`
		AgentPath     string `json:"agent_path"`
		AgentThreadID string `json:"agent_thread_id"`
	} `json:"item"`
}

// codexInfo is a token_count event's: running totals for the thread, the
// latest call's counts, and the model's context window.
type codexInfo struct {
	Total  codexTokens `json:"total_token_usage"`
	Last   codexTokens `json:"last_token_usage"`
	Window int64       `json:"model_context_window"`
}

// codexTokens counts input with its cached part in it, and output with its
// reasoning.
type codexTokens struct {
	Input  int64 `json:"input_tokens"`
	Cached int64 `json:"cached_input_tokens"`
	Write  int64 `json:"cache_write_input_tokens"`
	Output int64 `json:"output_tokens"`
}

func (t codexTokens) tokens() Tokens {
	return Tokens{Input: t.Input - t.Cached - t.Write, Output: t.Output, CacheRead: t.Cached, CacheWrite: t.Write}
}

func (c *codex) line(b *builder, line []byte) {
	var e codexEntry
	if json.Unmarshal(line, &e) != nil {
		return
	}
	ts, p := parseTime(e.Timestamp), e.Payload
	switch e.Type + "/" + p.Type {
	case "event_msg/task_started":
		c.tasks = true
		// A prompt logged before its task_started already opened the turn.
		if n := len(b.turns); n > 0 && b.turns[n-1].End.IsZero() && len(b.turns[n-1].Calls) == 0 && b.turns[n-1].Reply == "" {
			return
		}
		b.prompt(ts, "")
	case "turn_context/":
		c.model = p.Model
	case "event_msg/token_count":
		if p.Info == nil {
			return
		}
		// Totals can repeat, so the call's tokens are what the total grew
		// by. A total that shrank started over: a forked child's own.
		total := p.Info.Total.tokens()
		d := total.minus(c.total)
		if d.Input < 0 || d.Output < 0 || d.CacheRead < 0 || d.CacheWrite < 0 {
			d = total
		}
		c.total = total
		b.use(ts, c.model, d, p.Info.Last.Input, p.Info.Window)
	case "event_msg/task_complete":
		b.reply(ts, p.LastAgentMessage)
		b.end(ts, false)
	case "event_msg/turn_aborted":
		b.end(ts, true)
	case "event_msg/item_completed":
		c.item(b, ts, p)
	case "response_item/message":
		text := codexText(p.Content)
		switch p.Role {
		case "assistant":
			b.reply(ts, text)
		case "user":
			if injected(text) {
				return
			}
			n := len(b.turns)
			switch {
			case n > 0 && b.turns[n-1].End.IsZero() && b.turns[n-1].Prompt == "":
				b.turns[n-1].Prompt = cut(firstLine(text), 200)
			case n > 0 && b.turns[n-1].End.IsZero() && c.tasks:
				// Input the user sent into the running turn.
			default:
				b.prompt(ts, text)
			}
		}
	case "response_item/function_call":
		args := p.Arguments
		var s string
		if json.Unmarshal(args, &s) == nil {
			args = json.RawMessage(s)
		}
		b.call(ts, p.CallID, p.Name, arg(p.Name, args))
		c.function(b, ts, p, args)
	case "response_item/custom_tool_call":
		a := ""
		switch p.Name {
		case "apply_patch":
			a = patchFile(p.Input)
		case "exec", "exec_command", "shell":
			a = firstLine(p.Input)
		}
		b.call(ts, p.CallID, p.Name, a)
	case "response_item/local_shell_call":
		b.call(ts, p.CallID, "shell", command(p.Action.Command))
	case "response_item/function_call_output", "response_item/custom_tool_call_output":
		b.result(p.CallID, codexFailed(p.Output))
		if s := b.subBy(p.CallID); s != nil {
			var out struct {
				TaskName string `json:"task_name"`
				AgentID  string `json:"agent_id"`
			}
			var text string
			if json.Unmarshal(p.Output, &text) == nil && json.Unmarshal([]byte(text), &out) == nil {
				if out.TaskName != "" {
					s.key = out.TaskName
				}
				if threadID.MatchString(out.AgentID) {
					s.thread = out.AgentID
				}
			}
		}
	case "response_item/agent_message":
		// A subagent's message to its parent, by its agent path.
		if s := b.subBy(path.Base(p.Author)); s != nil && !s.final {
			if t := strings.TrimSpace(codexText(p.Content)); t != "" {
				s.Latest = cut(t, 2000)
			}
		}
	}
}

func (c *codex) function(b *builder, ts time.Time, p codexPayload, args json.RawMessage) {
	switch p.Name {
	case "update_plan":
		var in struct {
			Plan []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
			} `json:"plan"`
		}
		if json.Unmarshal(args, &in) != nil {
			return
		}
		var steps []Step
		for _, s := range in.Plan {
			steps = append(steps, Step{Text: s.Step, State: stepState(s.Status)})
		}
		b.setPlan(steps)
	case "spawn_agent":
		var in struct {
			Message   string `json:"message"`
			TaskName  string `json:"task_name"`
			AgentType string `json:"agent_type"`
		}
		_ = json.Unmarshal(args, &in)
		s := b.spawn(ts, p.CallID, in.TaskName, in.AgentType, in.Message)
		s.key = in.TaskName
	}
}

// item applies a finished item: a subagent's activity, or a failed command
// whose id is a call's.
func (c *codex) item(b *builder, ts time.Time, p codexPayload) {
	it := p.Item
	if it.Type == "SubAgentActivity" {
		s := b.subBy(path.Base(it.AgentPath))
		if s == nil {
			return
		}
		if s.thread == "" && threadID.MatchString(it.AgentThreadID) {
			s.thread = it.AgentThreadID
		}
		if it.Kind == "completed" && s.End.IsZero() {
			s.End = ts
		}
		return
	}
	if it.Status == "failed" {
		b.result(it.ID, true)
	}
}

// resolve finds a subagent's rollout by its thread id, in the parent's
// day directory or the one of the day it was spawned.
func (c *codex) resolve(b *builder) {
	day := filepath.Dir(c.path)
	root := filepath.Dir(filepath.Dir(filepath.Dir(day)))
	for _, s := range b.subs {
		if s.file != "" || s.thread == "" {
			continue
		}
		for _, dir := range []string{day, filepath.Join(root, s.Start.Local().Format("2006/01/02"))} {
			if m, _ := filepath.Glob(filepath.Join(dir, "rollout-*-"+s.thread+".jsonl")); len(m) > 0 {
				s.file = m[0]
				break
			}
		}
	}
}

var threadID = regexp.MustCompile(`^[0-9A-Za-z-]+$`)

// codexText joins a message's text parts.
func codexText(content json.RawMessage) string {
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(content, &parts)
	var texts []string
	for _, p := range parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

var injectedTag = regexp.MustCompile(`^<[A-Za-z_][A-Za-z0-9_-]*[ >]`)

// injected reports a user message Codex adds itself: the environment
// context, AGENTS.md and other tagged instructions, a question's reply.
func injected(text string) bool {
	text = strings.TrimSpace(text)
	return text == "" || injectedTag.MatchString(text) || strings.HasPrefix(text, "# AGENTS.md")
}

var exitLine = regexp.MustCompile(`(?m)^(?:Exit code:|Process exited with code) *(-?\d+)`)

// codexFailed reads a call's output for a failure: success false, a
// nonzero exit_code in older shell outputs' metadata, or an exit code line.
func codexFailed(raw json.RawMessage) bool {
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var o struct {
			Success *bool `json:"success"`
		}
		if json.Unmarshal(raw, &o) == nil && o.Success != nil {
			return !*o.Success
		}
		s = codexText(raw)
	}
	var o struct {
		Metadata struct {
			ExitCode *int `json:"exit_code"`
		} `json:"metadata"`
	}
	if json.Unmarshal([]byte(s), &o) == nil && o.Metadata.ExitCode != nil {
		return *o.Metadata.ExitCode != 0
	}
	if len(s) > 1000 {
		s = s[:1000]
	}
	m := exitLine.FindStringSubmatch(s)
	return m != nil && m[1] != "0"
}
