package agent

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// The labels of the answers a row presses beyond a question's own options.
const (
	AllowAlways = "Allow always"
	ApprovePlan = "Approve plan"
)

// Offer is an answer the form on an agent's screen takes: the label its
// button shows and the key that picks it.
type Offer struct{ Label, Key string }

var (
	// optionLine is a numbered option of a menu, with the cursor mark on the
	// highlighted one and Gemini's and OpenCode's box edge before it.
	optionLine = regexp.MustCompile(`^\s*(?:[│┃]\s*)?([❯›>●]\s*)?(\d)\.\s+(\S.*?)\s*$`)
	// codexLetter is the shortcut Codex prints after an option's text.
	codexLetter = regexp.MustCompile(`\(([a-z])\)$`)
	// hiddenRows is the row a Claude Code prompt ends a cut command with, such
	// as "+3 lines, +2 above (ctrl+o to see more)".
	hiddenRows = regexp.MustCompile(`^(?:\S )?\+?\d+ (?:lines?|above)\b`)
)

type formOption struct {
	label  string // the text on its first row
	cursor bool
	row    int // its index in the rows it was read from
}

// screenRows is every row of g, top to bottom, blank ones included.
func screenRows(g vt.Grid) []string {
	rows := make([]string, g.Rows)
	for y := range rows {
		rows[y] = rowText(g, y)
	}
	return rows
}

// formOptions reads the last numbered list in rows. Rows between its options,
// such as a description or a rule, are skipped.
func formOptions(rows []string) []formOption {
	var out []formOption
	for y, l := range rows {
		m := optionLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		n := int(m[2][0] - '0')
		if n == 1 {
			out = out[:0]
		} else if n != len(out)+1 {
			continue
		}
		out = append(out, formOption{label: m[3], cursor: m[1] != "", row: y})
	}
	return out
}

// MenuHint reports whether a line at the bottom of g's screen starts with the
// footer of a menu. Mid-line, the words are only someone quoting them.
func MenuHint(g vt.Grid) bool {
	for _, l := range bottomLines(g, screenLines) {
		if menuHint(l) {
			return true
		}
	}
	return false
}

func menuHint(line string) bool {
	low := strings.ToLower(strings.TrimLeft(line, " │┃"))
	for _, hint := range []string{"enter to select", "↑↓ select", "↑/↓"} {
		if strings.HasPrefix(low, hint) {
			return true
		}
	}
	return false
}

// cursorMenu finds a numbered list with a cursor at the bottom of g's screen
// and returns the highlighted option's text. A prompt below the list means it
// is transcript, such as an earlier numbered prompt.
func cursorMenu(g vt.Grid) (label string, ok bool) {
	lines := bottomLines(g, screenLines)
	slices.Reverse(lines)
	opts := formOptions(lines)
	if len(opts) < 2 {
		return "", false
	}
	for _, l := range lines[opts[len(opts)-1].row+1:] {
		if t := strings.TrimLeft(l, " "); strings.HasPrefix(t, "❯") || strings.HasPrefix(t, "›") || strings.HasPrefix(t, ">") {
			return "", false
		}
	}
	for _, o := range opts {
		if o.cursor {
			return o.label, true
		}
	}
	return "", false
}

// Choosing reports whether g's screen shows a menu of options, where Enter
// picks the highlighted one instead of sending what was pasted.
func Choosing(g vt.Grid) bool {
	_, ok := cursorMenu(g)
	return ok || MenuHint(g)
}

// FormUp reports whether g's screen shows a form that typed text and Enter
// would answer: a menu, a permission prompt or a plan. A free-text form takes
// them, so it does not count. pasted is text just typed: a numbered list of
// its own, echoed at the prompt from its first row, is not a menu.
func FormUp(g vt.Grid, pasted string) bool {
	if s := State(g); s == model.StatePendingApproval || s == model.StatePlanReady || MenuHint(g) {
		return true
	}
	label, ok := cursorMenu(g)
	return ok && (pasted == "" || !strings.HasPrefix(squeeze(pasted), "1"+squeeze(label)))
}

// Offers is what the form on g's screen takes for a, beyond Allow and Deny:
// a question's options, Allow always on a permission prompt, Approve plan on
// a plan. It reads the screen, so it is empty when the form a asks about is
// not on it, is not the kind a's state says, or has an agent's keys pitwall
// does not know. Only a Claude Code question or a Claude Code or Codex
// approval takes any.
func Offers(a model.Activity, g vt.Grid) []Offer {
	if a.Provider != model.ProviderClaude && a.Provider != model.ProviderCodex {
		return nil
	}
	if State(g) != a.State {
		return nil
	}
	rows := screenRows(g)
	opts := formOptions(rows)
	cursor := -1
	for i, o := range opts {
		if o.cursor {
			if cursor >= 0 {
				return nil
			}
			cursor = i
		}
	}
	if cursor < 0 {
		return nil
	}
	switch a.State {
	case model.StatePendingApproval:
		return pickOption(a.Provider, opts, AllowAlways, func(low string) bool {
			if a.Provider == model.ProviderCodex {
				return strings.Contains(low, "don't ask again") || strings.Contains(low, "for this session")
			}
			return strings.Contains(low, "don't ask again") || strings.Contains(low, "always") || strings.Contains(low, "allow all")
		})
	case model.StatePlanReady:
		// why: the other yeses also raise the permission mode, which a button
		// named Approve plan does not say.
		return pickOption(a.Provider, opts, ApprovePlan, func(low string) bool {
			return strings.Contains(low, "manually approve")
		})
	case model.StateAwaitingInput:
		return questionOffers(a, rows, opts, cursor)
	}
	return nil
}

// pickOption offers the one option starting with Yes that match accepts,
// under label. Two that do are no offer: which one the button means would be
// a guess.
func pickOption(p model.Provider, opts []formOption, label string, match func(low string) bool) []Offer {
	var out []Offer
	for i, o := range opts {
		low := strings.ToLower(o.label)
		if !strings.HasPrefix(low, "yes") || !match(low) {
			continue
		}
		k := optionKey(p, i, o)
		if k == "" {
			return nil
		}
		out = append(out, Offer{label, k})
	}
	if len(out) != 1 {
		return nil
	}
	return out
}

// optionKey is the key that picks opts[i]. Claude Code picks an option by its
// number. Codex's numbers are not shortcuts, so it takes the letter after the
// text, and an option without one has no key.
func optionKey(p model.Provider, i int, o formOption) string {
	if p == model.ProviderClaude {
		return strconv.Itoa(i + 1)
	}
	if m := codexLetter.FindStringSubmatch(o.label); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

// questionOffers matches the question a hook named against the whole form on
// screen, each option with its wrapped rows and description, so a stale button
// cannot answer a newer form that starts the same way. The cursor must be on
// one of the options, as Claude Code's "Type something." option types a digit
// into its field.
func questionOffers(a model.Activity, rows []string, opts []formOption, cursor int) []Offer {
	if a.Provider != model.ProviderClaude || len(a.Options) == 0 || cursor >= len(a.Options) || len(opts) < len(a.Options) {
		return nil
	}
	if !questionAbove(rows, opts[0].row, a.Detail) {
		return nil
	}
	out := make([]Offer, len(a.Options))
	for i, l := range a.Options {
		want := l
		if i < len(a.OptionNotes) {
			want += " " + a.OptionNotes[i]
		}
		text, ok := optionText(rows, opts, i)
		if !ok || squeeze(text) != squeeze(want) {
			return nil
		}
		out[i] = Offer{l, strconv.Itoa(i + 1)}
	}
	return out
}

// questionAbove reports whether the rows above row hold exactly question: all
// of it, and above it only blank rows, then the form's header or rule or the
// top of the screen. A question a form shows only part of does not match.
func questionAbove(rows []string, row int, question string) bool {
	want, got := squeeze(question), ""
	i := row - 1
	for ; i >= 0 && len(got) < len(want); i-- {
		got = squeeze(rows[i]) + got
	}
	if want == "" || got != want {
		return false
	}
	for i >= 0 && rows[i] == "" {
		i--
	}
	return i < 0 || squeeze(rows[i]) == "" || strings.ContainsAny(rows[i], "☐☑☒✔")
}

// optionText is option i of opts as shown: everything from its label to the
// next option, or for the last one to the menu's footer. ok is false with no
// footer to end it at, as what belongs to the option is then a guess.
func optionText(rows []string, opts []formOption, i int) (text string, ok bool) {
	end := -1
	if i+1 < len(opts) {
		end = opts[i+1].row
	} else {
		for y := len(rows) - 1; y > opts[i].row && end < 0; y-- {
			if menuHint(rows[y]) {
				end = y
			}
		}
	}
	if end < 0 {
		return "", false
	}
	text = opts[i].label
	for _, l := range rows[opts[i].row+1 : end] {
		text += " " + l
	}
	return text, true
}

// wrapSlack is how many columns short of a pane's width a row may end and still
// count as full: the agent's right margin.
const wrapSlack = 4

// CommandShown reports whether the permission prompt on g's screen asks to run
// command, as far as the screen shows it. Rows must match it byte for byte, apart
// from the prompt's indent and a row end where wrapping could have split it. A
// prompt that shows part of a long command is compared on that part. A layout it
// does not know has no command to compare, so that is false too.
func CommandShown(p model.Provider, g vt.Grid, command string) bool {
	// why: a newline, tab or other space has no one way to look on screen, and
	// part of the command may be off screen. A refusal costs one trip to the tab.
	if strings.ContainsFunc(command, func(r rune) bool { return r != ' ' && (unicode.IsSpace(r) || !unicode.IsGraphic(r)) }) {
		return false
	}
	want := strings.Trim(command, " ")
	rows := screenRows(g)
	opts := formOptions(rows)
	if len(opts) == 0 {
		return false
	}
	from, to, start := claudeCommand(rows[:opts[0].row])
	if p == model.ProviderCodex {
		from, to, start = codexCommand(rows[:opts[0].row])
	}
	for from < to && rows[to-1] == "" {
		to--
	}
	for from < to && rows[from] == "" {
		from++
	}
	return commandFits(g, from, rows[from:to], start, want)
}

// claudeCommand is the rows from up to to above a Claude Code prompt's options
// that hold its command, between the two nearest rules. start is false when the
// upper rule has scrolled off, so the first rows may be gone with it.
func claudeCommand(above []string) (from, to int, start bool) {
	lower := lastRule(above)
	if lower < 0 {
		return 0, 0, false
	}
	upper := lastRule(above[:lower])
	return upper + 1, lower, upper >= 0
}

func lastRule(rows []string) int {
	for y := len(rows) - 1; y >= 0; y-- {
		if rows[y] != "" && strings.Trim(rows[y], " ╌─") == "" {
			return y
		}
	}
	return -1
}

// codexCommand is the rows from up to to above a Codex prompt's options that hold
// its command, from the one that starts with "$ ", which it blanks in above.
// start is false with no such row: it has scrolled off, or this is not a command.
func codexCommand(above []string) (from, to int, start bool) {
	for y := len(above) - 1; y >= 0; y-- {
		t := strings.TrimLeft(above[y], " ")
		if s, ok := strings.CutPrefix(t, "$ "); ok {
			above[y] = strings.Repeat(" ", len(above[y])-len(t)+2) + s
			return y, len(above), true
		}
	}
	return 0, len(above), false
}

// commandFits reports whether rows, which start at row y of g, show want. Without
// start its first rows may be off screen, and a count row or a final ellipsis
// says its last ones are hidden.
func commandFits(g vt.Grid, y int, rows []string, start bool, want string) bool {
	rows = slices.Clone(rows)
	end := true
	if n := len(rows) - 1; n >= 0 {
		if last := strings.TrimLeft(rows[n], " "); hiddenRows.MatchString(last) {
			start = start && !strings.Contains(last, "above")
			rows, end = rows[:n], false
		} else if t, ok := strings.CutSuffix(rows[n], "…"); ok {
			rows[n], end = strings.TrimRight(t, " "), false
		}
	}
	// A blank row is no part of a wrapped line.
	if len(rows) == 0 || slices.Contains(rows, "") {
		return false
	}
	indent := len(rows[0])
	for _, r := range rows {
		indent = min(indent, len(r)-len(strings.TrimLeft(r, " ")))
	}
	for i, r := range rows {
		rows[i] = r[indent:]
	}
	rows[0] = strings.TrimLeft(rows[0], " ")
	for i := 0; i < len(want); i++ {
		if e, ok := rowsFollow(g, y, rows, want, i); ok && (!end || e == len(want)) {
			return true
		}
		if start {
			return false
		}
	}
	return false
}

// rowsFollow matches rows, which start at row y of g, against want from byte i
// and returns where they end. Wrapping moves a word down only if it would not
// fit, and cuts one only on a full row, so any other row end is not in the
// command.
func rowsFollow(g vt.Grid, y int, rows []string, want string, i int) (end int, ok bool) {
	full := g.Cols - wrapSlack
	for k, r := range rows {
		if k > 0 {
			switch used := rowCells(g, y+k-1); {
			case i == len(want):
				return 0, false
			case want[i] == ' ':
				if used+1+wordCells(g, y+k) <= full {
					return 0, false
				}
				i++
				r = strings.TrimPrefix(r, " ")
			case used < full:
				return 0, false
			}
		}
		if !strings.HasPrefix(want[i:], r) {
			return 0, false
		}
		i += len(r)
	}
	return i, true
}

// blankCell reports a cell with nothing drawn in it. A wide character's second
// cell has no width, and it is drawn.
func blankCell(c vt.Cell) bool { return c.Width != 0 && (c.Content == "" || c.Content == " ") }

// rowCells is how many cells of row y of g reach its last drawn one.
func rowCells(g vt.Grid, y int) int {
	for x := g.Cols - 1; x >= 0; x-- {
		if !blankCell(g.At(x, y)) {
			return x + 1
		}
	}
	return 0
}

// wordCells is how many cells the first word of row y of g takes.
func wordCells(g vt.Grid, y int) int {
	n := 0
	for x := 0; x < g.Cols; x++ {
		switch {
		case !blankCell(g.At(x, y)):
			n++
		case n > 0:
			return n
		}
	}
	return n
}

// squeeze keeps s's letters and digits, lowercased: a terminal wraps and
// indents text and Claude Code renders a question's markdown.
func squeeze(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, s)
}
