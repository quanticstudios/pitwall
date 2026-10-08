package flow

import (
	"maps"
	"regexp"
	"time"
)

// Tokens are one model's token counts. Input is what was sent and neither
// read from nor written to the prompt cache.
type Tokens struct {
	Input, Output, CacheRead, CacheWrite int64
	// CacheWrite1h is the part of CacheWrite cached for an hour, which
	// Anthropic bills at twice the input price rather than 1.25 times.
	CacheWrite1h int64
}

// Total is every token sent and received.
func (t Tokens) Total() int64 { return t.Input + t.Output + t.CacheRead + t.CacheWrite }

func (t Tokens) plus(o Tokens) Tokens {
	return Tokens{t.Input + o.Input, t.Output + o.Output, t.CacheRead + o.CacheRead, t.CacheWrite + o.CacheWrite, t.CacheWrite1h + o.CacheWrite1h}
}

func (t Tokens) minus(o Tokens) Tokens {
	return Tokens{t.Input - o.Input, t.Output - o.Output, t.CacheRead - o.CacheRead, t.CacheWrite - o.CacheWrite, t.CacheWrite1h - o.CacheWrite1h}
}

// Usage is what a session's model calls used, its subagents' included.
type Usage struct {
	// Models holds the tokens by model id. A Claude call in fast mode
	// counts under the id with " (fast)" after it. nil before the first
	// call with usage.
	Models map[string]Tokens
	Model  string // the latest call's model
	// Context is how many tokens the main session's latest call sent: the
	// conversation as the model saw it. 0 when unknown.
	Context int64
	// Window is the context window the session file reports (Codex); 0
	// when it reports none, and ContextWindow is the fallback.
	Window int64
	// Partial is set when only the end of a long Claude or pi file was
	// read, so the counts miss its start. Codex reports running totals.
	Partial bool
}

// Tokens is the sum over every model.
func (u Usage) Tokens() Tokens {
	var t Tokens
	for _, m := range u.Models {
		t = t.plus(m)
	}
	return t
}

// Fill is Context as a share of the model's context window, and false
// when either is unknown or the window looks wrong.
func (u Usage) Fill() (float64, bool) {
	w := u.Window
	if w == 0 {
		w = ContextWindow(u.Model)
	}
	if w <= 0 || u.Context <= 0 || u.Context > w {
		return 0, false
	}
	return float64(u.Context) / float64(w), true
}

// Cost is what the tokens cost at the list prices of PricesAsOf, and false
// when any model with tokens has no price: no cost beats a made-up one.
func (u Usage) Cost() (float64, bool) {
	sum := 0.0
	for id, t := range u.Models {
		if t == (Tokens{}) {
			continue
		}
		p, ok := PriceOf(id)
		if !ok {
			return 0, false
		}
		sum += p.cost(t)
	}
	return sum, len(u.Models) > 0
}

// Add merges o into u: the tokens add up, and o's latest call stands in
// for both when u has none or o's context is the fuller one.
func (u *Usage) Add(o Usage) {
	if len(o.Models) > 0 && u.Models == nil {
		u.Models = map[string]Tokens{}
	}
	for id, t := range o.Models {
		u.Models[id] = u.Models[id].plus(t)
	}
	fu, _ := u.Fill()
	if fo, _ := o.Fill(); u.Model == "" || fo > fu {
		u.Model, u.Context, u.Window = o.Model, o.Context, o.Window
	}
	u.Partial = u.Partial || o.Partial
}

func (u Usage) clone() Usage {
	u.Models = maps.Clone(u.Models)
	return u
}

// use counts the tokens of one call at ts; context and window are 0 when
// unknown. A call before the builder's since is a forked child's copy of
// its parent's history, already counted there.
func (b *builder) use(ts time.Time, model string, t Tokens, context, window int64) {
	if ts.Before(b.since) {
		return
	}
	if model == "" {
		model = "unknown model" // a Codex file read from past its turn_context
	}
	if b.usage.Models == nil {
		b.usage.Models = map[string]Tokens{}
	}
	b.usage.Models[model] = b.usage.Models[model].plus(t)
	if context > 0 {
		b.usage.Model, b.usage.Context, b.usage.Window = model, context, window
	}
}

var dated = regexp.MustCompile(`-\d{8}$`)

// modelOf is a model id's row in models: as written, then without a
// release date ("claude-haiku-4-5-20251001").
func modelOf(id string) (modelRow, bool) {
	if r, ok := models[id]; ok {
		return r, true
	}
	r, ok := models[dated.ReplaceAllString(id, "")]
	return r, ok
}

// ContextWindow is a known model's context window in tokens, else 0.
func ContextWindow(model string) int64 {
	if m := fastNote.FindStringIndex(model); m != nil {
		model = model[:m[0]]
	}
	r, _ := modelOf(model)
	return r.window
}

var fastNote = regexp.MustCompile(` \(fast\)$`)

// PriceOf is a model's list price, and false for a model the table does
// not have.
func PriceOf(model string) (Price, bool) {
	r, ok := modelOf(model)
	return r.price, ok && r.price != (Price{})
}

func (p Price) cost(t Tokens) float64 {
	w5 := t.CacheWrite - t.CacheWrite1h
	return (float64(t.Input)*p.Input + float64(t.Output)*p.Output + float64(t.CacheRead)*p.CacheRead +
		float64(w5)*p.CacheWrite + float64(t.CacheWrite1h)*p.CacheWrite1h) / 1e6
}
