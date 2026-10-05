package flow

import (
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// claude reads a Claude Code transcript. Every entry has a type and a
// timestamp; user and assistant entries carry a message whose content is a
// string or a list of blocks. A subagent writes its own transcript under
// <transcript without .jsonl>/subagents/, with a meta.json naming the
// toolUseId of the Agent call that spawned it.
type claude struct {
	path    string
	child   bool                  // a subagent's file, whose entries are all sidechain
	metas   map[string]claudeMeta // by meta.json file name
	tasks   map[string]int        // a TaskCreate call's id to its plan index, until its result
	created int                   // TaskCreate calls since the Task tools took the plan
}

type claudeMeta struct {
	ToolUseID string `json:"toolUseId"`
	AgentType string `json:"agentType"`
}

type claudeEntry struct {
	Type             string `json:"type"`
	Subtype          string `json:"subtype"`
	Timestamp        string `json:"timestamp"`
	IsMeta           bool   `json:"isMeta"`
	IsSidechain      bool   `json:"isSidechain"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Origin           *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		Content    json.RawMessage `json:"content"`
		StopReason string          `json:"stop_reason"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   bool            `json:"is_error"`
	Content   json.RawMessage `json:"content"`
}

func (c *claude) line(b *builder, line []byte) {
	var e claudeEntry
	if json.Unmarshal(line, &e) != nil || e.IsSidechain && !c.child {
		return
	}
	ts := parseTime(e.Timestamp)
	switch e.Type {
	case "user":
		c.user(b, e)
	case "assistant":
		var blocks []claudeBlock
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			return
		}
		for _, bl := range blocks {
			switch bl.Type {
			case "text":
				b.reply(ts, bl.Text)
			case "tool_use":
				b.call(ts, bl.ID, bl.Name, arg(bl.Name, bl.Input))
				c.toolUse(b, e, bl)
			}
		}
		b.active(ts)
		if (e.Message.StopReason == "end_turn" || e.Message.StopReason == "stop_sequence") && !b.pending() {
			b.end(ts, false)
		}
	case "system":
		if e.Subtype == "turn_duration" {
			b.end(ts, false)
		}
	}
}

func (c *claude) user(b *builder, e claudeEntry) {
	ts := parseTime(e.Timestamp)
	var text string
	if json.Unmarshal(e.Message.Content, &text) != nil {
		var blocks []claudeBlock
		if json.Unmarshal(e.Message.Content, &blocks) != nil {
			return
		}
		for _, bl := range blocks {
			switch bl.Type {
			case "tool_result":
				b.result(bl.ToolUseID, bl.IsError)
				c.toolResult(b, e, bl)
				if c := b.callByID(bl.ToolUseID); c != nil && c.Tool == "SubagentHandback" {
					b.end(ts, false)
				}
			case "text":
				if text == "" {
					text = bl.Text
				}
			}
		}
		if text == "" || slices.ContainsFunc(blocks, func(bl claudeBlock) bool { return bl.Type == "tool_result" }) {
			return
		}
	}
	trimmed := strings.TrimSpace(text)
	switch {
	case e.IsMeta || e.IsCompactSummary:
	case e.Origin != nil && e.Origin.Kind == "task-notification" || strings.HasPrefix(trimmed, "<task-notification>"):
		c.notification(b, ts, trimmed)
	case strings.HasPrefix(trimmed, "[Request interrupted by user"):
		b.end(ts, true)
	case strings.HasPrefix(trimmed, "<command-"), strings.HasPrefix(trimmed, "<local-command-"), strings.HasPrefix(trimmed, "<bash-"):
		// A slash command's echo or a ! shell command's, not a prompt.
	case e.Origin != nil && e.Origin.Kind != "human":
		// A message from another agent or the harness.
	default:
		b.prompt(ts, text)
	}
}

func (c *claude) toolUse(b *builder, e claudeEntry, bl claudeBlock) {
	ts := parseTime(e.Timestamp)
	switch bl.Name {
	case "SubagentHandback":
		// A subagent's last call: its report is the reply.
		var in struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(bl.Input, &in)
		b.reply(ts, in.Message)
	case "Agent", "Task":
		var in struct {
			Description string `json:"description"`
			Type        string `json:"subagent_type"`
			Prompt      string `json:"prompt"`
		}
		_ = json.Unmarshal(bl.Input, &in)
		b.spawn(ts, bl.ID, in.Description, in.Type, in.Prompt)
	case "TodoWrite":
		var in struct {
			Todos []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"todos"`
		}
		if json.Unmarshal(bl.Input, &in) != nil {
			return
		}
		var steps []Step
		for _, t := range in.Todos {
			steps = append(steps, Step{Text: t.Content, State: stepState(t.Status)})
		}
		b.setPlan(steps)
	case "TaskCreate":
		var in struct {
			Subject string `json:"subject"`
		}
		if json.Unmarshal(bl.Input, &in) != nil {
			return
		}
		// The Task tools own the plan from their first call after a TodoWrite.
		if b.planIDs == nil {
			b.plan, b.planIDs, c.created = []Step{}, []string{}, 0
		}
		// why: Claude Code numbers a task list from 1; the result names the
		// real id when it comes.
		c.created++
		b.plan = append(b.plan, Step{Text: in.Subject})
		b.planIDs = append(b.planIDs, strconv.Itoa(c.created))
		c.taskCalls()[bl.ID] = len(b.plan) - 1
	case "TaskUpdate":
		var in struct {
			ID      json.RawMessage `json:"taskId"`
			Status  string          `json:"status"`
			Subject string          `json:"subject"`
		}
		if json.Unmarshal(bl.Input, &in) != nil {
			return
		}
		i := slices.Index(b.planIDs, idString(in.ID))
		if i < 0 || i >= len(b.plan) {
			return
		}
		switch {
		case in.Status == "deleted":
			b.plan, b.planIDs = slices.Delete(b.plan, i, i+1), slices.Delete(b.planIDs, i, i+1)
			return
		case in.Status != "":
			b.plan[i].State = stepState(in.Status)
		}
		if in.Subject != "" {
			b.plan[i].Text = in.Subject
		}
	}
}

func (c *claude) taskCalls() map[string]int {
	if c.tasks == nil {
		c.tasks = map[string]int{}
	}
	return c.tasks
}

func (c *claude) toolResult(b *builder, e claudeEntry, bl claudeBlock) {
	ts := parseTime(e.Timestamp)
	if i, ok := c.tasks[bl.ToolUseID]; ok {
		delete(c.tasks, bl.ToolUseID)
		var r struct {
			Task struct {
				ID json.RawMessage `json:"id"`
			} `json:"task"`
		}
		if json.Unmarshal(e.ToolUseResult, &r) == nil && idString(r.Task.ID) != "" && i < len(b.planIDs) {
			b.planIDs[i] = idString(r.Task.ID)
		}
		return
	}
	s := b.subBy(bl.ToolUseID)
	if s == nil {
		return
	}
	var r struct {
		AgentID string        `json:"agentId"`
		Status  string        `json:"status"`
		Content []claudeBlock `json:"content"`
	}
	_ = json.Unmarshal(e.ToolUseResult, &r)
	if r.AgentID != "" {
		s.key = r.AgentID
		s.file = filepath.Join(strings.TrimSuffix(c.path, ".jsonl"), "subagents", "agent-"+r.AgentID+".jsonl")
	}
	// An async agent's result only says it launched; it ends later.
	if r.Status == "async_launched" && !bl.IsError {
		return
	}
	s.End, s.Failed, s.final = ts, bl.IsError, true
	text := blocksText(r.Content)
	if text == "" {
		var blocks []claudeBlock
		if json.Unmarshal(bl.Content, &blocks) == nil {
			text = blocksText(blocks)
		} else {
			_ = json.Unmarshal(bl.Content, &text)
		}
	}
	s.Latest = cut(text, 2000)
}

var notifyTag = regexp.MustCompile(`(?s)<(tool-use-id|task-id|status|result)>(.*?)</(?:tool-use-id|task-id|status|result)>`)

// notification ends the background subagent a task notification is about.
func (c *claude) notification(b *builder, ts time.Time, text string) {
	tags := map[string]string{}
	for _, m := range notifyTag.FindAllStringSubmatch(text, -1) {
		if _, ok := tags[m[1]]; !ok {
			tags[m[1]] = m[2]
		}
	}
	s := b.subBy(strings.TrimSpace(tags["tool-use-id"]))
	if s == nil {
		s = b.subBy(strings.TrimSpace(tags["task-id"]))
	}
	status := strings.TrimSpace(tags["status"])
	if s == nil || s.final || status == "" {
		return
	}
	s.End, s.Failed, s.final = ts, status == "failed", true
	if r := strings.TrimSpace(tags["result"]); r != "" {
		s.Latest = cut(r, 2000)
	} else if s.kid != nil {
		s.Latest = s.kid.b.last
	}
}

// resolve finds a running foreground subagent's file by the toolUseId in
// its meta.json; the Agent call's result, which names it too, comes only
// when it is done.
func (c *claude) resolve(b *builder) {
	dir := filepath.Join(strings.TrimSuffix(c.path, ".jsonl"), "subagents")
	names, _ := filepath.Glob(filepath.Join(dir, "agent-*.meta.json"))
	if c.metas == nil {
		c.metas = map[string]claudeMeta{}
	}
	for _, name := range names {
		m, ok := c.metas[name]
		if !ok {
			f, _, err := openRegular(name)
			if err != nil {
				continue
			}
			data, err := io.ReadAll(io.LimitReader(f, 64<<10))
			f.Close()
			if err != nil || json.Unmarshal(data, &m) != nil {
				continue
			}
			c.metas[name] = m
		}
		s := b.subBy(m.ToolUseID)
		if s == nil || s.file != "" {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(name), "agent-"), ".meta.json")
		s.key, s.file = id, filepath.Join(dir, "agent-"+id+".jsonl")
		if s.Type == "" {
			s.Type = m.AgentType
		}
	}
}

func blocksText(blocks []claudeBlock) string {
	var parts []string
	for _, bl := range blocks {
		if bl.Type == "text" && bl.Text != "" {
			parts = append(parts, bl.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// idString is a JSON string or number as a string.
func idString(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}
