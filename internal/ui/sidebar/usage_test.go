package sidebar

import (
	"testing"

	"github.com/quanticstudios/pitwall/internal/flow"
)

func TestUsageFormat(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 950: "950", 1000: "1k", 12_345: "12.3k", 99_960: "100k", 480_200: "480k",
		999_499: "999k", 999_500: "1M", 1_234_567: "1.2M", 25_000_000: "25M", 345_600_000: "346M",
		999_499_999: "999M", 999_500_000: "1B", 1_076_000_000: "1.1B", 250_000_000_000: "250B"} {
		if got := TokenCount(n); got != want {
			t.Errorf("TokenCount(%d) = %q, want %q", n, got, want)
		}
	}
	for c, want := range map[float64]string{0.004: "<$0.01", 4.123: "$4.12", 999.994: "$999.99", 1204.4: "$1,204", 1_234_567: "$1,234,567"} {
		if got := Dollars(c); got != want {
			t.Errorf("Dollars(%v) = %q, want %q", c, got, want)
		}
	}
	for f, want := range map[float64]string{0.004: "<1%", 0.342: "34%", 1: "100%"} {
		if got := FillText(f); got != want {
			t.Errorf("FillText(%v) = %q, want %q", f, got, want)
		}
	}
	if got := UsageText(flow.Usage{}, true); got != "" {
		t.Errorf("no usage reads %q", got)
	}
}

// The hover card shows the usage line only when there is one.
func TestCardUsageLine(t *testing.T) {
	if got := (card{title: "x", usage: "1k tokens"}).lines(); got != "tu" {
		t.Errorf("lines = %q", got)
	}
	if got := (card{title: "x"}).lines(); got != "t" {
		t.Errorf("lines = %q", got)
	}
}
