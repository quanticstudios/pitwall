package panel

import (
	"fmt"
	"testing"
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
