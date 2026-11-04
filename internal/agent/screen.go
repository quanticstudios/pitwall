package agent

import (
	"strings"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// Screen is what the bottom of an agent's screen says about its turn.
type Screen int

const (
	ScreenNone Screen = iota // nothing either way: idle, or streaming text without a spinner
	ScreenBusy               // a spinner line or an interrupt hint: a turn is running
	ScreenForm               // a menu or prompt that waits for an answer
)

// screenLines is how many non-empty rows from the bottom ReadScreen reads:
// the prompt box, its footer, the spinner and a short todo list above it.
const screenLines = 12

// ReadScreen classifies the bottom of a Claude Code or Codex screen. A form
// outranks a spinner, as an approval box can sit under a running turn.
//
// Claude Code 2.1.287 hides its spinner while a reply streams, so ScreenNone
// does not prove a turn ended; the caller also checks the screen is still.
func ReadScreen(g vt.Grid) Screen { return activePatterns.Load().readScreen(g, nil) }

// why: schema 1 has no whole-row list, so these rows are matched in code.
func (trace *ScreenTrace) emptyPlan(kind, line, low string) bool {
	low = strings.TrimSpace(low)
	for _, pattern := range []string{"exit plan mode?", "claude wants to exit plan mode"} {
		if trace.check(kind+"/"+pattern, pattern, line, low == pattern) {
			return true
		}
	}
	return false
}

func (p *screenPatterns) readScreen(g vt.Grid, trace *ScreenTrace) Screen {
	lines := bottomLines(g, screenLines)
	busy := false
	for _, l := range lines {
		low := strings.ToLower(l)
		if trace.containsAny("form", p.Common.Form, l, low) || trace.emptyPlan("form", l, low) {
			return ScreenForm
		}
		busy = busy || trace.containsAny("busy", p.Common.Busy, l, low)
		for _, re := range p.spinner {
			busy = busy || trace.check("busy/spinner", re.String(), l, re.MatchString(l))
		}
	}
	if busy {
		return ScreenBusy
	}
	return ScreenNone
}

// State is what an agent's screen says when no hook reports for it: working,
// pending-approval, plan-ready or awaiting-input. It is "" for a screen with
// none of them, which is an idle prompt or a reply streaming without a
// spinner; the caller tells those apart by whether the screen still moves.
func State(g vt.Grid) model.AgentState { return activePatterns.Load().state(g, nil) }

func (p *screenPatterns) state(g vt.Grid, trace *ScreenTrace) model.AgentState {
	switch p.readScreen(g, trace) {
	case ScreenBusy:
		trace.decide("busy")
		return model.StateWorking
	case ScreenNone:
		return ""
	}
	for _, l := range bottomLines(g, screenLines) {
		low := strings.ToLower(l)
		switch {
		case trace.containsAny("plan", p.Common.Plan, l, low) || trace.emptyPlan("plan", l, low):
			trace.decide("plan")
			return model.StatePlanReady
		// Claude's AskUserQuestion and MCP input forms, Codex's question form.
		case trace.containsAny("input", p.Common.Input, l, low):
			trace.decide("input")
			return model.StateAwaitingInput
		}
	}
	trace.decide("form")
	return model.StatePendingApproval
}

// StateOf is State for Claude, Codex and pi, provider-specific text rules
// for other supported agents, and "" for an agent without rules.
func StateOf(p model.Provider, g vt.Grid) model.AgentState {
	return activePatterns.Load().stateOf(p, g, nil)
}

func (rules *screenPatterns) stateOf(p model.Provider, g vt.Grid, trace *ScreenTrace) model.AgentState {
	s, ok := rules.Providers[p]
	if !ok {
		if p == model.ProviderClaude || p == model.ProviderCodex || p == model.ProviderPi {
			return rules.state(g, trace)
		}
		if trace != nil {
			trace.Rule = "no-screen-rules"
		}
		return ""
	}
	lines := bottomLines(g, screenLines)
	for _, m := range []struct {
		subs  []string
		state model.AgentState
	}{
		{s.Plan, model.StatePlanReady},
		{s.Input, model.StateAwaitingInput},
		{s.Approval, model.StatePendingApproval},
		{s.Busy, model.StateWorking},
	} {
		for _, l := range lines {
			low := strings.ToLower(l)
			for _, sub := range m.subs {
				if trace.contains(string(m.state), sub, l, low) {
					trace.decide(string(m.state))
					return m.state
				}
			}
		}
	}
	return ""
}

// bottomLines returns up to n non-empty rows of g, bottom row first.
func bottomLines(g vt.Grid, n int) []string {
	var out []string
	for y := g.Rows - 1; y >= 0 && len(out) < n; y-- {
		if l := rowText(g, y); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// rowText is row y of g without its trailing spaces.
func rowText(g vt.Grid, y int) string {
	var b strings.Builder
	for x := 0; x < g.Cols; x++ {
		c := g.At(x, y)
		switch {
		case c.Width == 0:
		case c.Content == "":
			b.WriteByte(' ')
		default:
			b.WriteString(c.Content)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// ScreenText is the last n non-empty rows of g, top to bottom, for a
// decision model to read.
func ScreenText(g vt.Grid, n int) string {
	lines := bottomLines(g, n)
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n")
}
