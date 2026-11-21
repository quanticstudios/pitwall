package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/vt"
)

// liveGrid is fixture name, a screen recorded at 80x24, as a grid.
func liveGrid(t *testing.T, name string) vt.Grid {
	t.Helper()
	b, err := os.ReadFile("testdata/live/" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	e := vt.New(80, 24, nil)
	e.Write([]byte(strings.ReplaceAll(strings.TrimSuffix(string(b), "\n"), "\n", "\r\n")))
	return e.Snapshot()
}

func TestLiveScreen(t *testing.T) {
	claude, codex, agy := model.ProviderClaude, model.ProviderCodex, model.ProviderAntigravity
	for _, tc := range []struct {
		name      string
		p         model.Provider
		top       bool
		text      string // the block unwrapped; "" for none
		thinking  string
		command   string
		firstRow  string
		rowsCount int
	}{
		{name: "claude-start", p: claude, top: true,
			text: "TCP congestion control keeps a sender from overwhelming the network. Because no one tells a sender how much capacity the path has, TCP infers it from"},
		{name: "claude-bullet", p: claude},
		{name: "claude-tool", p: claude},
		{name: "claude-running", p: claude},
		{name: "claude-scrolled", p: claude, firstRow: "cautiously, about one segment per round trip. This is the \"additive increase\"", rowsCount: 19},
		{name: "codex-start", p: codex, top: true,
			text: "[work · ready]\nTCP congestion control adjusts how much data a sender puts into the network before receiving acknowledgments. If a sender transmits faster than a link can carry"},
		{name: "codex-scrolled", p: codex, firstRow: "double each round-trip time. This lets TCP discover available capacity", rowsCount: 17},
		{name: "codex-tool", p: codex},
		{name: "antigravity-thinking", p: agy, thinking: `The "slow start" phase is observed, where the congestion window is initial`},
		{name: "antigravity-start", p: agy, top: true, firstRow: "TCP congestion control is a set of mechanisms designed to prevent a sender", rowsCount: 16},
		{name: "antigravity-running", p: agy, command: "ls", thinking: "I will list the files in the current directory. Next, the content of `READ"},
	} {
		lv := LiveScreen(tc.p, liveGrid(t, tc.name))
		got := Unwrap(lv.Rows, lv.Width)
		if lv.Top != tc.top || tc.text != "" && got != tc.text || tc.text == "" && tc.firstRow == "" && len(lv.Rows) > 0 {
			t.Errorf("%s: top %v, text %q", tc.name, lv.Top, got)
		}
		if tc.firstRow != "" && (len(lv.Rows) != tc.rowsCount || lv.Rows[0] != tc.firstRow) {
			t.Errorf("%s: %d rows, first %q", tc.name, len(lv.Rows), lv.Rows)
		}
		if lv.Thinking != tc.thinking || lv.Command != tc.command {
			t.Errorf("%s: thinking %q, command %q", tc.name, lv.Thinking, lv.Command)
		}
	}
}

func TestUnwrap(t *testing.T) {
	rows := []string{
		"Loss is the main signal that the network is congested. If three duplicate",
		"acknowledgments arrive, the sender assumes a single packet was dropped.",
		"",
		"Steps:",
		"- slow start doubles the window each round trip until it reaches the slow",
		"  start threshold",
		"- congestion avoidance",
		"1. one",
		"done",
	}
	want := "Loss is the main signal that the network is congested. If three duplicate acknowledgments arrive, the sender assumes a single packet was dropped.\n\n" +
		"Steps:\n- slow start doubles the window each round trip until it reaches the slow start threshold\n- congestion avoidance\n1. one\ndone"
	if got := Unwrap(rows, 78); got != want {
		t.Errorf("Unwrap:\n%s\nwant\n%s", got, want)
	}
}

func TestFold(t *testing.T) {
	md := "The workspace has [main.go](file:///x/main.go) and **README.md**:\n\n- `ls`"
	screen := "The workspace has main.go and README.md:\n\n• ls"
	if Fold(md, 100) != Fold(screen, 100) {
		t.Errorf("Fold: %q != %q", Fold(md, 100), Fold(screen, 100))
	}
}

// BenchmarkLiveScreen is one read of a 200x60 Claude Code screen that
// streams a reply, as a pane chat does on each ChatRead while a turn runs.
func BenchmarkLiveScreen(b *testing.B) {
	rule := strings.Repeat("─", 200)
	rows := []string{"❯ explain tcp", "", "● " + strings.Repeat("word ", 39)}
	for len(rows) < 54 {
		rows = append(rows, "  "+strings.Repeat("word ", 39))
	}
	rows = append(rows, "", "✻ Moseying… (1s)", rule, "❯ ", rule, "  footer")
	e := vt.New(200, 60, nil)
	e.Write([]byte(strings.Join(rows, "\r\n")))
	g := e.Snapshot()
	for b.Loop() {
		lv := LiveScreen(model.ProviderClaude, g)
		Fold(Unwrap(lv.Rows, lv.Width), 60)
	}
}
