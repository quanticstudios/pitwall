// Package decide asks a decision model typed questions about a state and
// gets typed answers with probabilities back. TypeSafe's Jev is one
// provider; a user command speaking the same JSON is the other.
//
// Every call goes through Client, which redacts secrets from the state,
// limits each pane's call rate, cuts the call off after a short timeout and
// counts calls and errors per feature. A caller treats any error as "no
// decision" and carries on as it would without one.
package decide

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"sync"
	"time"
)

// Question types, as the TypeSafe API names them.
const (
	Choice = "choice" // pick one option; Criteria is map[string]string
	Score  = "score"  // rate on ordered levels; Criteria is []string, 2 to 10
	Noul   = "noul"   // yes or no; Criteria is nil or map with "true" and "false"
)

// Question is one typed question about the state.
type Question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Answer is the reply to one Question. Choice and Score answers carry
// Confidence and Probabilities (per option, or per level index as "0",
// "1", ...); a Noul answer carries Noul, the probability of yes.
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
}

// Request is what a provider evaluates: a state (text, or JSON built from
// maps, slices and strings) and named questions.
type Request struct {
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Provider answers every question of a request, keyed like the questions.
type Provider interface {
	Ask(ctx context.Context, r Request) (map[string]Answer, error)
}

// Feature names, used for counters and rate limits.
const (
	FeatureApprovals = "approvals"
	FeatureTriage    = "triage"
	FeatureAgents    = "agents"
	FeatureTurnCheck = "turn_check"
	FeatureTest      = "test"
)

// DefaultTimeout is how long a call may take before pitwall goes on
// without its answer.
const DefaultTimeout = 1500 * time.Millisecond

// Rate limit: at most perPane calls for one pane in window.
const (
	perPane = 30
	window  = time.Minute
)

// ErrRateLimited is returned when a pane has used up its calls for now.
var ErrRateLimited = errors.New("decision rate limit for this pane reached")

// Count is one feature's calls and errors on Day (local date).
type Count struct {
	Day           string
	Calls, Errors int
}

// Counters keep per-feature counts across Clients, so a config reload does
// not reset them. The zero value is ready.
type Counters struct {
	mu sync.Mutex
	m  map[string]Count
}

func (c *Counters) add(feature string, failed bool, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]Count{}
	}
	day := now.Format(time.DateOnly)
	n := c.m[feature]
	if n.Day != day {
		n = Count{Day: day}
	}
	n.Calls++
	if failed {
		n.Errors++
	}
	c.m[feature] = n
}

// Today returns each feature's counts for now's date.
func (c *Counters) Today(now time.Time) map[string]Count {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := map[string]Count{}
	day := now.Format(time.DateOnly)
	for f, n := range c.m {
		if n.Day == day {
			out[f] = n
		}
	}
	return out
}

// Client wraps a Provider with redaction, a timeout, a per-pane rate limit
// and counters. Its methods are safe for concurrent use.
type Client struct {
	P       Provider
	Timeout time.Duration // 0 means DefaultTimeout
	Counts  *Counters     // nil counts nothing
	// Secrets are exact strings to scrub besides what Redact finds, such
	// as the API key itself.
	Secrets []string
	Now     func() time.Time // nil means time.Now
	// Limit holds the per-pane call times; nil uses one of the Client's
	// own. Share one across Clients rebuilt per call.
	Limit *Limiter

	own Limiter
}

// Limiter caps calls per pane. The zero value is ready.
type Limiter struct {
	mu    sync.Mutex
	calls map[string][]time.Time // pane: recent call times
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// allow records a call for pane, or reports false when the pane has made
// perPane calls in the last window. An empty pane is never limited.
func (l *Limiter) allow(pane string, now time.Time) bool {
	if pane == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.calls == nil {
		l.calls = map[string][]time.Time{}
	}
	recent := slices.DeleteFunc(l.calls[pane], func(t time.Time) bool { return now.Sub(t) >= window })
	if len(recent) >= perPane {
		l.calls[pane] = recent
		return false
	}
	l.calls[pane] = append(recent, now)
	return true
}

// Forget drops a closed pane's call times.
func (l *Limiter) Forget(pane string) {
	l.mu.Lock()
	delete(l.calls, pane)
	l.mu.Unlock()
}

// Ask redacts state, asks the provider within the timeout, and checks
// every question got an answer of its type. On any error the answers are
// nil.
func (c *Client) Ask(ctx context.Context, feature, pane string, state any, qs map[string]Question) (map[string]Answer, error) {
	ans, _, err := c.AskMeta(ctx, feature, pane, state, qs)
	return ans, err
}

// Metered is a Provider that also reports the input tokens a request
// used, as Jev's reply does; 0 means it did not say.
type Metered interface {
	AskTokens(ctx context.Context, r Request) (map[string]Answer, int, error)
}

// Meta is what one call took and cost, for the decisions log. It holds
// no text.
type Meta struct {
	Took time.Duration
	// InputTokens is the provider's count, or with Estimated set the
	// request's JSON size / 4; 0 when nothing was sent.
	InputTokens int
	Estimated   bool
	// Err is the kind of failure, "" for an answer: rate_limited and
	// too_large (nothing sent), timeout, http, bad_reply (the reply did not
	// decode), bad_answer (an answer failed check), provider (anything else
	// the provider reported).
	Err string
}

// AskMeta is Ask, with what the call took and cost.
func (c *Client) AskMeta(ctx context.Context, feature, pane string, state any, qs map[string]Question) (map[string]Answer, Meta, error) {
	var m Meta
	if c == nil || c.P == nil {
		m.Err = "provider"
		return nil, m, errors.New("no decision provider")
	}
	now := c.now()
	l := c.Limit
	if l == nil {
		l = &c.own
	}
	if !l.allow(pane, now) {
		m.Err = "rate_limited"
		return nil, m, ErrRateLimited
	}
	t := c.Timeout
	if t <= 0 {
		t = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	var ans map[string]Answer
	prepared, _, err := c.Prepare(state)
	if err != nil {
		m.Err = "too_large"
	} else {
		r := Request{State: prepared, Questions: qs}
		start := time.Now()
		if mp, ok := c.P.(Metered); ok {
			ans, m.InputTokens, err = mp.AskTokens(ctx, r)
		} else {
			ans, err = c.P.Ask(ctx, r)
		}
		m.Took = time.Since(start)
		if m.InputTokens <= 0 {
			b, _ := json.Marshal(r)
			m.InputTokens, m.Estimated = len(b)/4, true
		}
		if err != nil {
			m.Err = providerErr(ctx, err)
		} else if err = check(qs, ans); err != nil {
			m.Err = "bad_answer"
		} else if ctx.Err() != nil {
			err, m.Err = errors.New("no answer within the timeout"), "timeout" // an answer after the deadline is not used
		} else {
			normalize(ans)
		}
	}
	if err != nil {
		err = errors.New(Redact(err.Error(), c.Secrets...))
		ans = nil
	}
	if c.Counts != nil {
		c.Counts.add(feature, err != nil, now)
	}
	return ans, m, err
}

// providerErr is the Meta.Err kind of a provider's error, read from the
// error's text, which Jev and Command write.
func providerErr(ctx context.Context, err error) string {
	s := err.Error()
	switch {
	case ctx.Err() != nil || strings.Contains(s, "within the timeout"):
		return "timeout"
	case strings.Contains(s, "HTTP "):
		return "http"
	case strings.Contains(s, "bad reply") || strings.Contains(s, "reply too large"):
		return "bad_reply"
	}
	return "provider"
}

// probTolerance is how far a distribution's sum may be from 1 before the
// answer is refused. Within it, normalize divides by the sum, so a
// threshold always applies to a distribution that sums to 1.
const probTolerance = 0.02

// normalize scales each answer's probabilities to sum to exactly 1.
func normalize(ans map[string]Answer) {
	for id, a := range ans {
		sum := 0.0
		for _, p := range a.Probabilities {
			sum += p
		}
		if sum <= 0 {
			continue
		}
		ps := make(map[string]float64, len(a.Probabilities))
		for k, p := range a.Probabilities {
			ps[k] = p / sum
		}
		a.Probabilities = ps
		ans[id] = a
	}
}

// check reports the first question without a well-formed answer. A
// choice or score answer needs a probability for every option or level
// and no other, each in [0, 1], summing to 1, with the chosen option at
// the top; anything else is no answer, so an inconsistent reply can never
// turn into an approval.
func check(qs map[string]Question, ans map[string]Answer) error {
	for _, id := range slices.Sorted(maps.Keys(qs)) {
		q := qs[id]
		a, ok := ans[id]
		switch {
		case !ok:
			return fmt.Errorf("no answer to %q", id)
		case a.Type != q.Type:
			return fmt.Errorf("answer to %q is a %s, want %s", id, a.Type, q.Type)
		}
		var keys []string
		switch a.Type {
		case Noul:
			if a.Noul < 0 || a.Noul > 1 {
				return fmt.Errorf("answer to %q is out of range", id)
			}
			continue
		case Choice:
			opts, _ := q.Criteria.(map[string]string)
			if _, ok := opts[a.Choice]; !ok {
				return fmt.Errorf("answer to %q is not one of its options", id)
			}
			keys = slices.Collect(maps.Keys(opts))
		case Score:
			levels, _ := q.Criteria.([]string)
			if a.Score < 0 || a.Score > float64(len(levels)-1) {
				return fmt.Errorf("answer to %q is out of range", id)
			}
			for i := range levels {
				keys = append(keys, fmt.Sprint(i))
			}
		}
		if len(a.Probabilities) != len(keys) {
			return fmt.Errorf("answer to %q lacks probabilities", id)
		}
		sum, top := 0.0, 0.0
		for _, k := range keys {
			p, ok := a.Probabilities[k]
			if !ok || p < 0 || p > 1 || math.IsNaN(p) {
				return fmt.Errorf("answer to %q has a bad probability", id)
			}
			sum, top = sum+p, max(top, p)
		}
		if math.Abs(sum-1) > probTolerance {
			return fmt.Errorf("answer to %q has probabilities that do not sum to 1", id)
		}
		if a.Type == Choice && a.Probabilities[a.Choice] < top {
			return fmt.Errorf("answer to %q picks an option it rates lower than another", id)
		}
		if a.Confidence < 0 || a.Confidence > 1 || math.IsNaN(a.Confidence) {
			return fmt.Errorf("answer to %q has a bad confidence", id)
		}
	}
	return nil
}
