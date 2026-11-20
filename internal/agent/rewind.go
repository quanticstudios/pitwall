package agent

import (
	"regexp"
	"strings"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// Claude Code's rewind picker (Esc Esc on an empty prompt, or /rewind)
// lists the conversation's prompts, oldest first, each over a line saying
// what code changed, and "(current)" last, where its cursor starts:
//
//	Rewind
//	Restore the code and/or conversation to the point before…
//	  Reply with just the word ALPHA.
//	  No code changes
//	❯ (current)
//	Enter to continue · Esc to cancel
//
// Enter on a prompt asks how to rewind, with numbered options such as
// "Restore conversation" (measured on 2.1.329).

// RewindCurrent is the picker's last row, the conversation as it is.
const RewindCurrent = "(current)"

// RewindPick reads the rewind picker's cursor row: the label of the prompt
// it is on, or RewindCurrent; false when the picker is not up.
func RewindPick(g vt.Grid) (string, bool) {
	rows := menuRows(g)
	if !rewindTitle(rows, "Restore the code and/or conversation") {
		return "", false
	}
	for _, r := range rows {
		if l, ok := strings.CutPrefix(strings.TrimLeft(r, " "), "❯ "); ok {
			return strings.TrimSpace(l), true
		}
	}
	return "", false
}

// rewindOption is a numbered option row, its cursor before it or not.
var rewindOption = regexp.MustCompile(`^\s*(❯\s+)?\d+\.\s+(.+?)\s*$`)

// RewindConfirm reads the menu that asks how to rewind a picked prompt:
// its options' labels and the one under the cursor; false when it is not
// up.
func RewindConfirm(g vt.Grid) (opts []string, at int, ok bool) {
	rows := menuRows(g)
	if !rewindTitle(rows, "Restore to the point before") {
		return nil, 0, false
	}
	at = -1
	for _, r := range rows {
		m := rewindOption.FindStringSubmatch(r)
		if m == nil {
			continue
		}
		if m[1] != "" {
			at = len(opts)
		}
		opts = append(opts, m[2])
	}
	return opts, at, len(opts) > 0 && at >= 0
}

// rewindTitle reports whether rows are the rewind dialog whose line under
// its title starts with sub.
func rewindTitle(rows []string, sub string) bool {
	for i, r := range rows {
		if strings.TrimSpace(r) == "Rewind" && i+1 < len(rows) && strings.HasPrefix(strings.TrimSpace(rows[i+1]), sub) {
			return true
		}
	}
	return false
}

// RewindMatch reports whether label, a picker row, is prompt's: its first
// line with runs of spaces as one, cut with an ellipsis when long.
func RewindMatch(label, prompt string) bool {
	label = strings.Join(strings.Fields(label), " ")
	first := ""
	for l := range strings.SplitSeq(prompt, "\n") {
		if first = strings.Join(strings.Fields(l), " "); first != "" {
			break
		}
	}
	if label == "" || first == "" {
		return false
	}
	if cut, ok := strings.CutSuffix(label, "…"); ok {
		return strings.HasPrefix(first, strings.TrimSpace(cut))
	}
	return label == first
}
