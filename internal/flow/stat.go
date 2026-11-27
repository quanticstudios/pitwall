package flow

import (
	"encoding/json"
	"strings"

	managed "github.com/quanticstudios/pitwall/internal/session"
)

// The lines an edit call adds and removes, for the phone's diff card.
// The desktop counts the same way from the same input (panel's
// diffLines): old and new text line by line, a whole file's lines, or a
// patch's + and - lines.

// editStat is the lines an edit tool's input adds and removes, 0 and 0
// for another tool.
func editStat(tool string, input json.RawMessage) (add, del int) {
	var in map[string]json.RawMessage
	if json.Unmarshal(input, &in) != nil {
		return 0, 0
	}
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(in[k], &s)
		return s
	}
	switch {
	case in["old_string"] != nil || in["new_string"] != nil:
		return lineStat(str("old_string"), str("new_string"))
	case in["edits"] != nil:
		var edits []struct {
			Old string `json:"old_string"`
			New string `json:"new_string"`
		}
		_ = json.Unmarshal(in["edits"], &edits)
		for _, e := range edits {
			a, d := lineStat(e.Old, e.New)
			add, del = add+a, del+d
		}
		return add, del
	case in["content"] != nil && filePath(tool, input) != "":
		return lineStat("", str("content"))
	case tool == "apply_patch":
		if p := str("input"); p != "" {
			return patchStat(p)
		}
		return patchStat(str("patch"))
	}
	return 0, 0
}

// ToolStat is the lines a chat's edit call adds and removes.
func ToolStat(t managed.ToolCall) (add, del int) {
	if d := t.Diff; d != "" {
		if _, nw, ok := strings.Cut(d, "\nOld text:\n"); ok {
			o, n, _ := strings.Cut(nw, "\nNew text:\n")
			return lineStat(o, n)
		}
		return patchStat(d)
	}
	return editStat(t.ToolName, t.Input)
}

// patchStat counts a unified diff's or an apply_patch body's + and -
// lines, past their file headers.
func patchStat(p string) (add, del int) {
	for l := range strings.Lines(p) {
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "***"):
		case strings.HasPrefix(l, "+"):
			add++
		case strings.HasPrefix(l, "-"):
			del++
		}
	}
	return add, del
}

// lineStat is the lines b adds to a and removes from it, by their
// longest common run of lines.
func lineStat(a, b string) (add, del int) {
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(strings.TrimRight(s, "\n"), "\n")
	}
	x, y := split(a), split(b)
	// shortcut: a quadratic LCS, as the desktop's; past 250k cells the
	// change counts as all removed then all added, as the desktop shows it.
	if len(x)*len(y) > 250_000 {
		return len(y), len(x)
	}
	row := make([]int32, len(y)+1)
	prev := make([]int32, len(y)+1)
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				row[j] = prev[j+1] + 1
			} else {
				row[j] = max(prev[j], row[j+1])
			}
		}
		row, prev = prev, row
	}
	same := int(prev[0])
	return len(y) - same, len(x) - same
}
