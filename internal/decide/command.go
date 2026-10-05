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

// Ask runs the command. When ctx ends, the command is killed with every
// process in its group on Unix; on Windows only the command itself is
// killed, and output pipes a grandchild holds open are let go after
// WaitDelay.
func (c Command) Ask(ctx context.Context, r Request) (map[string]Answer, error) {
	if len(c.Argv) == 0 || c.Argv[0] == "" {
		return nil, errors.New("decisions.command is empty")
	}
	in, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	ownGroup(cmd)
	cmd.WaitDelay = 500 * time.Millisecond // stop waiting on pipes a stray child still holds
	cmd.Stdin = bytes.NewReader(in)
	var out, errb limited
	out.max, errb.max = 1<<20, 64<<10
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("command: no answer within the timeout")
		}
		msg := clip(strings.Join(strings.Fields(Redact(wholeText(errb.Bytes(), errb.cut))), " "), 300)
		if msg != "" {
			return nil, fmt.Errorf("command: %v: %s", err, msg)
		}
		return nil, fmt.Errorf("command: %v", err)
	}
	if out.cut {
		return nil, errors.New("command: reply too large")
	}
	ans, err := decodeAnswers(out.Bytes())
	if err != nil {
		return nil, fmt.Errorf("command: %w", err)
	}
	return ans, nil
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
