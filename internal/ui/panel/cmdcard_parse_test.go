package panel

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// cardFixture is testdata/cmdcard/name: Claude Code 2.1.329's output from
// a live run, or what internal/session/claude writes for it.
func cardFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/cmdcard/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseContextTUI(t *testing.T) {
	c, ok := parseContext(cardFixture(t, "context-tui.txt"))
	if !ok {
		t.Fatal("no card")
	}
	if c.model != "Sonnet 5.5" || c.used != 42_600 || c.window != 1_000_000 || c.pct != 4 {
		t.Errorf("head %q %d/%d %v%%", c.model, c.used, c.window, c.pct)
	}
	names := []string{}
	for _, k := range c.cats {
		names = append(names, k.name)
	}
	if got := strings.Join(names, ","); got != "System prompt,System tools,MCP server instructions,Skills,Messages,Free space,Autocompact buffer" {
		t.Errorf("categories %s", got)
	}
	if k := c.cats[1]; k.tokens != 21_700 || k.pct != 2 {
		t.Errorf("System tools %+v", k)
	}
	var titles []string
	for _, s := range c.details {
		titles = append(titles, s.title+"|"+s.note)
	}
	if got := strings.Join(titles, ","); got != "|,MCP tools|/mcp (loaded on-demand),Skills|/skills" {
		t.Errorf("sections %s", got)
	}
	if r := c.details[1].rows; len(r) != 1 || r[0].name != "87 tools" {
		t.Errorf("MCP rows %+v", r)
	}
}

// A share under 0.1% prints as "<0.1%".
func TestParseContextMemory(t *testing.T) {
	c, ok := parseContext(cardFixture(t, "context-tui-memory.txt"))
	if !ok {
		t.Fatal("no card")
	}
	i := slices.IndexFunc(c.cats, func(k ctxCat) bool { return k.name == "Memory files" })
	if i < 0 || c.cats[i].tokens != 25 || c.cats[i].pct != 0.1 || len(c.cats) != 8 {
		t.Errorf("categories %+v", c.cats)
	}
	if len(c.details) != 4 || c.details[2].title != "Memory files" || c.details[2].rows[0].name != "1 file · 25 tokens" {
		t.Errorf("sections %+v", c.details)
	}
}

func TestParseContextMarkdown(t *testing.T) {
	c, ok := parseContext(cardFixture(t, "context-md.txt"))
	if !ok {
		t.Fatal("no card")
	}
	if c.model != "claude-sonnet-5-5" || c.used != 17_600 || c.window != 1_000_000 || len(c.cats) != 9 {
		t.Errorf("head %q %d/%d, %d categories", c.model, c.used, c.window, len(c.cats))
	}
	if len(c.details) != 2 || c.details[0].title != "MCP Tools" || c.details[1].title != "Skills" {
		t.Fatalf("sections %+v", c.details)
	}
	if r := c.details[0].rows[0]; r != (detailRow{"mcp__docs__search", "docs", "154 tokens"}) {
		t.Errorf("MCP row %+v", r)
	}
	if r := c.details[1].rows[0]; r.value != "~480 tokens" || r.meta != "Built-in" {
		t.Errorf("skill row %+v", r)
	}
}

func TestParseUsage(t *testing.T) {
	c, ok := parseUsage(cardFixture(t, "usage-stream.txt"))
	if !ok || c.plan != "Subscription" || c.cost != "$0.0179" || len(c.windows) != 2 || len(c.models) != 1 {
		t.Fatalf("card %+v %v", c, ok)
	}
	if w := c.windows[0]; w != (useWindow{"Current week (all models)", 52, "Nov 24, 9pm"}) {
		t.Errorf("window %+v", w)
	}
	if m := c.models[0]; m != (useModel{"claude-sonnet-5-5", 2, 7, 13_600, 4_100, "$0.0179"}) {
		t.Errorf("model %+v", m)
	}
	c, ok = parseUsage(cardFixture(t, "usage-history.txt"))
	if !ok || len(c.windows) != 2 || len(c.models) != 1 || c.cost != "$0.0432" || len(c.contrib) != 3 {
		t.Fatalf("card with history %+v %v", c, ok)
	}
	if s := c.contrib[1]; s.title != "Last 24h" || s.note != "8 requests · 7 sessions" || len(s.rows) != 2 ||
		s.rows[0] != (detailRow{name: "See what fills your context · run /context", value: "53%"}) {
		t.Errorf("contributions %+v", s)
	}
	c, ok = parseUsage(cardFixture(t, "cost-tui.txt"))
	if !ok || c.cost != "$0.0604" || len(c.models) != 2 || len(c.rows) != 4 || c.rows[3].name != "Prompt cache (main)" {
		t.Fatalf("cost card %+v %v", c, ok)
	}
}

func TestParseStatus(t *testing.T) {
	c, ok := parseStatus(cardFixture(t, "status.txt"))
	if !ok || len(c.rows) != 6 || c.rows[2] != (detailRow{name: "cwd", value: "/tmp/demo/repo"}) {
		t.Fatalf("card %+v %v", c, ok)
	}
}

// Anything else, or a format that moved, is no card: the plain output
// shows instead.
func TestParseUnknown(t *testing.T) {
	for _, out := range []string{
		"",
		"Status dialog dismissed",
		"/status isn't available in this environment.",
		"Set model to Opus 5.5",
		// A /context whose header stayed but whose categories changed shape.
		"Context usage\n⛁ ⛁  Sonnet 5.5\n⛁ ⛁  42.6k/1m tokens (4%)\n⛁ ⛁  System prompt — 3k",
		"Context usage\n⛁ ⛁  Sonnet 5.5\n⛁ ⛁  42.6k/1m tokens (4%)\n⛁ ⛁  Estimated usage by category\n⛁ ⛁  ⛁ System prompt: 3k tokens (0.3%)\n⛁ ⛁  ⛁ Messages: about 11k",
		"## Context Usage\n\n**Model:** x\n**Tokens:** lots\n",
		"## Context Usage\n\n**Model:** x\n**Tokens:** 1k / 2k (50%)\n\n### Estimated usage by category\n\n| Category | Tokens |\n|--|--|\n| A | 1k |\n",
		// /usage with a line it does not know.
		"Current week (all models): 52% used · resets Nov 24, 9pm\nA new kind of line with no colon",
		// Key-value lines that are not /status.
		"Total duration (API): 2s\nTotal duration (wall): 47s",
		strings.Repeat("Version: 1\n", 2) + "not a field",
	} {
		_, c := parseContext(out)
		_, u := parseUsage(out)
		_, s := parseStatus(out)
		if c || u || s {
			t.Errorf("%q: context %v usage %v status %v, want no card", out, c, u, s)
		}
	}
}

func TestParseTokens(t *testing.T) {
	for s, want := range map[string]int64{"812": 812, "28.2k": 28_200, "1m": 1_000_000, "~480": 480, "949.4k": 949_400} {
		if n, ok := parseTokens(s); !ok || n != want {
			t.Errorf("parseTokens(%q) = %d %v, want %d", s, n, ok, want)
		}
	}
	if _, ok := parseTokens("lots"); ok {
		t.Error("parseTokens(lots) ok")
	}
}
