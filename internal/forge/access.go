package forge

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/quanticstudios/pitwall/internal/gitstat"
)

// PushAccess returns why gh in dir could not open a pull request on the
// GitHub repository origin points at, nil when its account may push there.
// repo is that repository, "" when origin is not on GitHub, which needs
// no gh.
func PushAccess(ctx context.Context, dir string) (repo string, err error) {
	out, err := gitstat.Command(ctx, dir, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return "", nil // no origin: the git checks report it
	}
	if repo, err = githubRepository(strings.TrimSpace(string(out))); err != nil {
		return "", nil
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return repo, fmt.Errorf("gh is not installed")
	}
	cmd := ghCmd(ctx, dir, "api", "repos/"+repo, "--jq", ".permissions.push")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err = cmd.Output()
	if err != nil {
		msg, _, _ := strings.Cut(strings.TrimSpace(stderr.String()), "\n")
		if msg == "" {
			msg = err.Error()
		}
		return repo, fmt.Errorf("gh cannot read %s: %s", repo, msg)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return repo, fmt.Errorf("gh's account cannot push to %s", repo)
	}
	return repo, nil
}
