package codex

import (
	"cmp"
	"encoding/json"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// toolNames are the item types that show as tool calls, with aide's names.
var toolNames = map[string]string{
	"commandExecution": "Shell",
	"fileChange":       "Edit",
	"mcpToolCall":      "Tool",
	"webSearch":        "Web search",
}

// deltaKinds are the streaming notifications and the content they add to.
var deltaKinds = map[string]session.ItemKind{
	"item/agentMessage/delta":           session.KindAssistantText,
	"item/reasoning/textDelta":          session.KindReasoningText,
	"item/reasoning/summaryTextDelta":   session.KindReasoningSummaryText,
	"item/commandExecution/outputDelta": session.KindCommandOutput,
	"item/fileChange/outputDelta":       session.KindFileChangeOutput,
}

// notify turns one server notification into events, as aide's
// CodexManagedSessionBackend.handleNotification does.
func (d *driver) notify(method string, params json.RawMessage) {
	if kind, ok := deltaKinds[method]; ok {
		d.delta(params, kind)
		return
	}
	switch method {
	case "thread/status/changed":
		var n struct {
			Status struct {
				Type        string
				ActiveFlags []string
			}
		}
		json.Unmarshal(params, &n)
		e := session.Event{Type: session.EventState}
		switch flags := strings.Join(n.Status.ActiveFlags, " "); {
		case n.Status.Type == "idle":
			e.State = session.StateReady
		case n.Status.Type == "systemError":
			e.State = session.StateError
		case n.Status.Type != "active":
			return // notLoaded
		case strings.Contains(flags, "waitingOnApproval"):
			e.State, e.Detail = session.StateWaiting, "Codex is waiting for approval."
		case strings.Contains(flags, "waitingOnUserInput"):
			e.State, e.Detail = session.StateWaiting, "Codex is waiting for input."
		default:
			e.State = session.StateRunning
		}
		d.post(e)

	case "turn/started":
		var n struct{ Turn struct{ ID string } }
		json.Unmarshal(params, &n)
		d.mu.Lock()
		d.turn = n.Turn.ID
		d.mu.Unlock()
		d.post(session.Event{Type: session.EventTurnStarted, TurnID: n.Turn.ID})

	case "turn/completed":
		var n struct {
			Turn struct {
				ID, Status string
				Error      *struct{ Message string }
			}
		}
		json.Unmarshal(params, &n)
		e := session.Event{Type: session.EventTurnCompleted, TurnID: n.Turn.ID, State: session.TurnCompleted, Point: n.Turn.ID}
		switch n.Turn.Status {
		case "failed":
			e.State = session.TurnFailed
			if n.Turn.Error != nil {
				e.ErrorMessage = n.Turn.Error.Message
			}
		case "interrupted":
			e.State = session.TurnInterrupted
		}
		d.mu.Lock()
		d.turn = ""
		d.mu.Unlock()
		clear(d.tools)
		clear(d.phases)
		clear(d.streamed)
		d.post(e)

	case "item/started", "item/completed":
		var n struct{ Item json.RawMessage }
		json.Unmarshal(params, &n)
		d.item(n.Item, method == "item/completed")

	case "thread/tokenUsage/updated":
		d.usage(params)

	case "account/rateLimits/updated":
		d.limit(params)

	case "error":
		var n struct {
			Error     struct{ Message string }
			WillRetry bool
		}
		json.Unmarshal(params, &n)
		msg := cmp.Or(n.Error.Message, "Codex session failed.")
		if n.WillRetry {
			// Codex retries on its own; an error event would end the turn.
			d.post(session.Event{Type: session.EventState, State: session.StateRunning, Detail: msg})
			return
		}
		d.post(session.Event{Type: session.EventError, Message: msg})

	case "thread/started", "turn/diff/updated", "item/reasoning/summaryPartAdded":
		// Nothing to show.

	default:
		logUnknown(d, method)
	}
}

func (d *driver) currentModel() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.model
}

// item handles an item starting or ending: tool items become tool calls,
// a finished agent message its text unless deltas already streamed it.
func (d *driver) item(raw json.RawMessage, completed bool) {
	var it struct {
		ID, Type, Phase, Text, Command, Query, Status, AggregatedOutput, Server, Tool string
		ExitCode                                                                      *int
		Changes                                                                       []struct {
			Path string
			Kind struct{ Type string }
		}
	}
	if json.Unmarshal(raw, &it) != nil || it.ID == "" {
		return
	}
	if name, ok := toolNames[it.Type]; ok {
		c := d.tools[it.ID]
		if c == nil {
			c = &session.ToolCall{CallID: it.ID}
			d.tools[it.ID] = c
		}
		c.Input = raw
		c.ToolName = name
		if mcp := strings.Trim(it.Server+"."+it.Tool, "."); it.Type == "mcpToolCall" && mcp != "" {
			c.ToolName = mcp
		}
		c.Status = "running"
		if completed {
			c.Status = "completed"
			if it.Status == "failed" || it.Status == "declined" || it.ExitCode != nil && *it.ExitCode != 0 {
				c.Status = "failed"
			}
		}
		c.Command = cmp.Or(it.Command, it.Query, c.Command)
		if it.ExitCode != nil {
			c.ExitCode = it.ExitCode
		}
		if len(it.Changes) > 0 {
			c.Files = nil
			for _, ch := range it.Changes {
				c.Files = append(c.Files, session.ToolFile{Path: ch.Path, Status: cmp.Or(ch.Kind.Type, "modified")})
			}
		}
		if c.Output == "" {
			c.Output = it.AggregatedOutput
		}
		d.postTool(c)
		return
	}
	if it.Type != "agentMessage" {
		return
	}
	if it.Phase == "commentary" || it.Phase == "final_answer" {
		d.phases[it.ID] = it.Phase
	}
	if !completed {
		return
	}
	text := it.Text
	if d.streamed[it.ID] {
		text = ""
	}
	d.post(session.Event{Type: session.EventContent, ItemID: it.ID, Kind: session.KindAssistantText, Phase: d.phases[it.ID], Model: d.currentModel(), Text: text})
}

func (d *driver) postTool(c *session.ToolCall) {
	t := *c
	d.post(session.Event{Type: session.EventContent, ItemID: c.CallID, Kind: session.KindToolCall, Model: d.currentModel(), Tool: &t})
}

// delta appends streamed text: to a tool call's output when the item is
// one, else to the text item of kind.
func (d *driver) delta(params json.RawMessage, kind session.ItemKind) {
	var n struct{ ItemID, Delta string }
	if json.Unmarshal(params, &n) != nil || n.ItemID == "" || n.Delta == "" {
		return
	}
	if c := d.tools[n.ItemID]; c != nil {
		c.Output += n.Delta
		d.postTool(c)
		return
	}
	if kind == session.KindAssistantText {
		d.streamed[n.ItemID] = true
	}
	d.post(session.Event{Type: session.EventContent, ItemID: n.ItemID, Kind: kind, Phase: d.phases[n.ItemID], Model: d.currentModel(), Text: n.Delta})
}

// usage is aide's parseCodexUsage: thread totals, and the last request's
// size as the context.
func (d *driver) usage(params json.RawMessage) {
	type breakdown struct {
		InputTokens, OutputTokens, CachedInputTokens, TotalTokens int64
		CacheWriteInputTokens                                     *int64
	}
	var n struct {
		ThreadID, TurnID string
		TokenUsage       struct {
			Total, Last        breakdown
			ModelContextWindow *int64
		}
	}
	if json.Unmarshal(params, &n) != nil || n.TurnID == "" {
		return
	}
	d.mu.Lock()
	thread, model := d.thread, d.model
	d.mu.Unlock()
	if n.ThreadID != thread {
		return
	}
	total, at := n.TokenUsage.Total, session.Stamp(now())
	fresh := total.InputTokens - total.CachedInputTokens
	if cw := total.CacheWriteInputTokens; cw != nil {
		fresh -= *cw
	}
	m := session.ModelUsage{
		InputTokens:              fresh,
		OutputTokens:             total.OutputTokens,
		CachedInputTokens:        &total.CachedInputTokens,
		CacheCreationInputTokens: total.CacheWriteInputTokens,
	}
	if model != "" {
		m.Model = &model
	}
	e := session.Event{
		Type:              session.EventUsage,
		CreatedAt:         at,
		InputTokens:       total.InputTokens,
		OutputTokens:      total.OutputTokens,
		CachedInputTokens: &total.CachedInputTokens,
		ContextTokens:     &n.TokenUsage.Last.TotalTokens,
		Report: &session.UsageReport{
			ID:        n.TurnID,
			ScopeID:   thread,
			Provider:  "codex",
			TurnID:    &n.TurnID,
			CreatedAt: at,
			Models:    []session.ModelUsage{m},
		},
	}
	if w := n.TokenUsage.ModelContextWindow; w != nil && *w > 0 {
		e.MaxTokens = w
	}
	d.post(e)
}

// limit reports the account's rate limit windows. A rejected limit resets
// when the last full window does.
func (d *driver) limit(params json.RawMessage) {
	type window struct {
		UsedPercent        float64
		WindowDurationMins int64
		ResetsAt           int64 // unix seconds
	}
	var n struct {
		RateLimits struct {
			Primary, Secondary   *window
			RateLimitReachedType *string
		}
	}
	if json.Unmarshal(params, &n) != nil {
		return
	}
	l := &session.Limit{Rejected: n.RateLimits.RateLimitReachedType != nil, Windows: []session.LimitWindow{}}
	for _, w := range []*window{n.RateLimits.Primary, n.RateLimits.Secondary} {
		if w == nil {
			continue
		}
		lw := session.LimitWindow{Minutes: w.WindowDurationMins, Used: w.UsedPercent}
		if w.ResetsAt > 0 {
			lw.Resets = time.Unix(w.ResetsAt, 0).UTC()
		}
		if l.Rejected && w.UsedPercent >= 100 && lw.Resets.After(l.Resets) {
			l.Resets = lw.Resets
		}
		l.Windows = append(l.Windows, lw)
	}
	d.post(session.Event{Type: session.EventLimit, Limit: l})
}
