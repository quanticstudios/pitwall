package flow

// PricesAsOf is the day the table below was checked against Anthropic's
// and OpenAI's published prices. Update it with the table.
const PricesAsOf = "2026-10-08"

// Price is a model's list price in US dollars per million tokens.
type Price struct {
	Input, Output, CacheRead float64
	// CacheWrite is a five-minute cache write, CacheWrite1h a one-hour
	// one. OpenAI lists no write price: its cached input bills as input.
	CacheWrite, CacheWrite1h float64
}

type modelRow struct {
	price  Price
	window int64 // context window in tokens, 0 when unknown
}

// models is every model pitwall knows a price or window for, by the id
// the agents write in their session files. A model missing here shows
// tokens and no cost. Anthropic bills cache writes at 1.25x input (5
// minutes) and 2x (1 hour). The OpenAI prices are the Codex pricing page's
// credits divided by its 25 credits per dollar of API price.
var models = map[string]modelRow{
	"claude-fable-5-1":  {Price{10, 50, 0.25, 12.5, 20}, 1_000_000},
	"claude-fable-5":    {Price{10, 50, 1, 12.5, 20}, 1_000_000},
	"claude-opus-5-5":   {Price{4, 20, 0.20, 5, 8}, 1_000_000},
	"claude-opus-5":     {Price{5, 25, 0.50, 6.25, 10}, 1_000_000},
	"claude-opus-4-8":   {Price{5, 25, 0.50, 6.25, 10}, 1_000_000},
	"claude-opus-4-7":   {Price{5, 25, 0.50, 6.25, 10}, 1_000_000},
	"claude-opus-4-6":   {Price{5, 25, 0.50, 6.25, 10}, 1_000_000},
	"claude-sonnet-5-5": {Price{2, 10, 0.20, 2.5, 4}, 1_000_000},
	"claude-sonnet-5":   {Price{2, 10, 0.20, 2.5, 4}, 1_000_000},
	"claude-sonnet-4-6": {Price{3, 15, 0.30, 3.75, 6}, 1_000_000},
	"claude-haiku-4-5":  {Price{1, 5, 0.10, 1.25, 2}, 200_000},

	"gpt-6.1-sol":  {Price{2, 10, 0.10, 2, 2}, 1_050_000},
	"gpt-6-sol":    {Price{2, 10, 0.20, 2, 2}, 1_050_000},
	"gpt-6-luna":   {Price{0.10, 0.50, 0.01, 0.10, 0.10}, 1_050_000},
	"gpt-6-astra":  {Price{10, 50, 1, 10, 10}, 0},
	"gpt-5.6-sol":  {Price{4, 20, 0.40, 4, 4}, 0},
	"gpt-5.6-luna": {Price{0.20, 1.20, 0.02, 0.20, 0.20}, 0},
}
