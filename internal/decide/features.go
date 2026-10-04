package decide

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
)

// The questions pitwall asks and the states it sends, one pair per
// feature. States hold the full text; Client.Prepare redacts and only
// then cuts it to fit Jev's budget.

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
	var input any = string(c.Input)
	var v any
	if json.Unmarshal(c.Input, &v) == nil {
		input = v
	}
	return map[string]any{
		"agent":       agentName,
		"tool":        c.Tool,
		"input":       input,
		"cwd":         c.Cwd,
		"repo_root":   c.Root,
		"user_prompt": prompt,
	}
}

// Approval reads the verdict answer: the choice and the probability of
// each verdict.
func Approval(ans map[string]Answer) (string, map[string]float64) {
	a := ans["verdict"]
	return a.Choice, a.Probabilities
}

// AutoVerdict is what auto mode does with an answer: Deny when the model
// chose deny with p(deny) at denyAbove or more; Allow when the model chose
// allow with p(allow) at allowAbove or more and canAllow (the call was
// fully checked, broke no rule and was sent whole); else "" (no decision:
// the agent asks as usual). Deny needs no check: it is the safe direction.
func AutoVerdict(choice string, probs map[string]float64, canAllow bool, allowAbove, denyAbove float64) string {
	switch {
	case choice == Deny && probs[Deny] >= denyAbove:
		return Deny
	case canAllow && choice == Allow && probs[Allow] >= allowAbove:
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
	return map[string]any{"agent": agentName, "state": state, "text": text}
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
	return map[string]any{"agent": agentName, "screen": screen}
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
	return map[string]any{"agent": agentName, "last_message": lastMessage, "screen": screen}
}

// Review reads the probability the turn needs review.
func Review(ans map[string]Answer) float64 { return ans["review"].Noul }

// Budget for one state, in runes, after redaction: about 12k tokens,
// well under Jev's 32k for the state and the longest question.
const (
	stateBudget = 48000
	stringMax   = 8000
)

// endKeys are state fields whose latest text is at the end.
var endKeys = map[string]bool{"screen": true, "last_message": true}

// ErrTooLarge is a state that does not fit the budget even with every
// string cut short.
var ErrTooLarge = errors.New("state too large to send")

// Prepare redacts state, turns it into plain JSON values (so every map
// and slice type is counted and cut alike), and only then cuts it to the
// budget: each string to stringMax runes, then all of them shorter until
// the whole fits. truncated reports any cut; a state that still does not
// fit is ErrTooLarge and must not be sent. An approval with a cut or
// refused state is never approved automatically.
func (c *Client) Prepare(state any) (out any, truncated bool, err error) {
	red := RedactValue(state, c.secrets()...)
	b, err := json.Marshal(red)
	if err != nil {
		return nil, true, err
	}
	var norm any
	if err := json.Unmarshal(b, &norm); err != nil {
		return nil, true, err
	}
	for limit := stringMax; limit >= 16; limit /= 2 {
		cut := false
		out = clipAll(norm, "", limit, &cut)
		truncated = truncated || cut
		if size(out) <= stateBudget {
			return out, truncated, nil
		}
		truncated = true
	}
	return nil, true, ErrTooLarge
}

func (c *Client) secrets() []string {
	if c == nil {
		return nil
	}
	return c.Secrets
}

// clipAll copies v with every string cut to limit runes at a word
// boundary, so no token is split, keeping the end for endKeys.
func clipAll(v any, key string, limit int, cut *bool) any {
	switch t := v.(type) {
	case string:
		r := []rune(t)
		if len(r) <= limit {
			return t
		}
		*cut = true
		if endKeys[key] {
			s := string(r[len(r)-limit:])
			if i := strings.IndexAny(s, " \n\t"); i >= 0 {
				s = s[i+1:]
			} else {
				s = ""
			}
			return "…" + s
		}
		s := string(r[:limit])
		if i := strings.LastIndexAny(s, " \n\t"); i > 0 {
			s = s[:i]
		} else {
			s = "" // one long token: none of it, so no half key
		}
		return s + "…"
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = clipAll(x, k, limit, cut)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = clipAll(x, key, limit, cut)
		}
		return out
	}
	return v
}

// size is the runes of every string and key in v.
func size(v any) int {
	switch t := v.(type) {
	case string:
		return len([]rune(t))
	case map[string]any:
		n := 0
		for k, x := range t {
			n += len(k) + size(x)
		}
		return n
	case []any:
		n := 0
		for _, x := range t {
			n += size(x)
		}
		return n
	}
	return 8
}

// clip cuts s to its first n runes, at a word boundary when there is one.
func clip(s string, n int) string {
	cut := false
	return clipAll(s, "", n, &cut).(string)
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
