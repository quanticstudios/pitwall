package gitstat

import (
	"context"
	"strings"
)

// Head is the commit checked out at path.
func Head(ctx context.Context, path string) (string, error) {
	out, err := git(ctx, path, "rev-parse", "--verify", "HEAD")
	return strings.TrimSpace(out), err
}

// UnchangedSince reports whether path has no new commits or changed or untracked files since base.
func UnchangedSince(ctx context.Context, path, base string) (bool, error) {
	files, err := Status(ctx, path)
	if err != nil || len(files) > 0 {
		return false, err
	}
	_, err = git(ctx, path, "merge-base", "--is-ancestor", "--", "HEAD", base)
	if exitCode(err) == 1 && ctx.Err() == nil {
		return false, nil
	}
	return err == nil, err
}
