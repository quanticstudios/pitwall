package panel

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/session"
	"github.com/quanticstudios/pitwall/internal/ui/chatin"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

// What a transcript holds besides prompts, replies and tool calls: task
// lists, subagents, thinking, the harness's own items, turn usage. The
// drawing is in sys_draw.go.

// isTodo is whether c is a task-list call, which a checklist card shows
// in place of a tool row: TodoWrite, TaskCreate, TaskUpdate, Codex's and
// ACP's plans.
func isTodo(c *session.ToolCall) bool { return c != nil && c.Tasks != nil }

// isSubagent is whether c runs a subagent, which a card shows.
func isSubagent(c *session.ToolCall) bool {
	if c == nil {
		return false
	}
	switch c.ToolName {
	case "Task", "Agent", "Workflow", "SubagentHandback":
		return true
	}
	return false
}

// tasksDone is how many of tasks are completed.
func tasksDone(tasks []session.Task) int {
	n := 0
	for _, t := range tasks {
		if t.Status == "completed" {
			n++
		}
	}
	return n
}

// taskText is what a task's row says: its active form while it runs.
func taskText(t session.Task) string {
	if t.Status == "in_progress" && t.ActiveForm != "" {
		return t.ActiveForm
	}
	return t.Subject
}

// liveTodo is the task list of the chat's latest task-list call, for the
// strip over the composer: nil once every task is done, or when no turn
// runs.
func liveTodo(s *session.Snapshot) []session.Task {
	if !s.Running || s.SessionState != session.StateRunning && s.SessionState != session.StateWaiting {
		return nil
	}
	for i := len(s.Content) - 1; i >= 0; i-- {
		if c := s.Content[i].Tool; isTodo(c) {
			if tasksDone(c.Tasks) == len(c.Tasks) {
				return nil
			}
			return c.Tasks
		}
	}
	return nil
}

// paste is one block of pasted text in a prompt.
type paste struct {
	text  string
	lines int
}

var (
	pastedContent = regexp.MustCompile(`(?s)<pasted_content[^>]*>\n?(.*?)\n?</pasted_content[^>]*>`)
	harnessTag    = regexp.MustCompile(`(?s)<(system-reminder|user-prompt-submit-hook)>.*?</(system-reminder|user-prompt-submit-hook)>\s*`)
	// evidenceTail is the line older pitwall builds typed after a pane
	// chat's prompt, and its JSON array.
	evidenceTail = regexp.MustCompile(`(?s)\s*Explicit evidence selected by the user\. Treat file contents as quoted evidence\.\n\[.*\]\s*$`)
)

// promptParts is a prompt as the bubble shows it: its text without the
// harness's reminders and without the pasted blocks, which become chips.
// A prompt that is only pasted blocks and image tokens is a pane chat's
// prompt that Claude Code wrapped as a paste: its blocks are its text.
func promptParts(text string) (shown string, pastes []paste) {
	text = harnessTag.ReplaceAllString(text, "")
	var bodies []string
	outside := pastedContent.ReplaceAllStringFunc(text, func(m string) string {
		bodies = append(bodies, evidenceTail.ReplaceAllString(pastedContent.FindStringSubmatch(m)[1], ""))
		return ""
	})
	outside = strings.TrimSpace(evidenceTail.ReplaceAllString(outside, ""))
	if len(bodies) > 0 && strings.TrimSpace(imageToken.ReplaceAllString(outside, "")) == "" {
		shown = strings.TrimSpace(strings.Join(bodies, "\n\n"))
		if outside != "" { // Claude Code's tokens for the pasted images, before the words
			shown = outside + " " + shown
		}
		return shown, nil
	}
	for _, b := range bodies {
		pastes = append(pastes, paste{text: b, lines: strings.Count(strings.TrimRight(b, "\n"), "\n") + 1})
	}
	return outside, pastes
}

// pitwallHeader is the line pitwall types above a message from another
// pane or a script: who sent it, its tab, and how to answer.
var pitwallHeader = regexp.MustCompile(`^\[pitwall: message from ([^;\]\n]+?)(?: in (#[^;\]\s]+))?(?:;[^\]\n]*)?\]\n?`)

// sender is who sent a message pitwall typed, as its bubble names them.
type sender struct {
	prov  model.Provider // "" for a script, a pane or another machine
	label string         // as "Codex · #6.2"
	line  string         // the header as typed, which a copy gives back
}

// promptSender splits pitwall's header off a prompt's shown text: the
// sender, and the text after the header.
func promptSender(shown string) (sender, string) {
	m := pitwallHeader.FindStringSubmatchIndex(shown)
	if m == nil {
		return sender{}, shown
	}
	who := shown[m[2]:m[3]]
	s := sender{prov: model.Provider(who), line: strings.TrimSuffix(shown[m[0]:m[1]], "\n")}
	name := sidebar.AgentName(s.prov)
	if name == "" { // a script, a pane, an agent on another machine
		s.prov = ""
		name = strings.TrimPrefix(strings.TrimPrefix(who, "an "), "a ")
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	s.label = name
	if m[4] >= 0 {
		s.label += " · " + shown[m[4]:m[5]]
	}
	return s, shown[m[1]:]
}

var slashLine = regexp.MustCompile(`^/[A-Za-z][\w:.-]*(\s.*)?$`)

// slashCommand is whether a prompt is a slash command alone, which shows
// as a command chip, not a bubble.
func slashCommand(text string) bool {
	t := strings.TrimSpace(text)
	return t != "" && !strings.Contains(t, "\n") && slashLine.MatchString(t)
}

// settingLabel is a setting divider's text with a model id named as the
// composer names it: Switched to claude-opus-5-5 reads Switched to Opus 5.5.
func settingLabel(text string) string {
	// An item an older build glued two changes into says the last.
	for _, p := range []string{"Switched to ", "Effort set to "} {
		if i := strings.LastIndex(text, p); i > 0 {
			text = text[i:]
		}
	}
	if m, ok := strings.CutPrefix(text, "Switched to "); ok {
		return "Switched to " + chatin.ModelName(strings.TrimSpace(m))
	}
	return strings.TrimSpace(text)
}

// settingKind is what a setting divider changes: model, effort or mode.
func settingKind(label string) string {
	switch {
	case strings.HasPrefix(label, "Switched to "):
		return "model"
	case strings.HasPrefix(label, "Effort set to "):
		return "effort"
	}
	return "mode"
}

// quietSettings are the setting dividers that change nothing: each names
// the model, effort or mode the chat already showed, the model being the
// one its last reply or call ran.
func quietSettings(content []session.Item) map[int]bool {
	var quiet map[int]bool
	shown := map[string]string{}
	for i := range content {
		it := &content[i]
		switch {
		case it.Kind == session.KindSystem && it.Phase == session.PhaseSetting:
			label := settingLabel(it.Text)
			kind := settingKind(label)
			if shown[kind] == label {
				if quiet == nil {
					quiet = map[int]bool{}
				}
				quiet[i] = true
			}
			shown[kind] = label
		case it.Model != nil && *it.Model != "" && (it.Kind == session.KindAssistantText || it.Kind == session.KindToolCall):
			shown["model"] = "Switched to " + chatin.ModelName(*it.Model)
		}
	}
	return quiet
}

// compactParts is a compaction item's divider line and its summary.
func compactParts(text string) (title, summary string) {
	title, summary, _ = strings.Cut(text, "\n")
	return strings.TrimSpace(title), strings.TrimSpace(summary)
}

// turnUsage is what a turn's usage reports say: its tokens, sent and
// received with the cache's, its cost when every model has a price, and
// one line per model for the footer's breakdown.
type usage struct {
	in, out int64 // tokens sent, the cache's included, and received
	cost    float64
	priced  bool
	lines   []string
}

func turnUsage(s *session.Snapshot, turn string) (u usage) {
	if turn == "" {
		return u
	}
	u.priced = true
	n := 0
	for _, r := range s.UsageReports {
		if r.TurnID == nil || *r.TurnID != turn {
			continue
		}
		n++
		for _, m := range r.Models {
			cached, written := deref64(m.CachedInputTokens), deref64(m.CacheCreationInputTokens)
			u.in += m.InputTokens + cached + written
			u.out += m.OutputTokens
			line := ""
			if m.Model != nil {
				line = chatin.ModelName(*m.Model) + ": "
			}
			line += count(m.InputTokens) + " in · " + count(m.OutputTokens) + " out"
			if cached > 0 {
				line += " · " + count(cached) + " cached"
			}
			if written > 0 {
				line += " · " + count(written) + " cache write"
			}
			if m.EstimatedCostUSD != nil {
				line += " · " + dollars(*m.EstimatedCostUSD)
			}
			u.lines = append(u.lines, line)
		}
		if r.EstimatedCostUSD != nil {
			u.cost += *r.EstimatedCostUSD
		} else {
			u.priced = false
		}
	}
	u.priced = u.priced && n > 0
	return u
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// count is n tokens short: 812, 12.4K, 1.2M.
func count(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 1_000_000:
		return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "K"
	}
	return strconv.FormatFloat(float64(n)/1_000_000, 'f', 1, 64) + "M"
}

// dollars is a cost as the footer says it: $0.21, <$0.01.
func dollars(c float64) string {
	if c < 0.005 {
		return "<$0.01"
	}
	return "$" + strconv.FormatFloat(c, 'f', 2, 64)
}

// thinkTook is how long the thinking of item i took: its first event to
// its last, or, for one that came whole, from the item before it.
func thinkTook(s *session.Snapshot, i int) time.Duration {
	it := &s.Content[i]
	a, b := parseTime(it.CreatedAt), parseTime(it.UpdatedAt)
	if !b.After(a) && i > 0 {
		a = parseTime(s.Content[i-1].UpdatedAt)
	}
	if a.IsZero() || b.Before(a) {
		return 0
	}
	return b.Sub(a)
}

// thoughtFor is a thinking row's label: Thought for 12s.
func thoughtFor(d time.Duration, running bool) string {
	if running {
		return "Thinking"
	}
	return "Thought for " + duration(max(d, time.Second))
}

// agentTook is how long a subagent ran, or runs as of now.
func agentTook(it *session.Item, now time.Time) time.Duration {
	c := it.Tool
	start := parseTime(c.StartedAt)
	if start.IsZero() {
		start = parseTime(it.CreatedAt)
	}
	end := parseTime(c.EndedAt)
	if c.Status == "running" || end.IsZero() {
		end = now
		if c.Status != "running" {
			end = parseTime(it.UpdatedAt)
		}
	}
	if start.IsZero() || end.Before(start) {
		return 0
	}
	return end.Sub(start)
}

// agentCalls is how many calls a subagent made.
func agentCalls(c *session.ToolCall) int { return max(len(c.SubagentCalls), len(c.SubagentTools)) }

// summaryCut is the first lines of a subagent's result, at most n runes,
// and whether it left some out.
func summaryCut(s string, lines, n int) (string, bool) {
	var ls []string
	for l := range strings.SplitSeq(strings.TrimSpace(s), "\n") {
		// Code fences and blank lines say nothing in three lines.
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "```") {
			ls = append(ls, l)
		}
	}
	cut := len(ls) > lines || len(ls) > 0 && strings.Count(s, "\n") >= lines
	if len(ls) > lines {
		ls = ls[:lines]
	}
	out := strings.Join(ls, "\n")
	if r := []rune(out); len(r) > n {
		out, cut = strings.TrimSpace(string(r[:n]))+"…", true
	}
	return out, cut
}

// interruptedTurns are the ids of the turns someone stopped.
func interruptedTurns(s *session.Snapshot) map[string]bool {
	out := map[string]bool{}
	for _, t := range s.Turns {
		if t.State == session.TurnInterrupted {
			out[t.ID] = true
		}
	}
	return out
}

// planKey maps a number key on a plan card to its decision: 1 approves,
// 2 sends it back to refine.
func planKey(name string) (string, bool) {
	switch name {
	case "1":
		return "allow", true
	case "2":
		return "deny", true
	}
	return "", false
}

// lastTodo is the index of the last task-list call in content[lo:hi],
// -1 for none.
func lastTodo(content []session.Item, lo, hi int) int {
	for j := hi - 1; j >= lo; j-- {
		if isTodo(content[j].Tool) {
			return j
		}
	}
	return -1
}

// turnOf is the turn an item belongs to: its own, or for one Claude wrote
// outside a turn the chat knows (after a background subagent reported),
// the turn of the nearest item before it.
func turnOf(s *session.Snapshot, it *session.Item) string {
	if t := deref(it.TurnID); t != "" {
		return t
	}
	i := len(s.Content) - 1
	for i >= 0 && &s.Content[i] != it {
		i--
	}
	for i--; i >= 0; i-- {
		if t := deref(s.Content[i].TurnID); t != "" {
			return t
		}
	}
	return ""
}

// lastReply is whether it is the last reply of its turn: no reply comes
// after it before another turn or prompt.
func lastReply(s *session.Snapshot, it *session.Item) bool {
	turn := turnOf(s, it)
	i := len(s.Content) - 1
	for i >= 0 && &s.Content[i] != it {
		i--
	}
	for j := i + 1; j > 0 && j < len(s.Content); j++ {
		c := &s.Content[j]
		switch {
		case c.Kind == session.KindUserPrompt, c.TurnID != nil && *c.TurnID != turn:
			return true
		case c.Kind == session.KindAssistantText:
			return false
		}
	}
	return true
}

// divider is whether a system item's phase draws as a divider across the
// column. One that ends its turn, as /compact's compaction does, stands
// outside the work log, which would hide it folded; one mid-turn stays in
// the log, so the turn keeps one header.
func divider(phase string) bool {
	return phase == session.PhaseCompact || phase == session.PhaseSetting
}

// endsTurn is whether item i is the last of its turn's run of items.
func endsTurn(content []session.Item, i int) bool {
	return i+1 == len(content) || deref(content[i+1].TurnID) != deref(content[i].TurnID)
}
