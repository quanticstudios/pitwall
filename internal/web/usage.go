package web

// The readUsage command (docs/web-protocol.md, readUsage): what Settings, Usage
// shows, read the way it reads it.

import (
	"encoding/json"
	"time"

	"github.com/quanticstudios/pitwall/internal/flow"
)

// Usage is what readUsage reads.
type Usage struct {
	Limits   []flow.Limit  // the latest report of each plan limit
	Records  []flow.Record // the session files' calls since the window's first day
	ShowCost bool          // [usage] show_cost
}

const (
	usageDays    = 30 // the window when the command names none
	maxUsageDays = 90
	// topSessions is how many of the window's sessions the answer lists.
	topSessions = 10
)

// usage runs readUsage; false for any other. Reading the session
// files can take seconds, so it answers from its own goroutine.
func (s *Server) usage(c *conn, typ, id string, msg []byte) bool {
	if typ != "readUsage" || s.Usage == nil {
		return false
	}
	var f struct {
		Days *int `json:"days"`
	}
	days := usageDays
	if json.Unmarshal(msg, &f) != nil || f.Days != nil && (*f.Days < 1 || *f.Days > maxUsageDays) {
		c.reply(result(id, "invalid", "days is a whole number from 1 to 90"))
		return true
	}
	if f.Days != nil {
		days = *f.Days
	}
	go func() {
		now := time.Now()
		c.reply(ok(id, "", map[string]any{"usage": usageData(s.Usage(flow.Since(now, days)), now, days)}))
	}()
	return true
}

// usageData is u's report over the days days ending on now's local day.
func usageData(u Usage, now time.Time, days int) map[string]any {
	r := flow.NewReport(u.Records, now, days)
	limits := []map[string]any{}
	for _, l := range u.Limits {
		windows := []map[string]any{}
		for _, w := range l.Windows {
			m := map[string]any{"minutes": w.Minutes, "used": w.Used}
			if !w.Resets.IsZero() {
				m["resets"] = w.Resets.UTC()
			}
			windows = append(windows, m)
		}
		limits = append(limits, map[string]any{"provider": l.Provider, "profile": l.Profile, "profileLabel": l.ProfileLabel,
			"name": l.Name, "seen": l.Seen.UTC(), "windows": windows})
	}
	daily := make([]map[string]any, len(r.Days))
	for i, d := range r.Days {
		daily[i] = map[string]any{"date": d.Format(time.DateOnly), "tokens": r.Daily[i].Tokens.Total(), "cost": r.Daily[i].Cost}
	}
	sessions := []map[string]any{}
	for _, su := range r.BySession[:min(len(r.BySession), topSessions)] {
		sessions = append(sessions, map[string]any{"provider": su.Provider, "sessionId": su.Session, "last": su.Last.UTC(),
			"tokens": su.Tokens.Total(), "cost": su.Cost})
	}
	return map[string]any{
		"showCost": u.ShowCost, "pricesAsOf": flow.PricesAsOf, "limits": limits, "days": daily, "sessions": sessions,
		"total": map[string]any{"sessions": r.Sessions, "tokens": r.Tokens.Total(), "cost": r.Cost, "unpriced": r.Unpriced},
	}
}
