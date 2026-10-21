package decide

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// The questions pitwall asks and the states it sends, one pair per
// feature. States hold the full text; Client.Prepare redacts and only
// then cuts it to fit Jev's budget.

// Approval verdicts.
const (
	Allow = "allow"
	Ask   = "ask"
)

// ApprovalQuestions ask about a tool call pitwall saw no risk in, one
// narrow question each, as TypeSafe advises: was it asked for, and is it
// hard to undo.
func ApprovalQuestions() map[string]Question {
	return map[string]Question{
		"requested": {Type: Noul, Instructions: "A coding agent wants to run `tool` with `input` in the project at `cwd`. " +
			"Did the developer ask for this in `user_prompt`, or is it a routine step toward what they asked: reading, building, testing, formatting, or editing files in the project?"},
		"irreversible": {Type: Noul, Instructions: "A coding agent wants to run `tool` with `input` in the project at `cwd`. " +
			"Could it do something hard to undo: delete or overwrite files outside version control, discard uncommitted work, push, publish, deploy, send a message or a payment, or change the system or its settings?"},
	}
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

// Approval reads the verdict: allow when the call was likely asked for
// and likely easy to undo, else ask, with the probability of that verdict.
func Approval(ans map[string]Answer) (verdict string, p float64) {
	allow := ans["requested"].Noul * (1 - ans["irreversible"].Noul)
	if allow >= 0.5 {
		return Allow, allow
	}
	return Ask, 1 - allow
}

// Urgency levels, lowest first. Triage answers fyi, soon or now; later
// is kept so an old answer still ranks.
var Urgencies = []string{"fyi", "later", "soon", "now"}

// TriageQuestions ask two narrow questions about a pane that needs the
// developer: is it blocked on them, and is there nothing for them to do.
func TriageQuestions() map[string]Question {
	return map[string]Question{
		"blocked": {Type: Noul, Instructions: "A coding agent stopped with `state` and showed `text`. " +
			"Is it blocked until the developer acts: a risky action to approve, an error that stops the work, or a decision only the developer can make?"},
		"nothing": {Type: Noul, Instructions: "A coding agent stopped with `state` and showed `text`. " +
			"Is there nothing for the developer to do: a status note, or a finished turn that went fine?"},
	}
}

// TriageState is what triage is asked about.
func TriageState(agentName, state, text string) map[string]any {
	return map[string]any{"agent": agentName, "state": state, "text": text}
}

// Urgency reads the level: now when blocked, fyi when there is nothing to
// do, soon otherwise. Blocked wins when the model says both.
func Urgency(ans map[string]Answer) string {
	switch {
	case ans["blocked"].Noul >= 0.5:
		return "now"
	case ans["nothing"].Noul >= 0.5:
		return "fyi"
	}
	return "soon"
}

// TurnQuestions ask whether a finished turn needs the developer's review.
func TurnQuestions() map[string]Question {
	return map[string]Question{"review": {
		Type: Noul,
		Instructions: "A coding agent just finished a turn; `last_message` is its final message. " +
			"Does this turn need the developer's review: failed tests, errors left, unfinished work, or a question?",
	}}
}

// TurnState is what the turn check is asked about: the turn's last
// message, never the screen.
func TurnState(agentName, lastMessage string) map[string]any {
	return map[string]any{"agent": agentName, "last_message": lastMessage}
}

// Review reads the probability the turn needs review.
func Review(ans map[string]Answer) float64 { return ans["review"].Noul }

var (
	turnRuleRe   = regexp.MustCompile(`(?i)\b(?:fail(?:s|ed|ing|ure)?|errors?|skipped|couldn'?t|could not|unable|not yet|todo|remaining|blocked)\b|\?\s*$`)
	correctionRe = regexp.MustCompile(`(?i)\b(?:still|didn'?t|doesn'?t|don'?t|isn'?t|wasn'?t|not work\w*|broken|wrong|again|revert|undo|forgot|missed|fix|fails?|failing|error)\b`)
)

// TurnRule is the rule the turn check must beat: a last message that
// names a failure, an error, a skip or work left, or ends on a question.
func TurnRule(lastMessage string) bool { return turnRuleRe.MatchString(lastMessage) }

// Correction reports a prompt that reads as the developer correcting the
// agent's last turn ("still fails", "that's wrong", "fix ..."). It is
// the report's label for the turn check: a weak one, since a new task
// can say "fix" too.
func Correction(prompt string) bool { return correctionRe.MatchString(prompt) }

// checkRe finds a command that checks the agent's work: hooks, tests,
// linters, type checks, builds and commits, which run pre-commit hooks.
var checkRe = regexp.MustCompile(`(?:^|[\s;&|(])(?:pre-commit|lefthook|git commit|go (?:test|vet|build)|golangci-lint|(?:npm|pnpm|yarn|bun)(?: run)? (?:test|lint|check|typecheck|build)|npx (?:tsc|eslint|vitest|jest|playwright)|tsc|eslint|vitest|jest|pytest|tox|ruff|mypy|cargo (?:test|clippy|check|build)|make|just|gradle|mvn|mix test|rspec|phpunit)\b`)

// IsCheck reports whether a shell command runs a check.
func IsCheck(command string) bool { return checkRe.MatchString(command) }

// CheckQuestions ask whether a check that exited 0 still shows a problem.
func CheckQuestions() map[string]Question {
	return map[string]Question{"failed": {Type: Noul, Instructions: "A coding agent ran `command` to check its work. It exited 0 and printed `output` (the end of it). " +
		"Does the output still show a problem: failures or errors, tests skipped or none found, hooks bypassed (--no-verify, SKIP=), or errors hidden (|| true)?"}}
}

// CheckState is what the check question is asked about.
func CheckState(agentName, command, output string) map[string]any {
	return map[string]any{"agent": agentName, "command": command, "output": output}
}

// CheckFailed reads the probability the check still shows a problem.
func CheckFailed(ans map[string]Answer) float64 { return ans["failed"].Noul }

// StuckAfter is how long an agent works without stopping before it is
// asked about, and how long between two asks.
const StuckAfter = 10 * time.Minute

// StuckQuestions ask whether a long-running agent is stuck.
func StuckQuestions() map[string]Question {
	return map[string]Question{"stuck": {Type: Noul, Instructions: "This is the visible terminal screen of the coding agent `agent`, which has worked for `minutes` minutes without stopping. " +
		"Is it stuck: repeating the same action or error, retrying a failing command over and over, waiting on something that will not finish, or making no visible progress?"}}
}

// StuckState is what the stuck question is asked about.
func StuckState(agentName string, minutes int, screen string) map[string]any {
	return map[string]any{"agent": agentName, "minutes": minutes, "screen": screen}
}

// Stuck reads the probability the agent is stuck.
func Stuck(ans map[string]Answer) float64 { return ans["stuck"].Noul }

// Budget for one state, in runes, after redaction: about 12k tokens,
// well under Jev's 32k for the state and the longest question.
const (
	stateBudget = 48000
	stringMax   = 8000
)

// endKeys are state fields whose latest text is at the end.
var endKeys = map[string]bool{"screen": true, "last_message": true, "output": true}

// ErrTooLarge is a state that does not fit the budget even with every
// string cut short.
var ErrTooLarge = errors.New("state too large to send")

// Prepare redacts state, turns it into plain JSON values (so every map
// and slice type is cut alike), and only then cuts it to the budget,
// measured as the JSON that is sent: each string to stringMax runes, then
// all of them shorter until the whole fits. truncated reports any cut; a
// state that still does not fit is ErrTooLarge and is not sent.
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

// size is the length in runes of v as the JSON that is sent.
func size(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return stateBudget + 1
	}
	return utf8.RuneCount(b)
}

// clip cuts s to its first n runes, at a word boundary when there is one.
func clip(s string, n int) string {
	cut := false
	return clipAll(s, "", n, &cut).(string)
}
