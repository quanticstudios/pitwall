// Package forge asks the gh CLI for the pull request of a branch, and
// decides how soon to ask again.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/model"
	"github.com/quanticstudios/pitwall/internal/nowindow"
)

var (
	// ErrNoGH is gh missing, logged out, or the repo not on GitHub: there
	// is nothing to show.
	ErrNoGH = errors.New("gh cannot answer: missing, logged out, or no GitHub remote")
	// ErrRateLimited is GitHub refusing for its rate limit.
	ErrRateLimited = errors.New("GitHub rate limit")
)

// Fields is what View asks gh for.
const Fields = "number,state,isDraft,url,reviewDecision,mergeable,statusCheckRollup"

// View is the pull request of the branch checked out at dir, nil when it
// has none. gh never prompts: it runs without a terminal and with prompts
// disabled.
func View(ctx context.Context, dir string) (*model.PR, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, ErrNoGH
	}
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", "--json", Fields)
	cmd.Dir = dir
	nowindow.Set(cmd)
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return answer(out, stderr.Bytes(), err)
}

// answer reads gh pr view's output: the PR on stdout when gh succeeded,
// else what its stderr and exit code mean.
func answer(stdout, stderr []byte, err error) (*model.PR, error) {
	if err == nil {
		pr, err := Parse(stdout)
		if err != nil {
			return nil, err
		}
		return &pr, nil
	}
	msg := strings.ToLower(string(stderr))
	var exit *exec.ExitError
	switch {
	case strings.Contains(msg, "no pull requests found"):
		return nil, nil
	case strings.Contains(msg, "rate limit"):
		return nil, ErrRateLimited
	case errors.As(err, &exit) && exit.ExitCode() == 4, // gh's "authentication required"
		strings.Contains(msg, "gh auth login"),
		strings.Contains(msg, "none of the git remotes"),
		strings.Contains(msg, "not a git repository"):
		return nil, ErrNoGH
	}
	return nil, fmt.Errorf("gh pr view: %s: %w", strings.TrimSpace(string(stderr)), err)
}

// ghPR is gh pr view --json Fields.
type ghPR struct {
	Number         int    `json:"number"`
	State          string `json:"state"`
	IsDraft        bool   `json:"isDraft"`
	URL            string `json:"url"`
	ReviewDecision string `json:"reviewDecision"`
	Mergeable      string `json:"mergeable"`
	Checks         []struct {
		Type       string `json:"__typename"` // CheckRun or StatusContext
		Name       string `json:"name"`       // a CheckRun's
		Context    string `json:"context"`    // a StatusContext's name
		Status     string `json:"status"`     // a CheckRun's: QUEUED, IN_PROGRESS, COMPLETED...
		Conclusion string `json:"conclusion"` // a completed CheckRun's
		State      string `json:"state"`      // a StatusContext's
	} `json:"statusCheckRollup"`
}

// Parse reads gh pr view --json Fields.
func Parse(data []byte) (model.PR, error) {
	var g ghPR
	if err := json.Unmarshal(data, &g); err != nil {
		return model.PR{}, err
	}
	pr := model.PR{Number: g.Number, State: model.PRState(strings.ToLower(g.State)), Draft: g.IsDraft, URL: g.URL, Conflicts: g.Mergeable == "CONFLICTING"}
	switch g.ReviewDecision {
	case "APPROVED":
		pr.Review = model.ReviewApproved
	case "CHANGES_REQUESTED":
		pr.Review = model.ReviewChanges
	case "REVIEW_REQUIRED":
		pr.Review = model.ReviewRequired
	}
	for _, c := range g.Checks {
		ch := model.Check{Name: c.Name}
		if c.Type == "StatusContext" {
			ch.Name = c.Context
			switch c.State {
			case "SUCCESS":
				ch.State = model.CheckPass
			case "PENDING", "EXPECTED":
				ch.State = model.CheckPending
			default: // FAILURE, ERROR
				ch.State = model.CheckFail
			}
		} else {
			// As gh pr checks buckets them, but cancelled counts as failed:
			// it did not pass, and gh run rerun --failed runs it again.
			switch {
			case c.Status != "COMPLETED":
				ch.State = model.CheckPending
			case c.Conclusion == "SUCCESS":
				ch.State = model.CheckPass
			case c.Conclusion == "NEUTRAL" || c.Conclusion == "SKIPPED":
				ch.State = model.CheckSkip
			default: // FAILURE, CANCELLED, TIMED_OUT, ACTION_REQUIRED, STARTUP_FAILURE, STALE
				ch.State = model.CheckFail
			}
		}
		pr.Checks = append(pr.Checks, ch)
	}
	return pr, nil
}

// How long View waits before asking about the same branch again.
const (
	OpenEvery  = time.Minute      // an open PR, whose checks and review move
	QuietEvery = 5 * time.Minute  // no PR, or a closed one
	NoGHEvery  = 15 * time.Minute // gh missing, logged out, or no GitHub remote
	// MergedEvery is for a merged PR, which is final: only a change of
	// branch, or a gh command ending in one of its tabs, asks sooner.
	MergedEvery = 24 * time.Hour
	// RateLimitPause stops every View once GitHub says the rate limit is hit.
	RateLimitPause = 15 * time.Minute
	// MaxBackoff caps the wait after errors in a row, which doubles from a
	// minute.
	MaxBackoff = 30 * time.Minute
	// HiddenTimes stretches the open, quiet, no-gh and error waits while no
	// window has focus.
	HiddenTimes = 5
)

// Next is how long after View answered pr and err to run it again for the
// same branch. fails is how many answers in a row, this one included, were
// errors other than ErrNoGH and ErrRateLimited; hidden is whether no window
// has focus.
func Next(pr *model.PR, err error, fails int, hidden bool) time.Duration {
	var d time.Duration
	switch {
	case errors.Is(err, ErrRateLimited):
		return RateLimitPause
	case errors.Is(err, ErrNoGH):
		d = NoGHEvery
	case err != nil:
		d = min(time.Minute<<min(max(fails-1, 0), 5), MaxBackoff)
	case pr != nil && pr.State == model.PRMerged:
		return MergedEvery
	case pr != nil && pr.State == model.PROpen:
		d = OpenEvery
	default:
		d = QuietEvery
	}
	if hidden {
		d *= HiddenTimes
	}
	return d
}
