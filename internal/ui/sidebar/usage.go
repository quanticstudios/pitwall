package sidebar

import (
	"fmt"
	"strings"

	"github.com/quanticstudios/pitwall/internal/flow"
)

// icGauge is lucide's gauge, beside a tab's token use in its hover card.
const icGauge = "M12 14l4-4M3.34 19a10 10 0 1 1 17.32 0"

// TokenCount is n tokens in short: "950", "12.3k", "480k", "1.2M".
func TokenCount(n int64) string {
	f := float64(n)
	var s string
	switch {
	case n < 1000:
		return fmt.Sprint(n)
	case n < 100_000:
		s = fmt.Sprintf("%.1fk", f/1e3)
	case n < 999_500:
		s = fmt.Sprintf("%.0fk", f/1e3)
	case n < 100_000_000:
		s = fmt.Sprintf("%.1fM", f/1e6)
	default:
		s = fmt.Sprintf("%.0fM", f/1e6)
	}
	return strings.Replace(s, ".0", "", 1)
}

// FillText is a context fill as a percentage: "34%", "<1%".
func FillText(f float64) string {
	if f < 0.01 {
		return "<1%"
	}
	return fmt.Sprintf("%.0f%%", f*100)
}

// Dollars is a cost: "<$0.01", "$4.12", "$1,204".
func Dollars(c float64) string {
	switch {
	case c < 0.01:
		return "<$0.01"
	case c < 1000:
		return fmt.Sprintf("$%.2f", c)
	}
	n := fmt.Sprintf("%.0f", c)
	for i := len(n) - 3; i > 0; i -= 3 {
		n = n[:i] + "," + n[i:]
	}
	return "$" + n
}

// UsageText is a session's token use on one line: "1.2M tokens · 34%
// context", then the cost when cost is set and every model has a price.
// A count that misses the start of a long file reads "≥1.2M". "" when
// there is no usage.
func UsageText(u flow.Usage, cost bool) string {
	t := u.Tokens().Total()
	if t == 0 {
		return ""
	}
	n := TokenCount(t)
	if u.Partial {
		n = "≥" + n
	}
	parts := []string{n + " tokens"}
	if f, ok := u.Fill(); ok {
		parts = append(parts, FillText(f)+" context")
	}
	if c, ok := u.Cost(); cost && ok {
		d := Dollars(c)
		if u.Partial {
			d = "≥" + d
		}
		parts = append(parts, d)
	}
	return strings.Join(parts, " · ")
}
