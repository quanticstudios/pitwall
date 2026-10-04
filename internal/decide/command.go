package decide

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Command runs a program per request: the request JSON on stdin, the reply
// on stdout in the shape Jev answers with, {"answers": {...}}. It lets a
// local classifier or another service stand in for Jev.
type Command struct {
	Argv []string
}

// Ask runs the command, killing it when ctx ends.
func (c Command) Ask(ctx context.Context, r Request) (map[string]Answer, error) {
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return nil, errors.New("decisions.command is empty")
	}
	in, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.WaitDelay = 500 * time.Millisecond // a child holding stdout open must not outlive the timeout
	cmd.Stdin = bytes.NewReader(in)
	var out, errb limited
	out.max, errb.max = 1<<20, 512
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("command: no answer within the timeout")
		}
		msg := strings.Join(strings.Fields(errb.String()), " ")
		if msg != "" {
			return nil, fmt.Errorf("command: %v: %s", err, Redact(msg))
		}
		return nil, fmt.Errorf("command: %v", err)
	}
	ans, err := decodeAnswers(out.Bytes())
	if err != nil {
		return nil, fmt.Errorf("command: %w", err)
	}
	return ans, nil
}

// limited keeps the first max bytes written to it and drops the rest.
type limited struct {
	bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.Len(); room > 0 {
		l.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
