package panel

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/quanticstudios/pitwall/internal/session"
)

// A local command's output read as a card: /context's window by
// category, /usage and /cost's limits and tokens, /status's fields. Each
// parser reads what Claude Code 2.1.329 prints (TUI and stream-json) and
// what internal/session/claude writes in its words, and reports false for
// anything it does not know, which then shows as the plain output.

// ctxCat is one category of the context window.
type ctxCat struct {
	name   string
	tokens int64
	pct    float64
}

// detailRow is a row of a card's folded details: a name, what else
// Claude says of it, a value.
type detailRow struct{ name, meta, value string }

// detailSection is a titled list in a card's details.
type detailSection struct {
	title, note string
	rows        []detailRow
}

// ctxCard is /context: the model, the tokens used of the window, the
// categories that fill it, and the lists Claude prints under them.
type ctxCard struct {
	model        string
	used, window int64
	pct          float64
	cats         []ctxCat // in Claude's order; Free space and the buffer last
	details      []detailSection
}

// useWindow is one plan limit window.
type useWindow struct {
	name   string
	used   float64 // percent
	resets string  // "" when unknown
}

// useModel is one model's tokens and its cost, "" when unpriced.
type useModel struct {
	name                         string
	in, out, cacheRead, cacheWrt int64
	cost                         string
}

// useCard is /usage or /cost: the plan, its windows, the session's cost
// and tokens by model, and Claude's other figures.
type useCard struct {
	plan    string
	windows []useWindow
	cost    string
	models  []useModel
	rows    []detailRow
	// contrib is "What's contributing to your usage?": a section per
	// period, a row per suggestion with its share.
	contrib []detailSection
}

// statusCard is /status: its fields in order.
type statusCard struct{ rows []detailRow }

var (
	tokenNum  = `~?[0-9]+(?:\.[0-9]+)?[kKmM]?`
	ctxCatRe  = regexp.MustCompile(`^(.+?): (` + tokenNum + `) tokens \(<?([0-9.]+)%\)$`)
	ctxTotRe  = regexp.MustCompile(`^(` + tokenNum + `) ?/ ?(` + tokenNum + `)(?: tokens)? \(([0-9.]+)%\)$`)
	ctxRowRe  = regexp.MustCompile(`^[└├]\s*(.+?)(?::\s+(` + tokenNum + ` tokens))?$`)
	kvRe      = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9 ()/.,'-]{0,40}?):\s+(\S.*)$`)
	windowRe  = regexp.MustCompile(`^(.+?): ([0-9]+(?:\.[0-9]+)?)% used(?: · [Rr]esets (.+?))?$`)
	zoneRe    = regexp.MustCompile(` \([A-Za-z_]+/[A-Za-z_/+-]+\)$`)
	modelRe   = regexp.MustCompile(`^(\S+):\s+(` + tokenNum + `) input, (` + tokenNum + `) output, (` + tokenNum + `) cache read, (` + tokenNum + `) cache write(?: \((\$[0-9.]+)\))?$`)
	shareRe   = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?%)\s+(\S.*)$`)
	costRe    = regexp.MustCompile(`^(?:Estimated|Total) cost:\s+(\$[0-9.,]+)`)
	gridRunes = "⛀⛁⛂⛃⛶⛝⛿"
)

// parseTokens reads a count as Claude prints it: 812, 28.2k, 1m, ~480.
func parseTokens(s string) (int64, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "~")
	mul := 1.0
	switch {
	case strings.HasSuffix(s, "k") || strings.HasSuffix(s, "K"):
		mul, s = 1e3, s[:len(s)-1]
	case strings.HasSuffix(s, "m") || strings.HasSuffix(s, "M"):
		mul, s = 1e6, s[:len(s)-1]
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return int64(f*mul + 0.5), true
}

// plainLines are out's lines without colors, trimmed on the right.
func plainLines(out string) []string {
	ls := strings.Split(ansi.ReplaceAllString(strings.ReplaceAll(out, "\r", ""), ""), "\n")
	for i, l := range ls {
		ls[i] = strings.TrimRight(l, " \t")
	}
	return ls
}

// parseContext reads /context's output: the TUI's grid or stream-json's
// markdown.
func parseContext(out string) (ctxCard, bool) {
	ls := plainLines(out)
	for i, l := range ls {
		switch strings.TrimSpace(l) {
		case "":
			continue
		case "Context usage":
			return contextTUI(ls[i+1:])
		case "## Context Usage":
			return contextMarkdown(ls[i+1:])
		}
		break
	}
	return ctxCard{}, false
}

// ok is whether c has what its card draws.
func (c ctxCard) ok() bool { return c.window > 0 && len(c.cats) > 0 }

func contextTUI(ls []string) (ctxCard, bool) {
	var c ctxCard
	i, inCats := 0, false
	for ; i < len(ls); i++ {
		t := strings.TrimSpace(strings.Trim(ls[i], gridRunes+" \t"))
		if t == "" && strings.TrimSpace(ls[i]) == "" {
			if len(c.cats) > 0 {
				break // the grid ends; the lists follow
			}
			continue
		}
		switch m := ctxCatRe.FindStringSubmatch(t); {
		case m != nil:
			n, ok1 := parseTokens(m[2])
			p, err := strconv.ParseFloat(m[3], 64)
			if !ok1 || err != nil {
				return ctxCard{}, false
			}
			c.cats = append(c.cats, ctxCat{m[1], n, p})
		case ctxTotRe.MatchString(t):
			m := ctxTotRe.FindStringSubmatch(t)
			c.used, _ = parseTokens(m[1])
			c.window, _ = parseTokens(m[2])
			c.pct, _ = strconv.ParseFloat(m[3], 64)
		case t == "Estimated usage by category":
			inCats = true
		case t == "":
		case inCats:
			return ctxCard{}, false // a category in a shape not known
		case c.model == "":
			c.model = t
		}
	}
	// The lists: a bold title, "└ rows", a blank line between.
	var sec *detailSection
	for ; i < len(ls); i++ {
		t := strings.TrimSpace(ls[i])
		switch {
		case t == "":
			sec = nil
		case strings.HasPrefix(t, "/context"):
			// A hint for the TUI: the chat's card has the lists.
		case ctxRowRe.MatchString(t):
			m := ctxRowRe.FindStringSubmatch(t)
			if sec == nil {
				c.details = append(c.details, detailSection{})
				sec = &c.details[len(c.details)-1]
			}
			sec.rows = append(sec.rows, detailRow{name: m[1], value: m[2]})
		case kvRe.MatchString(t) && sec == nil:
			m := kvRe.FindStringSubmatch(t)
			c.details = append(c.details, detailSection{rows: []detailRow{{name: m[1], value: m[2]}}})
		default:
			title, note, _ := strings.Cut(t, " · ")
			c.details = append(c.details, detailSection{title: title, note: note})
			sec = &c.details[len(c.details)-1]
		}
	}
	return c, c.ok()
}

func contextMarkdown(ls []string) (ctxCard, bool) {
	var c ctxCard
	var sec *detailSection
	var head []string
	cats := false
	for _, l := range ls {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
		case strings.HasPrefix(t, "**Model:**"):
			c.model = strings.TrimSpace(strings.TrimPrefix(t, "**Model:**"))
		case strings.HasPrefix(t, "**Tokens:**"):
			m := ctxTotRe.FindStringSubmatch(strings.TrimSpace(strings.TrimPrefix(t, "**Tokens:**")))
			if m == nil {
				return ctxCard{}, false
			}
			c.used, _ = parseTokens(m[1])
			c.window, _ = parseTokens(m[2])
			c.pct, _ = strconv.ParseFloat(m[3], 64)
		case strings.HasPrefix(t, "### "):
			title := strings.TrimPrefix(t, "### ")
			cats, sec, head = title == "Estimated usage by category", nil, nil
			if !cats {
				c.details = append(c.details, detailSection{title: title})
				sec = &c.details[len(c.details)-1]
			}
		case strings.HasPrefix(t, "|"):
			cells := strings.Split(strings.Trim(t, "|"), "|")
			for k := range cells {
				cells[k] = strings.TrimSpace(cells[k])
			}
			switch {
			case strings.Trim(t, "|- :") == "":
				continue // the rule under a table's header
			case head == nil:
				head = cells
				continue
			case cats:
				if len(cells) != 3 {
					return ctxCard{}, false
				}
				n, ok1 := parseTokens(cells[1])
				p, err := strconv.ParseFloat(strings.TrimSuffix(cells[2], "%"), 64)
				if !ok1 || err != nil {
					return ctxCard{}, false
				}
				c.cats = append(c.cats, ctxCat{cells[0], n, p})
			case sec != nil && len(cells) >= 2:
				r := detailRow{name: cells[0], value: cells[len(cells)-1]}
				if len(cells) > 2 {
					r.meta = strings.Join(cells[1:len(cells)-1], " · ")
				}
				if n, ok := parseTokens(r.value); ok && head[len(head)-1] == "Tokens" {
					r.value = strconv.FormatInt(n, 10) + " tokens"
					if strings.HasPrefix(cells[len(cells)-1], "~") {
						r.value = "~" + r.value
					}
				}
				sec.rows = append(sec.rows, r)
			}
		case sec != nil:
			sec.rows = append(sec.rows, detailRow{name: t})
		}
	}
	return c, c.ok()
}

// parseUsage reads /usage or /cost's output; any line it does not know
// makes it no card.
func parseUsage(out string) (useCard, bool) {
	var c useCard
	contrib := false // in "What's contributing to your usage?"
	for _, l := range plainLines(out) {
		t := strings.TrimSpace(l)
		if t == "" || t == "Session" {
			continue
		}
		if t == "Usage by model:" || costRe.MatchString(t) || modelRe.MatchString(t) {
			contrib = false
		}
		if t == "Usage by model:" {
			continue
		}
		if t == "What's contributing to your usage?" {
			contrib = true
			continue
		}
		if contrib {
			switch m := shareRe.FindStringSubmatch(t); {
			case strings.HasPrefix(t, "Approximate"):
				c.contrib = append(c.contrib, detailSection{title: "What's contributing", note: strings.TrimPrefix(strings.TrimPrefix(t, "Approximate"), " · ")})
			case m != nil && len(c.contrib) > 0:
				s := &c.contrib[len(c.contrib)-1]
				s.rows = append(s.rows, detailRow{name: m[2], value: m[1]})
			default:
				title, note, _ := strings.Cut(t, " · ")
				c.contrib = append(c.contrib, detailSection{title: title, note: note})
			}
			continue
		}
		if m := windowRe.FindStringSubmatch(t); m != nil {
			p, _ := strconv.ParseFloat(m[2], 64)
			c.windows = append(c.windows, useWindow{m[1], p, zoneRe.ReplaceAllString(m[3], "")})
			continue
		}
		if m := modelRe.FindStringSubmatch(t); m != nil {
			var n [4]int64
			for k := range n {
				n[k], _ = parseTokens(m[2+k])
			}
			c.models = append(c.models, useModel{m[1], n[0], n[1], n[2], n[3], m[6]})
			continue
		}
		if m := costRe.FindStringSubmatch(t); m != nil {
			c.cost = m[1]
			continue
		}
		if strings.Contains(t, "using your subscription") {
			c.plan = "Subscription"
			continue
		}
		if m := kvRe.FindStringSubmatch(t); m != nil {
			c.rows = append(c.rows, detailRow{name: m[1], value: m[2]})
			continue
		}
		return useCard{}, false
	}
	return c, len(c.windows) > 0 || len(c.models) > 0 || c.cost != ""
}

// parseStatus reads /status's "Key: value" lines; it needs every line to
// be one, and Version or Session ID among them.
func parseStatus(out string) (statusCard, bool) {
	var c statusCard
	known := false
	for _, l := range plainLines(out) {
		t := strings.TrimSpace(l)
		if t == "" {
			continue
		}
		m := kvRe.FindStringSubmatch(t)
		if m == nil {
			return statusCard{}, false
		}
		known = known || m[1] == "Version" || m[1] == "Session ID"
		c.rows = append(c.rows, detailRow{name: m[1], value: strings.TrimSpace(m[2])})
	}
	return c, known && len(c.rows) >= 2
}

// aloneInTurn is whether item i, a command's output, is all its turn has
// besides its prompt and other harness items: stream-json runs each local
// command as a turn, whose output then stands on its own, not in a work
// log.
func aloneInTurn(content []session.Item, i int) bool {
	t := deref(content[i].TurnID)
	quiet := func(j int) bool {
		return content[j].Kind == session.KindSystem || content[j].Kind == session.KindUserPrompt
	}
	for j := i - 1; j >= 0 && deref(content[j].TurnID) == t; j-- {
		if !quiet(j) {
			return false
		}
	}
	for j := i + 1; j < len(content) && deref(content[j].TurnID) == t; j++ {
		if !quiet(j) {
			return false
		}
	}
	return true
}
