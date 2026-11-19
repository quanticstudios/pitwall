package panel

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/key"

	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/session"
)

// blockKind is what one row of the transcript's list draws.
type blockKind uint8

const (
	blockPrompt blockKind = iota // a user prompt's bubble
	blockTurn                    // a turn's work log and its reply
	blockSystem                  // a line from pitwall between turns
)

// block is a run of transcript items, Content[lo:hi], drawn as one row.
type block struct {
	kind   blockKind
	lo, hi int
	turn   string // the items' turn id, "" for none
	// reply is the index of the turn's last assistant text after its last
	// tool call, drawn under the work log; -1 for none.
	reply  int
	tools  int  // tool calls in the turn
	failed bool // one of them failed, or an error ended it
	// todo is the turn's last task-list call, its checklist card; -1 for
	// none. todos counts its task-list calls, which the work log leaves out.
	todo, todos int
}

// blocks groups a transcript: each prompt is a bubble, the items after
// it up to the next prompt or a change of turn are one turn, and a
// system line outside a turn stands alone.
func blocks(content []session.Item, out []block) []block {
	out = out[:0]
	for i := range content {
		it := &content[i]
		turn := deref(it.TurnID)
		switch {
		case it.Kind == session.KindUserPrompt:
			out = append(out, block{kind: blockPrompt, lo: i, hi: i + 1, turn: turn, reply: -1})
			continue
		case it.Kind == session.KindSystem && turn == "":
			out = append(out, block{kind: blockSystem, lo: i, hi: i + 1, reply: -1})
			continue
		}
		if n := len(out); n > 0 && out[n-1].kind == blockTurn && out[n-1].hi == i && (turn == "" || out[n-1].turn == "" || turn == out[n-1].turn) {
			b := &out[n-1]
			b.hi = i + 1
			if b.turn == "" {
				b.turn = turn
			}
		} else {
			out = append(out, block{kind: blockTurn, lo: i, hi: i + 1, turn: turn, reply: -1})
		}
	}
	for k := range out {
		b := &out[k]
		if b.kind != blockTurn {
			continue
		}
		b.todo = -1
		for i := b.lo; i < b.hi; i++ {
			switch content[i].Kind {
			case session.KindToolCall:
				b.reply = -1
				b.failed = b.failed || toolOpen(content[i].Tool)
				if isTodo(content[i].Tool) { // its card, not the work log, shows it
					b.todo = i
					b.todos++
				} else {
					b.tools++
				}
			case session.KindAssistantText:
				b.reply = i
			case session.KindSystem:
				b.failed = b.failed || sysError(&content[i])
			}
		}
	}
	return out
}

// logOpen is whether a turn's work log starts open: the running turn
// shows its work, and so does one with a failed call; a finished one
// folds to its header.
func logOpen(running, failed bool) bool { return running || failed }

// toolOpen is whether a tool row starts open: only a failed call does.
func toolOpen(c *session.ToolCall) bool { return c != nil && c.Status == "failed" }

// isOpen is a fold's state: what the user set, else its default.
func isOpen(set map[string]bool, id string, def bool) bool {
	if v, ok := set[id]; ok {
		return v
	}
	return def
}

// imageRef is an "[Image #N]" token in a prompt, s[start:end].
type imageRef struct {
	start, end, n int
}

var imageToken = regexp.MustCompile(`\[Image #([0-9]{1,4})\]`)

// imageRefs finds the [Image #N] tokens in s, in order.
func imageRefs(s string) []imageRef {
	var out []imageRef
	for _, m := range imageToken.FindAllStringSubmatchIndex(s, -1) {
		n, _ := strconv.Atoi(s[m[2]:m[3]])
		out = append(out, imageRef{start: m[0], end: m[1], n: n})
	}
	return out
}

// imageNo is the number a prompt's n-th image shows: that of its n-th
// [Image #N] token, since the composer sends images in token order and
// keeps a removed image's number, else n.
func imageNo(refs []imageRef, n int) int {
	if n >= 1 && n <= len(refs) {
		return refs[n-1].n
	}
	return n
}

// proseChars is about where reply prose stops, in characters.
const proseChars = 96

// proseCap is how wide reply prose draws in a column col wide, for text
// whose average character is avg wide.
func proseCap(col, avg int) int {
	if avg <= 0 {
		return col
	}
	return min(col, proseChars*avg)
}

// keyAction is what a number key or Enter does to the newest request.
type keyAction struct {
	decision string // an approval's answer, "" for none
	option   int    // a question's option picked, 0-based; -1 for none
	submit   bool   // Enter: answer the question
}

// actionFor maps key name to an answer. An approval waiting takes 1, 2
// and 3; else a question takes its option numbers, one more for the
// free-text row, and Enter.
func actionFor(name string, approval bool, options int) (keyAction, bool) {
	no := keyAction{option: -1}
	if approval {
		switch name {
		case "1", "2", "3":
			return keyAction{decision: decisions[name[0]-'1'], option: -1}, true
		}
		return no, false
	}
	if options == 0 {
		return no, false
	}
	if name == string(key.NameReturn) || name == string(key.NameEnter) {
		return keyAction{option: -1, submit: true}, true
	}
	if n, err := strconv.Atoi(name); err == nil && n >= 1 && n <= min(options+1, 9) {
		return keyAction{option: n - 1}, true
	}
	return no, false
}

// recommended is the option a question marks "(Recommended)", -1 for
// none.
func recommended(q session.Question) int {
	for i, o := range q.Options {
		if strings.Contains(strings.ToLower(o.Label), "(recommended)") {
			return i
		}
	}
	return -1
}

// --- tool rows ---

// relPath is p relative to dir when p is under it, else p: the chat's
// own files read as main.go, not their absolute path.
func relPath(dir, p string) string {
	if dir == "" || !filepath.IsAbs(p) {
		return p
	}
	r, err := filepath.Rel(dir, p)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return p
	}
	return r
}

// toolSummary is what a tool row shows.
type toolSummary struct {
	verb, arg string
	icon      string // a sidebar.Icon name
	result    string // right-aligned, "" for none
	add, del  int    // an edit's line counts, shown instead of result
	passed    bool   // a command that exited 0: result starts with passed
	path      bool   // arg is a file path, cut in the middle when long
	kind      string // bash, read, search, edit, other: what its body shows
}

// summarize is c's row. now times a running call from start.
// lines is its diff, for an edit's counts.
func summarize(c *session.ToolCall, lines []diffLine, start, now time.Time) toolSummary {
	in := inputMap(c.Input)
	s := toolSummary{verb: c.ToolName, arg: flow.ToolArg(*c), icon: "settings", kind: "other"}
	file := in.str("file_path", "path", "notebook_path")
	switch c.ToolName {
	case "Bash", "bash", "exec_command", "shell_command", "shell", "run_shell_command", "BashOutput":
		s.verb, s.icon, s.kind = "Bash", "terminal", "bash"
		if c.Command != "" {
			s.arg = firstLine(c.Command)
		}
	case "Read", "read", "read_file", "NotebookRead":
		s.verb, s.icon, s.kind = "Read", "file", "read"
		if file != "" {
			s.arg, s.path = file, true
		}
		if n := countLines(c.Output); n > 0 && c.Status == "completed" {
			s.result = plural(n, "line")
		}
	case "Grep", "Glob", "grep", "find", "glob", "search_file_content", "grep_search":
		s.verb, s.icon, s.kind = "Search", "search", "search"
		if p := in.str("path"); p != "" && s.arg != "" && p != "." {
			s.arg += " in " + p
		}
		if c.Status == "completed" {
			if n := countLines(c.Output); n > 0 {
				s.result = plural(n, "match")
				if n != 1 {
					s.result = strconv.Itoa(n) + " matches"
				}
			} else {
				s.result = "No matches"
			}
		}
	case "Edit", "MultiEdit", "Write", "NotebookEdit", "edit", "write", "write_file", "replace", "apply_patch":
		s.verb, s.icon, s.kind = "Edit", "pencil", "edit"
		if c.ToolName == "Write" || c.ToolName == "write" || c.ToolName == "write_file" {
			s.verb = "Write"
		}
		if file != "" {
			s.arg, s.path = file, true
		}
		for _, l := range lines {
			switch l.kind {
			case '+':
				s.add++
			case '-':
				s.del++
			}
		}
	case "WebFetch", "WebSearch", "web_search", "google_web_search":
		s.icon = "globe"
	case "Task", "Agent":
		s.icon = "bot"
		if d := in.str("description"); d != "" {
			s.arg = d
		}
	case "TodoWrite":
		s.verb, s.icon = "Plan", "check"
		if len(c.Tasks) > 0 {
			s.arg = plural(len(c.Tasks), "task")
		}
	}
	if server, tool, ok := strings.Cut(strings.TrimPrefix(c.ToolName, "mcp__"), "__"); ok && strings.HasPrefix(c.ToolName, "mcp__") {
		s.verb, s.icon = strings.ReplaceAll(tool, "_", " "), "puzzle"
		if s.arg == "" {
			s.arg = server
		}
	}
	if s.arg == "" && len(c.Files) > 0 {
		s.arg = c.Files[0].Path
	}
	took := toolTook(c)
	switch c.Status {
	case "running":
		s.result = ""
		if !start.IsZero() {
			s.result = clock(now.Sub(start))
		}
	case "failed":
		s.add, s.del = 0, 0
		if c.ExitCode != nil {
			s.result = "exit " + strconv.Itoa(*c.ExitCode)
		} else {
			s.result = "failed"
		}
		if took != "" {
			s.result += " · " + took
		}
	default:
		if s.kind == "bash" {
			switch {
			case c.ExitCode != nil && *c.ExitCode != 0:
				s.result = "exit " + strconv.Itoa(*c.ExitCode)
			case c.ExitCode != nil:
				s.passed = true
			}
			if took != "" {
				s.result = strings.TrimSpace(s.result + " " + took)
			}
		}
	}
	return s
}

// toolTook is how long a finished call ran, "" when it did not say.
func toolTook(c *session.ToolCall) string {
	a, errA := time.Parse(time.RFC3339Nano, c.StartedAt)
	b, errB := time.Parse(time.RFC3339Nano, c.EndedAt)
	if errA != nil || errB != nil || b.Sub(a) < 100*time.Millisecond {
		return ""
	}
	return seconds(b.Sub(a))
}

// seconds is d as a tool's duration: 0.8s, 14s, 2m 5s.
func seconds(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return duration(d)
}

type inMap map[string]json.RawMessage

func inputMap(raw json.RawMessage) inMap {
	var m inMap
	_ = json.Unmarshal(raw, &m)
	return m
}

func (m inMap) str(keys ...string) string {
	for _, k := range keys {
		var s string
		if json.Unmarshal(m[k], &s) == nil && s != "" {
			return s
		}
	}
	return ""
}

// countLines is how many non-empty lines s has.
func countLines(s string) int {
	n := 0
	for l := range strings.SplitSeq(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)

// toolOutput is what a call's opened row shows: its output, or its
// progress while it runs and has printed nothing yet.
func toolOutput(c *session.ToolCall) string {
	if out := plainOutput(c.Output); out != "" || c.Status != "running" {
		return out
	}
	return plainOutput(c.Progress)
}

// plainOutput is a tool's output without terminal escapes or trailing
// blank lines.
func plainOutput(s string) string {
	return strings.TrimRight(ansi.ReplaceAllString(strings.ReplaceAll(s, "\r\n", "\n"), ""), "\n ")
}

// tailLines is s's last n lines, and how many it left out.
func tailLines(s string, n int) (string, int) {
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s, 0
	}
	return strings.Join(lines[len(lines)-n:], "\n"), len(lines) - n
}

// diffLine is one line of an edit's hunk. old and new are line numbers,
// 0 when the diff did not say.
type diffLine struct {
	kind     byte // ' ', '+', '-', or '@' for a hunk header
	old, new int
	text     string
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// diffLines is an edit call's change as lines: its unified diff, else
// the old and new text its diff or input carries.
func diffLines(c *session.ToolCall) []diffLine {
	if d := c.Diff; d != "" {
		if old, nw, ok := strings.Cut(d, "\nOld text:\n"); ok && !strings.HasPrefix(old, "@@") {
			o, n, _ := strings.Cut(nw, "\nNew text:\n")
			return lineDiff(o, n)
		}
		return unified(d)
	}
	in := inputMap(c.Input)
	switch {
	case in["old_string"] != nil || in["new_string"] != nil:
		return lineDiff(in.str("old_string"), in.str("new_string"))
	case in["edits"] != nil:
		var edits []struct {
			Old string `json:"old_string"`
			New string `json:"new_string"`
		}
		_ = json.Unmarshal(in["edits"], &edits)
		var out []diffLine
		for _, e := range edits {
			out = append(out, lineDiff(e.Old, e.New)...)
		}
		return out
	case in["content"] != nil:
		return lineDiff("", in.str("content"))
	case in["input"] != nil || in["patch"] != nil:
		return unified(in.str("input", "patch"))
	}
	return nil
}

// unified reads a unified diff or an apply_patch body: hunk headers give
// the line numbers when present.
func unified(d string) []diffLine {
	var out []diffLine
	old, nw := 0, 0
	for _, l := range strings.Split(strings.TrimRight(d, "\n"), "\n") {
		switch {
		case strings.HasPrefix(l, "@@"):
			if m := hunkHeader.FindStringSubmatch(l); m != nil {
				old, _ = strconv.Atoi(m[1])
				nw, _ = strconv.Atoi(m[2])
			}
			out = append(out, diffLine{kind: '@', text: l})
		case strings.HasPrefix(l, "---"), strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "***"),
			strings.HasPrefix(l, "diff "), strings.HasPrefix(l, "index "), strings.HasPrefix(l, `\`):
		case strings.HasPrefix(l, "+"):
			out = append(out, diffLine{kind: '+', new: nw, text: l[1:]})
			nw = next(nw)
		case strings.HasPrefix(l, "-"):
			out = append(out, diffLine{kind: '-', old: old, text: l[1:]})
			old = next(old)
		case strings.HasPrefix(l, " "):
			out = append(out, diffLine{kind: ' ', old: old, new: nw, text: l[1:]})
			old, nw = next(old), next(nw)
		}
	}
	return out
}

func next(n int) int {
	if n == 0 {
		return 0
	}
	return n + 1
}

// lineDiff is the change from a to b line by line, without line numbers:
// the text alone does not say where in the file it is.
func lineDiff(a, b string) []diffLine {
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimRight(s, "\n"), "\n")
	}
	x, y := split(a), split(b)
	// shortcut: a quadratic LCS, fine for an edit's few hundred lines; past
	// that the change shows as all removed then all added.
	if len(x)*len(y) > 250_000 {
		var out []diffLine
		for _, l := range x {
			out = append(out, diffLine{kind: '-', text: l})
		}
		for _, l := range y {
			out = append(out, diffLine{kind: '+', text: l})
		}
		return out
	}
	lcs := make([][]int32, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int32, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []diffLine
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			out = append(out, diffLine{kind: ' ', text: x[i]})
			i, j = i+1, j+1
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			out = append(out, diffLine{kind: '-', text: x[i]})
			i++
		default:
			out = append(out, diffLine{kind: '+', text: y[j]})
			j++
		}
	}
	return out
}

// unifiedText is lines as a unified diff to paste into a review.
func unifiedText(path string, lines []diffLine) string {
	var b strings.Builder
	if path != "" {
		b.WriteString("--- a/" + path + "\n+++ b/" + path + "\n")
	}
	for _, l := range lines {
		if l.kind == '@' {
			b.WriteString(l.text + "\n")
			continue
		}
		b.WriteByte(l.kind)
		b.WriteString(l.text + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// --- turns ---

// turnTimes are when a turn started and ended, from its summary or its
// items; end is zero while it runs. Without a summary the last turn runs
// while the chat does.
func turnTimes(s *session.Snapshot, b block, last bool) (start, end time.Time, running bool) {
	for i := range s.Turns {
		t := &s.Turns[i]
		if t.ID != b.turn || b.turn == "" {
			continue
		}
		start = parseTime(t.StartedAt)
		if t.CompletedAt != nil {
			end = parseTime(*t.CompletedAt)
		}
		return start, end, t.State == session.TurnRunning
	}
	start = parseTime(s.Content[b.lo].CreatedAt)
	if start.IsZero() {
		start = parseTime(s.Content[b.lo].UpdatedAt)
	}
	running = last && s.SessionState == session.StateRunning
	if !running {
		end = parseTime(s.Content[b.hi-1].UpdatedAt)
	}
	return start, end, running
}

// runningTurns tells, as turnTimes does, whether a block's turn runs,
// from one pass over the turns instead of one per block.
func runningTurns(s *session.Snapshot) func(b block, last bool) bool {
	known := make(map[string]bool, len(s.Turns))
	for i := range s.Turns {
		if _, ok := known[s.Turns[i].ID]; !ok {
			known[s.Turns[i].ID] = s.Turns[i].State == session.TurnRunning
		}
	}
	return func(b block, last bool) bool {
		if r, ok := known[b.turn]; ok && b.turn != "" {
			return r
		}
		return last && s.SessionState == session.StateRunning
	}
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// logTitle is a work log's header: Worked for 48s · 4 tools, or Working
// for 21s while it runs.
func logTitle(took time.Duration, tools int, running bool) string {
	verb := "Worked for "
	if running {
		verb = "Working for "
	}
	t := verb + duration(took)
	if took < time.Second {
		t = strings.TrimSuffix(verb, " for ")
	}
	if tools > 0 {
		t += " · " + plural(tools, "tool")
	}
	return t
}

// turnTokens is the tokens the turn's usage reports count, 0 for none.
func turnTokens(s *session.Snapshot, turn string) int64 {
	var n int64
	for _, r := range s.UsageReports {
		if turn == "" || r.TurnID == nil || *r.TurnID != turn {
			continue
		}
		for _, m := range r.Models {
			n += m.InputTokens + m.OutputTokens
		}
	}
	return n
}

// tokens is n as the log header says it: 812 tokens, 12.4K tokens.
func tokens(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10) + " tokens"
	}
	return strconv.FormatFloat(float64(n)/1000, 'f', 1, 64) + "K tokens"
}

// middleCut shortens a path to about room pixels, for characters about
// charW wide, by cutting its middle: the start and the file name stay.
func middleCut(s string, room, charW int) string {
	r := []rune(s)
	fit := room / max(charW, 1)
	if len(r) <= fit || fit < 5 {
		return s
	}
	keep := fit - 1
	tail := keep * 3 / 5
	return string(r[:keep-tail]) + "…" + string(r[len(r)-tail:])
}
