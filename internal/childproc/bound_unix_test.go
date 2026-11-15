//go:build unix

package childproc

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestBoundCancelsDescendantsHoldingPipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 60 & wait")
	defer Bound(cmd)()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	if err := cmd.Run(); err == nil || ctx.Err() == nil {
		t.Fatalf("Run = %v, context = %v", err, ctx.Err())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancellation waited %s for a descendant's pipe", elapsed)
	}
}
