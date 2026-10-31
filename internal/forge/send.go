package forge

import (
	"context"
	"strconv"
)

// SendPrompt fetches the prompt for Send failures or Send review comments.
// The window's startSend and daemon automation share this path. Fetchers
// are arguments so both callers can test without GitHub.
func SendPrompt(ctx context.Context, dir string, number int, act string,
	failures func(context.Context, string) ([]FailedCheck, error),
	reviews func(context.Context, string, int) ([]Thread, []Comment, error),
) (string, error) {
	if act == "send_failures" {
		checks, err := failures(ctx, dir)
		if err != nil || len(checks) == 0 {
			return "", err
		}
		return FailuresPrompt(number, checks), nil
	}
	threads, changes, err := reviews(ctx, dir, number)
	if err != nil {
		return "", err
	}
	return ReviewPrompt(number, threads, changes), nil
}

// Merge merges pull request number of the repository at dir by method,
// squash, merge or rebase, as the merge dialog's gh pr merge does.
func Merge(ctx context.Context, dir string, number int, method string) error {
	_, err := gh(ctx, dir, "pr", "merge", strconv.Itoa(number), "--"+method)
	return err
}
