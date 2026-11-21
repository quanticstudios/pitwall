package panel

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/session"
)

// toolKind is one kind of call in a fold's header, as T3 Code labels a
// group of them: "Ran 2 commands", "Read 3 files". prio orders the kinds
// the header names: the first two by it, the rest as other actions.
type toolKind struct {
	verb, one, many, icon string
	prio                  int
}

// Adapted from T3 Code (MIT): packages/client-runtime/src/work-log/presentation.ts
var (
	kindRun    = toolKind{"Ran", "command", "commands", "terminal", 0}
	kindEdit   = toolKind{"Changed", "file", "files", "pencil", 0}
	kindAgent  = toolKind{"Started", "agent", "agents", "bot", 0}
	kindRead   = toolKind{"Read", "file", "files", "file", 1}
	kindSearch = toolKind{"Searched code", "time", "times", "search", 1}
	kindWeb    = toolKind{"Searched the web", "time", "times", "globe", 1}
	kindFetch  = toolKind{"Fetched", "page", "pages", "globe", 1}
	kindOther  = toolKind{"Used", "tool", "tools", "wrench", 2}
)

// kindOf is the kind a call of tool counts as.
func kindOf(tool string) toolKind {
	switch tool {
	case "Read", "read", "read_file", "NotebookRead":
		return kindRead
	case "Bash", "bash", "exec_command", "shell_command", "shell", "run_shell_command", "Monitor":
		return kindRun
	case "Grep", "grep", "Glob", "glob", "search_file_content", "grep_search", "LS":
		return kindSearch
	case "Edit", "MultiEdit", "Write", "NotebookEdit", "apply_patch", "edit", "write":
		return kindEdit
	case "WebFetch":
		return kindFetch
	case "WebSearch", "web_search":
		return kindWeb
	}
	if isSubagent(&session.ToolCall{ToolName: tool}) {
		return kindAgent
	}
	return kindOther
}

// foldLabel is what the calls in content[lo:hi] did, as T3 Code sums a
// group: the two kinds first by priority, in the order they came, then
// the rest as other actions, "Ran 2 commands and read 1 file". An edit
// counts its files once each. icon is the kind's when the fold holds one
// kind, else a hammer, as T3 Code marks a mixed group. Task lists are left out, as the fold's
// rows leave them out. A fold of thinking alone is "Thought", "Thought
// (×2)". "" when there is neither.
func foldLabel(content []session.Item, lo, hi int) (label, icon string) {
	var order []toolKind
	n := map[toolKind]int{}
	files := map[string]bool{}
	thoughts, calls := 0, 0
	for i := lo; i < hi; i++ {
		it := &content[i]
		if it.Kind == session.KindReasoningText || it.Kind == session.KindReasoningSummaryText {
			thoughts++
		}
		c := it.Tool
		if it.Kind != session.KindToolCall || c == nil || isTodo(c) {
			continue
		}
		calls++
		k := kindOf(c.ToolName)
		if k == kindEdit {
			if p := editPath(c); p != "" {
				if files[p] {
					continue
				}
				files[p] = true
			}
		}
		if n[k] == 0 {
			order = append(order, k)
		}
		n[k]++
	}
	switch {
	case calls == 0 && thoughts == 1:
		return "Thought", "brain"
	case calls == 0 && thoughts > 1:
		return "Thought (×" + strconv.Itoa(thoughts) + ")", "brain"
	case calls == 0:
		return "", ""
	}
	icon = "hammer"
	if len(order) == 1 {
		icon = order[0].icon
	}
	pick := slices.Clone(order)
	slices.SortStableFunc(pick, func(a, b toolKind) int { return a.prio - b.prio })
	pick = pick[:min(2, len(pick))]
	var parts []string
	rest := 0
	for _, k := range order {
		if !slices.Contains(pick, k) {
			rest += n[k]
			continue
		}
		noun := k.many
		if n[k] == 1 {
			noun = k.one
		}
		parts = append(parts, k.verb+" "+strconv.Itoa(n[k])+" "+noun)
	}
	if rest > 0 {
		parts = append(parts, "Performed "+plural(rest, "other action"))
	}
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToLower(parts[i][:1]) + parts[i][1:]
	}
	switch len(parts) {
	case 1:
		return parts[0], icon
	case 2:
		return parts[0] + " and " + parts[1], icon
	}
	return strings.Join(parts[:len(parts)-1], ", ") + ", and " + parts[len(parts)-1], icon
}

// editPath is the file an edit call changed, "" when it does not say.
func editPath(c *session.ToolCall) string {
	if p := inputMap(c.Input).str("file_path", "path", "notebook_path"); p != "" {
		return p
	}
	if len(c.Files) > 0 {
		return c.Files[0].Path
	}
	return ""
}

// logStatus is the one line a running fold's header shows after its
// counts: its newest running call as its row says it, and how long it has
// run by now, "Bash · go test ./... · 0:12". dir shortens a call's path.
// "" for none. The turn's summaries draw outside the fold, above it.
func logStatus(content []session.Item, lo, hi int, dir string, now time.Time) string {
	for i := hi - 1; i >= lo; i-- {
		if c := content[i].Tool; content[i].Kind == session.KindToolCall && c != nil && c.Status == "running" && !isTodo(c) {
			v := summarize(c, nil, itemTime(&content[i]), now)
			if v.path {
				v.arg = relPath(dir, v.arg)
			}
			s := v.verb
			if a := firstLine(v.arg); a != "" {
				s += " · " + a
			}
			if v.result != "" {
				s += " · " + v.result
			}
			return s
		}
	}
	return ""
}

// itemTime is when it came, zero when it does not say.
func itemTime(it *session.Item) time.Time {
	return parseTime(cmp.Or(it.CreatedAt, it.UpdatedAt))
}
