package flow

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
)

// Limit is one plan limit as its agent last reported it: Claude Code's
// 5-hour and weekly windows, or a Codex limit's.
type Limit struct {
	Provider model.Provider `json:"provider"`
	// Name is Codex's name for a limit of a model's own, "" for the
	// agent's main limit.
	Name    string    `json:"name,omitempty"`
	Seen    time.Time `json:"seen"` // when the agent reported it
	Windows []Window  `json:"windows"`
}

// Window is one window of a limit.
type Window struct {
	Minutes int64     `json:"minutes"` // its length
	Used    float64   `json:"used"`    // percent, 0 to 100
	Resets  time.Time `json:"resets"`  // zero when unknown
}

// Now is w at now, of a report seen then: a window that reset since is
// unused, with its next reset unknown.
func (w Window) Now(now time.Time) Window {
	if !w.Resets.IsZero() && !now.Before(w.Resets) {
		return Window{Minutes: w.Minutes}
	}
	return w
}

// Hits is when w reaches 100% if its use goes on at its pace so far, from
// its start to seen. It is zero when that is not before its reset, when w
// is too young to tell, or when its start is unknown.
func (w Window) Hits(seen time.Time) time.Time {
	if w.Used <= 0 || w.Used >= 100 || w.Minutes <= 0 || w.Resets.IsZero() {
		return time.Time{}
	}
	length := time.Duration(w.Minutes) * time.Minute
	elapsed := seen.Sub(w.Resets.Add(-length))
	// The pace of the first twentieth of a window says little.
	if elapsed < length/20 {
		return time.Time{}
	}
	hit := seen.Add(time.Duration(float64(elapsed) * (100 - w.Used) / w.Used))
	if !hit.Before(w.Resets) {
		return time.Time{}
	}
	return hit
}

// Tightest is l's most used window at now, after resets.
func (l Limit) Tightest(now time.Time) (Window, bool) {
	var top Window
	for i, w := range l.Windows {
		if w = w.Now(now); i == 0 || w.Used > top.Used {
			top = w
		}
	}
	return top, len(l.Windows) > 0
}

// ClaudeLimitsFile is the file in pitwall's state directory that pitwall
// statusline keeps Claude Code's latest limits in.
const ClaudeLimitsFile = "claude-limits.json"

// Statusline reads the limits in a Claude Code statusline's JSON, seen at
// now. ok is false when it has none: before a session's first reply, and
// on a plan without them.
func Statusline(data []byte, now time.Time) (l Limit, ok bool) {
	type window struct {
		Used   *float64 `json:"used_percentage"`
		Resets int64    `json:"resets_at"`
	}
	var in struct {
		RateLimits struct {
			FiveHour *window `json:"five_hour"`
			SevenDay *window `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(data, &in) != nil {
		return l, false
	}
	l = Limit{Provider: model.ProviderClaude, Seen: now}
	for _, w := range []struct {
		w       *window
		minutes int64
	}{{in.RateLimits.FiveHour, 5 * 60}, {in.RateLimits.SevenDay, 7 * 24 * 60}} {
		if w.w == nil || w.w.Used == nil {
			continue
		}
		win := Window{Minutes: w.minutes, Used: *w.w.Used}
		if w.w.Resets > 0 {
			win.Resets = time.Unix(w.w.Resets, 0)
		}
		l.Windows = append(l.Windows, win)
	}
	return l, len(l.Windows) > 0
}

// codexLimits is a token_count's rate_limits. Older Codex gave each
// window's reset as resets_in_seconds from the event.
type codexLimits struct {
	ID        string       `json:"limit_id"`
	Name      string       `json:"limit_name"`
	Primary   *codexWindow `json:"primary"`
	Secondary *codexWindow `json:"secondary"`
}

type codexWindow struct {
	Used     float64 `json:"used_percent"`
	Minutes  int64   `json:"window_minutes"`
	ResetsAt int64   `json:"resets_at"`
	ResetsIn *int64  `json:"resets_in_seconds"`
}

// limit is r as a Limit seen at ts.
func (r *codexLimits) limit(ts time.Time) (Limit, bool) {
	l := Limit{Provider: model.ProviderCodex, Seen: ts}
	if r.ID != "" && r.ID != "codex" {
		l.Name = cmp.Or(r.Name, r.ID)
	}
	for _, w := range []*codexWindow{r.Primary, r.Secondary} {
		if w == nil {
			continue
		}
		win := Window{Minutes: w.Minutes, Used: w.Used}
		switch {
		case w.ResetsAt > 0:
			win.Resets = time.Unix(w.ResetsAt, 0)
		case w.ResetsIn != nil:
			win.Resets = ts.Add(time.Duration(*w.ResetsIn) * time.Second)
		}
		l.Windows = append(l.Windows, win)
	}
	return l, len(l.Windows) > 0
}

// Limits is the latest report of each plan limit: Codex's from the files
// the last Scan read, Claude Code's from the ClaudeLimitsFile in stateDir.
// Claude's first, then by name.
func (s *Scanner) Limits(stateDir string) []Limit {
	latest := map[string]Limit{}
	keep := func(l Limit) {
		k := string(l.Provider) + "\x00" + l.Name
		if old, ok := latest[k]; !ok || l.Seen.After(old.Seen) {
			latest[k] = l
		}
	}
	if data, err := os.ReadFile(filepath.Join(stateDir, ClaudeLimitsFile)); err == nil {
		var l Limit
		if json.Unmarshal(data, &l) == nil && l.Provider == model.ProviderClaude && len(l.Windows) > 0 {
			keep(l)
		}
	}
	s.mu.Lock()
	for _, f := range s.files {
		for _, l := range f.limits {
			keep(l)
		}
	}
	s.mu.Unlock()
	var out []Limit
	for _, l := range latest {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b Limit) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.Name, b.Name))
	})
	return out
}

// CodexDays are the Sources of Codex's rollouts of the days days ending on
// now's local day, which are where its latest limits are.
func CodexDays(home string, now time.Time, days int) []Source {
	var srcs []Source
	for i := range days {
		srcs = append(srcs, Source{model.ProviderCodex, filepath.Join(home, ".codex", "sessions", now.AddDate(0, 0, -i).Format("2006/01/02"))})
	}
	return srcs
}
