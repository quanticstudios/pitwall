package decide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Command runs a program per request: the request JSON on stdin, the reply
// on stdout in the shape Jev answers with, {"answers": {...}}. It lets a
// local classifier or another service stand in for Jev.
type Command struct {
	Argv []string
	// Secrets are the known keys, scrubbed from the command's error text
	// before anything else touches it.
	Secrets Secrets
}

// Ask runs the command, without the TypeSafe key in its environment.
// When ctx ends, the command's process group is killed on Unix; a child
// that leaves the group (setsid, daemonizing) is the provider's to stop.
// On Windows only the command itself is killed. Output pipes a stray
// child still holds are let go after WaitDelay.
func (c Command) Ask(ctx context.Context, r Request) (map[string]Answer, error) {
	ans, _, err := c.AskTokens(ctx, r)
	return ans, err
}

// AskTokens is Ask that also returns the input tokens the reply's usage
// reports, as Jev's does, or 0.
func (c Command) AskTokens(ctx context.Context, r Request) (map[string]Answer, int, error) {
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return nil, 0, errors.New("decisions.command is empty")
	}
	in, err := json.Marshal(r)
	if err != nil {
		return nil, 0, err
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Env = withoutCredentials(os.Environ())
	ownGroup(cmd)
	cmd.WaitDelay = 500 * time.Millisecond // stop waiting on pipes a stray child still holds
	cmd.Stdin = bytes.NewReader(in)
	var out, errb limited
	out.max, errb.max = 1<<20, 64<<10
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, 0, errors.New("command: no answer within the timeout")
		}
		msg := clip(strings.Join(strings.Fields(Redact(wholeText(errb.Bytes(), errb.cut), []string(c.Secrets)...)), " "), 300)
		if msg != "" {
			return nil, 0, fmt.Errorf("command: %v: %s", err, msg)
		}
		return nil, 0, fmt.Errorf("command: %v", err)
	}
	if out.cut {
		return nil, 0, errors.New("command: reply too large")
	}
	ans, n, err := decodeAnswers(out.Bytes())
	if err != nil {
		return nil, 0, fmt.Errorf("command: %w", err)
	}
	return ans, n, nil
}

// credentialEnv are the variables pitwall reads a Jev key from. A command
// provider never sees them.
var credentialEnv = []string{KeyEnv}

// withoutCredentials is env with every credentialEnv variable removed,
// matched without case as Windows does.
func withoutCredentials(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.ContainsFunc(credentialEnv, func(c string) bool { return strings.EqualFold(c, name) }) {
			out = append(out, kv)
		}
	}
	return out
}

// limited keeps the first max bytes written to it and drops the rest,
// noting in cut that it did.
type limited struct {
	bytes.Buffer
	max int
	cut bool
}

func (l *limited) Write(p []byte) (int, error) {
	room := l.max - l.Len()
	if len(p) > room {
		l.cut = true
	}
	if room > 0 {
		l.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
