// Package teardown runs the command that stops a worktree's services before removal.
package teardown

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/quanticstudios/pitwall/internal/config"
	"github.com/quanticstudios/pitwall/internal/logs"
	"github.com/quanticstudios/pitwall/internal/model"
)

// Timeout is the maximum time a teardown command may run.
const Timeout = 2 * time.Minute

// Run returns a notice with the command and its output tail. A repo command
// needs approval of its exact text. Skipping it still allows removal.
func Run(ctx context.Context, path string, s config.WorktreeSettings, ports model.PortBlock, approved string) (string, error) {
	if s.Teardown == "" {
		return "", nil
	}
	if s.TeardownFromRepo && approved != s.Teardown {
		return record(fmt.Sprintf("Teardown skipped for %s: %s needs approval each time. Would run: %q", path, config.WorktreeFile, s.Teardown)), nil
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", s.Teardown)
	cmd.Dir = path
	cmd.Env = append(os.Environ(), ports.Env()...)
	ownGroup(cmd)
	// A child that keeps stdout open must not extend the time limit.
	cmd.WaitDelay = time.Second
	out := &tail{}
	cmd.Stdout, cmd.Stderr = out, out
	err := cmd.Run()
	if err != nil && cmd.Process != nil {
		_ = cmd.Cancel()
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	notice := fmt.Sprintf("Teardown for %s: %q", path, s.Teardown)
	if len(out.data) > 0 {
		notice += fmt.Sprintf("\nOutput tail: %q", strings.TrimSpace(string(out.data)))
	}
	notice = record(notice)
	if err != nil {
		return notice, fmt.Errorf("teardown failed; worktree kept: %w\n%s", err, notice)
	}
	return notice, nil
}

type tail struct{ data []byte }

func (t *tail) Write(p []byte) (int, error) {
	const limit = 8192
	n := len(p)
	if len(p) >= limit {
		t.data = append(t.data[:0], p[len(p)-limit:]...)
	} else {
		t.data = append(t.data[max(0, len(t.data)+len(p)-limit):], p...)
	}
	return n, nil
}

// record keeps the full command and tail readable when the GUI notice wraps.
func record(notice string) string {
	path := filepath.Join(config.StateDir(), "teardown.log")
	f, err := logs.OpenFile(path, 1<<20)
	if err == nil {
		_, err = fmt.Fprintf(f, "%s\n%s\n\n", time.Now().Format(time.RFC3339), notice)
		err = errors.Join(err, f.Close())
	}
	if err != nil {
		return notice + "\nCannot write teardown log: " + err.Error()
	}
	return "Teardown log: " + path + "\n" + notice
}
