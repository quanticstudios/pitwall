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
	"errors"
	"fmt"
	"maps"
	"slices"
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
func (c *Client) allow(pane string, now time.Time) bool {
	if pane == "" {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls == nil {
		c.calls = map[string][]time.Time{}
	}
	recent := slices.DeleteFunc(c.calls[pane], func(t time.Time) bool { return now.Sub(t) >= window })
	if len(recent) >= perPane {
		c.calls[pane] = recent
		return false
	}
	c.calls[pane] = append(recent, now)
	return true
}

// Ask redacts state, asks the provider within the timeout, and checks
// every question got an answer of its type. On any error the answers are
// nil.
func (c *Client) Ask(ctx context.Context, feature, pane string, state any, qs map[string]Question) (map[string]Answer, error) {
	if c == nil || c.P == nil {
		return nil, errors.New("no decision provider")
	}
	now := c.now()
	if !c.allow(pane, now) {
		return nil, ErrRateLimited
	}
	t := c.Timeout
	if t <= 0 {
		t = DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, t)
	defer cancel()
	ans, err := c.P.Ask(ctx, Request{State: RedactValue(state, c.Secrets...), Questions: qs})
	if err == nil {
		err = check(qs, ans)
	}
	if err != nil {
		err = errors.New(Redact(err.Error(), c.Secrets...))
		ans = nil
	}
	if c.Counts != nil {
		c.Counts.add(feature, err != nil, now)
	}
	return ans, err
}

// check reports the first question without a well-formed answer.
func check(qs map[string]Question, ans map[string]Answer) error {
	for _, id := range slices.Sorted(maps.Keys(qs)) {
		q := qs[id]
		a, ok := ans[id]
		switch {
		case !ok:
			return fmt.Errorf("no answer to %q", id)
		case a.Type != q.Type:
			return fmt.Errorf("answer to %q is a %s, want %s", id, a.Type, q.Type)
		case a.Type == Noul && (a.Noul < 0 || a.Noul > 1):
			return fmt.Errorf("answer to %q is out of range", id)
		case a.Type == Choice:
			opts, _ := q.Criteria.(map[string]string)
			if _, ok := opts[a.Choice]; !ok {
				return fmt.Errorf("answer to %q is not one of its options", id)
			}
		case a.Type == Score:
			levels, _ := q.Criteria.([]string)
			if a.Score < 0 || a.Score > float64(len(levels)-1) {
				return fmt.Errorf("answer to %q is out of range", id)
			}
		}
		for _, p := range a.Probabilities {
			if p < 0 || p > 1 {
				return fmt.Errorf("answer to %q has a probability out of range", id)
			}
		}
	}
	return nil
}
