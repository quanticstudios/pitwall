package decide

import (
	"encoding/json"
	"math"
	"strings"
)

// The questions pitwall asks and the states it sends, one pair per
// feature. Text is cut so state plus question stays far below Jev's 32k
// token budget.

// Approval verdicts.
const (
	Allow = "allow"
	Ask   = "ask"
	Deny  = "deny"
)

// ApprovalQuestions ask whether a tool call is safe to run unattended.
func ApprovalQuestions() map[string]Question {
	return map[string]Question{"verdict": {
		Type: Choice,
		Instructions: "A coding agent wants to run `tool` with `input` in the project at `cwd`, while working on `user_prompt`. " +
			"Should this call run without asking the developer?",
		Criteria: map[string]string{
			Allow: "Routine and safe for this task: reads, builds, tests, formatting, edits inside the project that the prompt asks for.",
			Ask:   "Unclear, unusual, or with effects the developer should see first: network access, installs, deletes, anything outside the project.",
			Deny:  "Clearly destructive, exfiltrates data or secrets, or has nothing to do with the task.",
		},
	}}
}

// ApprovalState is what an approval question is asked about.
func ApprovalState(agentName string, c Call, prompt string) map[string]any {
	var input any = clip(string(c.Input), 6000)
	var v any
	if json.Unmarshal(c.Input, &v) == nil {
		input = clipValue(v, 6000)
	}
	return map[string]any{
		"agent":       agentName,
		"tool":        c.Tool,
		"input":       input,
		"cwd":         c.Cwd,
		"repo_root":   c.Root,
		"user_prompt": clip(prompt, 2000),
	}
}

// Approval reads the verdict answer: the choice and the probability of
// each verdict.
func Approval(ans map[string]Answer) (string, map[string]float64) {
	a := ans["verdict"]
	return a.Choice, a.Probabilities
}

// AutoVerdict is what auto mode does with an answer: Deny when p(deny)
// reaches denyAbove, Allow when p(allow) reaches allowAbove and no hard
// rule was broken, else "" (no decision: the agent asks as usual).
func AutoVerdict(probs map[string]float64, rules []string, allowAbove, denyAbove float64) string {
	switch {
	case probs[Deny] >= denyAbove:
		return Deny
	case len(rules) == 0 && probs[Allow] >= allowAbove:
		return Allow
	}
	return ""
}

// Urgency levels, lowest first, as triage returns them.
var Urgencies = []string{"fyi", "later", "soon", "now"}

// TriageQuestions rate how soon the developer should look at a pane.
func TriageQuestions() map[string]Question {
	return map[string]Question{"urgency": {
		Type:         Score,
		Instructions: "A coding agent stopped with `state` and showed `text`. How soon should the developer look at it?",
		Criteria: []string{
			"fyi: nothing to do; a status note or a finished turn that went fine",
			"later: needs the developer eventually, nothing waits on it",
			"soon: the agent waits on an answer or approval for routine work",
			"now: blocked on something risky, an error that stops the work, or a decision only the developer can make",
		},
	}}
}

// TriageState is what triage is asked about.
func TriageState(agentName, state, text string) map[string]any {
	return map[string]any{"agent": agentName, "state": state, "text": clip(text, 4000)}
}

// Urgency reads the level triage picked.
func Urgency(ans map[string]Answer) string {
	i := int(math.Round(ans["urgency"].Score))
	return Urgencies[max(0, min(i, len(Urgencies)-1))]
}

// Screen states an agent without hooks can be in, as asked.
const (
	ScreenWorking  = "working"
	ScreenWaiting  = "waiting for input"
	ScreenApproval = "asking approval"
	ScreenDone     = "done"
	ScreenIdle     = "idle"
)

// ScreenQuestions ask what an agent's visible screen shows.
func ScreenQuestions() map[string]Question {
	return map[string]Question{"status": {
		Type:         Choice,
		Instructions: "This is the visible terminal screen of the coding agent `agent`. What is the agent doing right now?",
		Criteria: map[string]string{
			ScreenWorking:  "Running: a spinner, streaming output, or a busy indicator",
			ScreenWaiting:  "Asking the developer a question or waiting for an answer it requested",
			ScreenApproval: "Asking permission to run a command or edit a file",
			ScreenDone:     "Just finished a task and shows its result",
			ScreenIdle:     "At its prompt with nothing pending",
		},
	}}
}

// ScreenState is what the screen question is asked about.
func ScreenState(agentName, screen string) map[string]any {
	return map[string]any{"agent": agentName, "screen": clipEnd(screen, 6000)}
}

// Screen reads the status and its confidence.
func Screen(ans map[string]Answer) (string, float64) {
	a := ans["status"]
	return a.Choice, a.Confidence
}

// TurnQuestions ask whether a finished turn needs the developer's review.
func TurnQuestions() map[string]Question {
	return map[string]Question{"review": {
		Type: Noul,
		Instructions: "A coding agent just finished a turn; `last_message` is its final message and `screen` its terminal. " +
			"Does this turn need the developer's review: failed tests, errors left, unfinished work, or a question?",
	}}
}

// TurnState is what the turn check is asked about.
func TurnState(agentName, lastMessage, screen string) map[string]any {
	return map[string]any{"agent": agentName, "last_message": clipEnd(lastMessage, 4000), "screen": clipEnd(screen, 4000)}
}

// Review reads the probability the turn needs review.
func Review(ans map[string]Answer) float64 { return ans["review"].Noul }

// clip cuts s to its first n runes.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// clipEnd cuts s to its last n runes, where a screen's latest output is.
func clipEnd(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

// clipValue clips every string in v.
func clipValue(v any, n int) any {
	switch t := v.(type) {
	case string:
		return clip(t, n)
	case map[string]any:
		for k, x := range t {
			t[k] = clipValue(x, n)
		}
	case []any:
		for i, x := range t {
			t[i] = clipValue(x, n)
		}
	}
	return v
}

// Summary is a short line about a call for the audit trail: the command,
// the file, or the start of the input, with secrets removed.
func Summary(c Call, secrets ...string) string {
	var in map[string]any
	_ = json.Unmarshal(c.Input, &in)
	s := string(c.Input)
	for _, k := range []string{"command", "file_path", "notebook_path", "path", "url"} {
		if v, ok := in[k].(string); ok && v != "" {
			s = v
			break
		}
	}
	return clip(strings.Join(strings.Fields(Redact(s, secrets...)), " "), 160)
}
