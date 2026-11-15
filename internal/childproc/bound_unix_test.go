//go:build unix

package childproc

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBoundCancelsDescendantsHoldingPipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 60 & echo $!; wait")
	Bound(cmd)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	start := time.Now()
	if err := cmd.Run(); err == nil || ctx.Err() == nil {
		t.Fatalf("Run = %v, context = %v", err, ctx.Err())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation waited %s for a descendant's pipe", elapsed)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(stdout.String()))
	if err != nil {
		t.Fatalf("descendant pid %q: %v", stdout.String(), err)
	}
	// The killed sleep is reaped by whoever adopted it, not at once.
	for deadline := time.Now().Add(2 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("the descendant outlived the cancelled command")
		}
	}
}

func TestBoundCancelAfterExitSignalsNothing(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "true")
	Bound(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	// A context that ends just after Wait reaped the command calls Cancel.
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Cancel after exit = %v, want os.ErrProcessDone", err)
	}
}
