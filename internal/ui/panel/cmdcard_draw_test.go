package panel

import (
	"fmt"
	"testing"

	"github.com/quanticstudios/pitwall/internal/session"
)

func TestCardSummaries(t *testing.T) {
	if got := windowLeft(useWindow{"Weekly", 52, "Nov 24, 9pm"}); got != "48% left · resets Nov 24, 9pm" {
		t.Errorf("windowLeft %q", got)
	}
	if got := windowLeft(useWindow{name: "Weekly", used: 104}); got != "0% left" {
		t.Errorf("windowLeft over the limit %q", got)
	}
	if got := modelTokens(useModel{in: 2, out: 7, cacheRead: 28_200, cacheWrt: 14_400}); got != "42.6k in · 7 out" {
		t.Errorf("modelTokens %q", got)
	}
	if got := ctxSummary(ctxCard{used: 42_600, window: 1_000_000, pct: 4}); got != "42.6k / 1m · 4%" {
		t.Errorf("ctxSummary %q", got)
	}
}

// outCard picks the card a text reads as, parses it once, and gives none
// to an output no parser knows; a card's details start folded.
func TestOutCard(t *testing.T) {
	d := &cdraw{t: &Transcript{}}
	d.t.init()
	for file, want := range map[string]string{"context-tui.txt": "panel.ctxCard", "usage-stream.txt": "panel.useCard",
		"cost-tui.txt": "panel.useCard", "status.txt": "panel.statusCard"} {
		c := d.outCard(file, cardFixture(t, file))
		if got := typeName(c); got != want {
			t.Errorf("%s: %s, want %s", file, got, want)
		}
	}
	if c := d.outCard("x", "Status dialog dismissed"); c != nil {
		t.Errorf("plain output: %T, want none", c)
	}
	if c := d.outCard("x", cardFixture(t, "status.txt")); c == nil {
		t.Error("an item's new text is parsed again")
	}
	if d.t.open["card:x"] {
		t.Error("details open by default")
	}
	// Deferred tools take no room: the bar and legend leave them out.
	c, _ := parseContext(cardFixture(t, "context-md.txt"))
	n := 0
	for _, k := range c.cats {
		if deferred(k) {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d deferred categories, want 2", n)
	}
}

func typeName(v any) string { return fmt.Sprintf("%T", v) }

// stream-json runs a local command as a turn of its own: its output is
// a block of its own, not a work log; one in a turn with work stays in it.
func TestOutputBlock(t *testing.T) {
	t1, t2 := "t1", "t2"
	content := []session.Item{
		{ID: "p1", Kind: session.KindUserPrompt, Text: "/context", TurnID: &t1},
		{ID: "o1", Kind: session.KindSystem, Phase: session.PhaseOutput, Text: "x", TurnID: &t1},
		{ID: "p2", Kind: session.KindUserPrompt, Text: "go", TurnID: &t2},
		{ID: "o2", Kind: session.KindSystem, Phase: session.PhaseOutput, Text: "y", TurnID: &t2},
		{ID: "r2", Kind: session.KindAssistantText, Text: "done", TurnID: &t2},
	}
	var kinds []string
	for _, b := range blocks(content, nil) {
		kinds = append(kinds, fmt.Sprint(b.kind, ":", content[b.lo].ID))
	}
	want := fmt.Sprint([]string{fmt.Sprint(blockPrompt, ":p1"), fmt.Sprint(blockSystem, ":o1"), fmt.Sprint(blockPrompt, ":p2"), fmt.Sprint(blockTurn, ":o2")})
	if got := fmt.Sprint(kinds); got != want {
		t.Errorf("blocks %s, want %s", got, want)
	}
}
