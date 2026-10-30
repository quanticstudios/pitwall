package forge

import "context"

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
