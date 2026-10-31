package flow

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Report is what the agents' calls over a window of days cost, at the list
// prices of PricesAsOf. Costs count priced models only.
type Report struct {
	Days     []time.Time // each day's local midnight, oldest first
	Cost     float64
	Sessions int
	Tokens   Tokens
	// Unpriced is the tokens of models without a price.
	Unpriced int64
	// Savings is what the cache reads would have cost as input, less what
	// they cost.
	Savings   float64
	Providers []ProviderUse // by cost, then tokens
	Models    []ModelUse    // by cost, then tokens
	Daily     []DayUse      // one per day of Days
	BySession []SessionUse  // by tokens, then cost
}

// ProviderUse is one agent's part of a Report.
type ProviderUse struct {
	Provider model.Provider
	Sessions int
	Cost     float64
	Tokens   Tokens
	Daily    []float64 // cost by day of Report.Days
}

// ModelUse is one model's part of a Report.
type ModelUse struct {
	Provider model.Provider // the first agent seen calling it
	Model    string
	Priced   bool
	Cost     float64
	Tokens   Tokens
}

// SessionUse is one session's part of a Report.
type SessionUse struct {
	Provider model.Provider
	Session  string    // the agent's session id, its file's name
	Last     time.Time // its latest call in the window
	Cost     float64
	Tokens   Tokens
}

// DayUse is one day's part of a Report.
type DayUse struct {
	Cost   float64
	Tokens Tokens
}

// Since is the local midnight that starts a window of days ending today.
func Since(now time.Time, days int) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d-days+1, 0, 0, 0, 0, now.Location())
}

// NewReport adds up recs over the days days ending on now's local day.
// Calls outside them are left out.
func NewReport(recs []Record, now time.Time, days int) Report {
	since := Since(now, days)
	r := Report{Daily: make([]DayUse, days)}
	for i := range days {
		y, m, d := since.Date()
		r.Days = append(r.Days, time.Date(y, m, d+i, 0, 0, 0, 0, since.Location()))
	}
	type session struct {
		p  model.Provider
		id string
	}
	sessions := map[session]*SessionUse{}
	provs := map[model.Provider]*ProviderUse{}
	models := map[string]*ModelUse{}
	for _, rec := range recs {
		t := rec.Time.In(since.Location())
		y, m, d := t.Date()
		// Rounded, as a day across a DST change is 23 or 25 hours.
		i := int(math.Round(time.Date(y, m, d, 0, 0, 0, 0, since.Location()).Sub(since).Hours() / 24))
		if i < 0 || i >= days {
			continue
		}
		price, priced := PriceOf(rec.Model)
		cost := 0.0
		if priced {
			cost = price.cost(rec.Tokens)
			r.Savings += float64(rec.CacheRead) * (price.Input - price.CacheRead) / 1e6
		} else {
			r.Unpriced += rec.Total()
		}
		r.Cost += cost
		r.Tokens = r.Tokens.plus(rec.Tokens)
		r.Daily[i].Cost += cost
		r.Daily[i].Tokens = r.Daily[i].Tokens.plus(rec.Tokens)

		pu := provs[rec.Provider]
		if pu == nil {
			pu = &ProviderUse{Provider: rec.Provider, Daily: make([]float64, days)}
			provs[rec.Provider] = pu
		}
		su := sessions[session{rec.Provider, rec.Session}]
		if su == nil {
			su = &SessionUse{Provider: rec.Provider, Session: rec.Session}
			sessions[session{rec.Provider, rec.Session}] = su
			pu.Sessions++
			r.Sessions++
		}
		su.Cost += cost
		su.Tokens = su.Tokens.plus(rec.Tokens)
		if rec.Time.After(su.Last) {
			su.Last = rec.Time
		}
		pu.Cost += cost
		pu.Tokens = pu.Tokens.plus(rec.Tokens)
		pu.Daily[i] += cost

		mu := models[rec.Model]
		if mu == nil {
			mu = &ModelUse{Provider: rec.Provider, Model: rec.Model, Priced: priced}
			models[rec.Model] = mu
		}
		mu.Cost += cost
		mu.Tokens = mu.Tokens.plus(rec.Tokens)
	}
	for _, pu := range provs {
		r.Providers = append(r.Providers, *pu)
	}
	slices.SortFunc(r.Providers, func(a, b ProviderUse) int {
		return cmp.Or(cmp.Compare(b.Cost, a.Cost), cmp.Compare(b.Tokens.Total(), a.Tokens.Total()), strings.Compare(string(a.Provider), string(b.Provider)))
	})
	for _, mu := range models {
		r.Models = append(r.Models, *mu)
	}
	slices.SortFunc(r.Models, func(a, b ModelUse) int {
		return cmp.Or(cmp.Compare(b.Cost, a.Cost), cmp.Compare(b.Tokens.Total(), a.Tokens.Total()), strings.Compare(a.Model, b.Model))
	})
	for _, su := range sessions {
		r.BySession = append(r.BySession, *su)
	}
	slices.SortFunc(r.BySession, func(a, b SessionUse) int {
		return cmp.Or(cmp.Compare(b.Tokens.Total(), a.Tokens.Total()), cmp.Compare(b.Cost, a.Cost), strings.Compare(a.Session, b.Session))
	})
	return r
}
