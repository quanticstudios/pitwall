package panel

import (
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
)

// toolKind is one kind of call in a work log's header, as Claude Code
// counts its collapsed calls: "read 2 files", "ran 3 commands".
type toolKind struct{ verb, one, many string }

var (
	kindRead   = toolKind{"read", "file", "files"}
	kindRun    = toolKind{"ran", "command", "commands"}
	kindSearch = toolKind{"searched for", "pattern", "patterns"}
	kindEdit   = toolKind{"edited", "file", "files"}
	kindFetch  = toolKind{"fetched", "page", "pages"}
	kindWeb    = toolKind{"ran", "web search", "web searches"}
	kindAgent  = toolKind{"started", "agent", "agents"}
	kindOther  = toolKind{"used", "other tool", "other tools"}
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

// toolKinds is what the calls in content[lo:hi] did, by kind in the order
// each kind first came: "Read 2 files, ran 3 commands". The work log's
// own rows leave out task lists, and so does this. At most three kinds
// show; the rest count as other tools. "" when there is no call.
func toolKinds(content []session.Item, lo, hi int) string {
	var order []toolKind
	n := map[toolKind]int{}
	for i := lo; i < hi; i++ {
		c := content[i].Tool
		if content[i].Kind != session.KindToolCall || c == nil || isTodo(c) {
			continue
		}
		k := kindOf(c.ToolName)
		if n[k] == 0 && k != kindOther {
			order = append(order, k)
		}
		n[k]++
	}
	if len(order) > 3 {
		for _, k := range order[3:] {
			n[kindOther] += n[k]
		}
		order = order[:3]
	}
	if n[kindOther] > 0 {
		order = append(order, kindOther)
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		noun := k.many
		if n[k] == 1 {
			noun = k.one
		}
		parts = append(parts, k.verb+" "+strconv.Itoa(n[k])+" "+noun)
	}
	s := strings.Join(parts, ", ")
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
