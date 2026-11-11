package agent

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/vt"
)

// Claude Code 2.1.322 saves a pick made with Enter, or typed as
// "/model <name>" or "/effort <level>", as the default for every new
// session. Its /model picker and /effort slider take s to apply the pick to
// the running session only. ModelMenu and EffortMenu read them for that.

// menuRows are the rows below the screen's last rule, top first: an open
// menu's rows, or the prompt box and its footer. A menu under a finished
// turn has a ▔ rule on top.
func menuRows(g vt.Grid) []string {
	var rows []string
	for y := g.Rows - 1; y >= 0; y-- {
		l := rowText(g, y)
		if isRule(l) || strings.HasPrefix(l, "▔▔▔") {
			break
		}
		rows = append(rows, l)
	}
	slices.Reverse(rows)
	return rows
}

// SessionMenuUp reports an open /model picker or /effort slider, read in
// full or not: its footer offers a session-only pick, or the picker's title
// shows while its cursor sits on a row that takes no s, such as More models.
func SessionMenuUp(g vt.Grid) bool {
	rows := menuRows(g)
	return footerSays(rows, modelFooter) || footerSays(rows, effortFooter) ||
		slices.ContainsFunc(rows, func(l string) bool { return strings.TrimSpace(l) == "Select model" })
}

// The footers of the /model picker and the /effort slider name s.
const modelFooter, effortFooter = "s to use this session only", "s for this session only"

// footerSays reports whether rows say phrase, on one row or wrapped onto
// more: a narrow pane splits a footer at a space, or mid-word when the
// terminal wraps it.
func footerSays(rows []string, phrase string) bool {
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	return strings.Contains(squash(strings.Join(rows, "")), squash(phrase))
}

// MenuRow is a row of Claude Code's /model picker: its number, from 1, and
// its name without the check mark, such as "Sonnet 5.5" or "Default
// (recommended)".
type MenuRow struct {
	N    int
	Name string
}

// ↑ and ↓ mark the rows at the edges of a picker scrolled to fit its pane.
var modelMenuRow = regexp.MustCompile(`^\s*(?:(❯)|[↑↓])?\s*([0-9]+)\.\s+(\S.*?)(?:\s{2,}|$)`)

// ModelMenu reads Claude Code's /model picker: the rows it shows, top
// first, and the number of the row under the cursor. A picker taller than
// its pane shows some of its rows. ok is false when no picker that offers a
// session-only pick is open.
func ModelMenu(g vt.Grid) (rows []MenuRow, cursor int, ok bool) {
	lines := menuRows(g)
	if !footerSays(lines, modelFooter) {
		return nil, 0, false
	}
	for _, l := range lines {
		m := modelMenuRow.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[2])
		if len(rows) > 0 && n != rows[len(rows)-1].N+1 {
			return nil, 0, false
		}
		if m[1] != "" {
			if cursor > 0 {
				return nil, 0, false
			}
			cursor = n
		}
		rows = append(rows, MenuRow{n, strings.TrimSpace(strings.ReplaceAll(m[3], "✔", ""))})
	}
	return rows, cursor, cursor > 0
}

// ModelRow is the number of the row for a /model name such as "sonnet",
// "default" or "opus[1m]", or -1 when no row shown is that model.
func ModelRow(rows []MenuRow, name string) int {
	base, wide := strings.CutSuffix(strings.ToLower(name), "[1m]")
	for _, r := range rows {
		low := strings.ToLower(r.Name)
		first, _, _ := strings.Cut(low, " ")
		if first == base && strings.Contains(low, "1m") == wide {
			return r.N
		}
	}
	return -1
}

var effortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// EffortMenu reads Claude Code's /effort slider: its levels, lowest first,
// and the one under its marker. ok is false when no slider that offers a
// session-only pick is open.
func EffortMenu(g vt.Grid) (levels []string, at int, ok bool) {
	lines := menuRows(g)
	if !footerSays(lines, effortFooter) {
		return nil, -1, false
	}
	for i := 1; i < len(lines); i++ {
		marker := slices.Index([]rune(lines[i-1]), '▲')
		if marker < 0 {
			continue
		}
		var centers []int
		row := []rune(lines[i])
		for x := 0; x < len(row); {
			if row[x] == ' ' {
				x++
				continue
			}
			end := x
			for end < len(row) && row[end] != ' ' {
				end++
			}
			if w := string(row[x:end]); slices.Contains(effortLevels, w) {
				levels = append(levels, w)
				centers = append(centers, x+end-1) // twice the label's middle column
			}
			x = end
		}
		if len(levels) == 0 {
			return nil, -1, false
		}
		at = 0
		for j, c := range centers {
			if abs(c-2*marker) < abs(centers[at]-2*marker) {
				at = j
			}
		}
		return levels, at, true
	}
	return nil, -1, false
}

func abs(n int) int { return max(n, -n) }
