package agent

import (
	"regexp"
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

// Adapted from tuios (MIT): internal/harness/manifests/claude-code.toml
// The spinner line above Claude's prompt box, as "✢ Nesting… (9s · ↓ 232
// tokens)". A finished turn's "✻ Worked for 5s" has no ellipsis.
var spinner = regexp.MustCompile(`^\s*[*·✢✳✶✻✽]\s+\S.*…(?:\s+\(\d+[smh]|\s*$)`)

// ReadScreen classifies the bottom of a Claude Code or Codex screen. A form
// outranks a spinner, as an approval box can sit under a running turn.
//
// Claude Code 2.1.287 hides its spinner while a reply streams, so ScreenNone
// does not prove a turn ended; the caller also checks the screen is still.
func ReadScreen(g vt.Grid) Screen {
	lines := bottomLines(g, screenLines)
	busy := false
	for _, l := range lines {
		low := strings.ToLower(l)
		if strings.Contains(low, "esc to cancel") || strings.Contains(low, "do you want to proceed") || strings.Contains(low, "would you like to proceed") {
			return ScreenForm
		}
		busy = busy || strings.Contains(low, "esc to interrupt") || spinner.MatchString(l)
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
func State(g vt.Grid) model.AgentState {
	switch ReadScreen(g) {
	case ScreenBusy:
		return model.StateWorking
	case ScreenNone:
		return ""
	}
	for _, l := range bottomLines(g, screenLines) {
		low := strings.ToLower(l)
		switch {
		case strings.Contains(low, "would you like to proceed"):
			return model.StatePlanReady
		// Claude's AskUserQuestion and MCP input forms, Codex's question form.
		case strings.Contains(low, "enter to select"), strings.Contains(low, "requests your input"), strings.Contains(low, "enter to submit"):
			return model.StateAwaitingInput
		}
	}
	return model.StatePendingApproval
}

// screenText is what marks each state on an agent's screen, as lowercase
// substrings of its bottom rows.
type screenText struct {
	plan, input, approval, busy []string
}

// screens are the agents whose screens StateOf reads by their text rather
// than ReadScreen's Claude and Codex rules. Cursor CLI and Amp are missing:
// no source or docs give their screens' text.
var screens = map[model.Provider]screenText{
	// Gemini CLI's confirmation queue titles, its tool approval options,
	// and its loading line, "(esc to cancel, 5s)".
	model.ProviderGemini: {
		plan:     []string{"ready to start implementation?"},
		input:    []string{"answer questions"},
		approval: []string{"allow once", "action required", "waiting for user confirmation"},
		busy:     []string{"esc to cancel"},
	},
	// OpenCode's question dialog footer, plan_exit's question header, its
	// permission prompt, and the prompt's "esc interrupt" while a run goes.
	model.ProviderOpenCode: {
		plan:     []string{"build agent"},
		input:    []string{"esc dismiss"},
		approval: []string{"permission required"},
		busy:     []string{"esc interrupt", "again to interrupt"},
	},
	// aider's confirm_ask, "Add file to the chat? (Y)es/(N)o [Yes]:", and
	// its spinner while the model answers, "Waiting for <model>".
	model.ProviderAider: {
		input: []string{"(y)es/(n)o"},
		busy:  []string{"waiting for "},
	},
}

// StateOf is State for agent p: Claude's, Codex's and pi's screens by
// State's rules, the agents in screens by their text, and "" for any other.
func StateOf(p model.Provider, g vt.Grid) model.AgentState {
	s, ok := screens[p]
	if !ok {
		if p == model.ProviderClaude || p == model.ProviderCodex || p == model.ProviderPi {
			return State(g)
		}
		return ""
	}
	lines := bottomLines(g, screenLines)
	for _, m := range []struct {
		subs  []string
		state model.AgentState
	}{
		{s.plan, model.StatePlanReady},
		{s.input, model.StateAwaitingInput},
		{s.approval, model.StatePendingApproval},
		{s.busy, model.StateWorking},
	} {
		for _, l := range lines {
			low := strings.ToLower(l)
			for _, sub := range m.subs {
				if strings.Contains(low, sub) {
					return m.state
				}
			}
		}
	}
	return ""
}

// ScreenRules reports whether StateOf reads agent p by the text in
// screens, which a decision model reading p's screen replaces.
func ScreenRules(p model.Provider) bool {
	_, ok := screens[p]
	return ok
}

// bottomLines returns up to n non-empty rows of g, bottom row first.
func bottomLines(g vt.Grid, n int) []string {
	var out []string
	for y := g.Rows - 1; y >= 0 && len(out) < n; y-- {
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
		if l := strings.TrimRight(b.String(), " "); l != "" {
			out = append(out, l)
		}
	}
	return out
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
