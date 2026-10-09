package app

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/flow"
	"github.com/quanticstudios/pitwall/internal/ui/sidebar"
)

const (
	// limitsEvery is how often a window reads the agents' plan limits.
	limitsEvery = time.Minute
	// limitsDays is how many days of Codex rollouts it reads them from.
	limitsDays = 8
	// limitNoticeAt is the percent of a window that shows the notice.
	limitNoticeAt = 90
)

// limitWatch keeps the plan limits that Codex's rollouts and pitwall
// statusline report, for Settings, Usage, the sidebar's meter and the
// notice a window at limitNoticeAt shows once.
type limitWatch struct {
	scan flow.Scanner // Codex's latest days only

	mu     sync.Mutex
	limits []flow.Limit
	fired  map[string]bool // the windows a notice was shown for, by limitKey
	notice string          // "" for none
	key    string          // the window notice is for
}

// watch reads the limits now and every limitsEvery until stop closes.
func (lw *limitWatch) watch(stop <-chan struct{}, invalidate func()) {
	home, _ := os.UserHomeDir()
	t := time.NewTicker(limitsEvery)
	defer t.Stop()
	for {
		now := time.Now()
		lw.scan.Scan(flow.CodexDays(home, now, limitsDays), now.AddDate(0, 0, -limitsDays))
		lw.set(lw.scan.Limits(config.StateDir()), now)
		invalidate()
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// get returns the latest limits; nil before the first read.
func (lw *limitWatch) get() []flow.Limit {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.limits
}

// shown is the notice to draw, "" for none.
func (lw *limitWatch) shown() string {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return lw.notice
}

// set takes a read's limits: a window at limitNoticeAt or more that had
// no notice gets one, and the notice goes once its window resets.
func (lw *limitWatch) set(limits []flow.Limit, now time.Time) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	lw.limits = limits
	if lw.fired == nil {
		lw.fired = map[string]bool{}
	}
	still := false
	for _, l := range limits {
		for _, w := range l.Windows {
			k := limitKey(l, w)
			if w.Now(now).Used < limitNoticeAt {
				continue
			}
			still = still || k == lw.key
			if !lw.fired[k] {
				lw.fired[k] = true
				lw.notice, lw.key, still = limitNotice(l, w, now), k, true
			}
		}
	}
	if !still {
		lw.notice, lw.key = "", ""
	}
}

// limitKey names one cycle of a window. Its reset time can drift by
// seconds between reports, so it counts in ten minutes.
func limitKey(l flow.Limit, w flow.Window) string {
	return fmt.Sprint(l.Provider, "/", l.Name, "/", w.Minutes, "/", w.Resets.Round(10*time.Minute).Unix())
}

// limitNotice is "Claude 5-hour window at 92%, resets 16:40".
func limitNotice(l flow.Limit, w flow.Window, now time.Time) string {
	s := fmt.Sprintf("%s %s window at %.0f%%", sidebar.LimitName(l), sidebar.WindowName(w.Minutes), w.Used)
	if !w.Resets.IsZero() {
		s += ", resets " + sidebar.Clock(w.Resets, now)
	}
	return s
}
